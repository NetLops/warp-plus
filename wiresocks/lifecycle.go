package wiresocks

import (
	"context"
	"log/slog"
	"math/rand"
	"sync"
	"time"
)

// RebuildFunc is the function signature for rebuilding a proxy
type RebuildFunc func(proxyIndex int) error

// LifecycleManager manages the lifecycle of proxies in the pool
type LifecycleManager struct {
	pool          *ProxyPool
	logger        *slog.Logger
	rebuildFunc   RebuildFunc
	lifetime      time.Duration
	rebuildDelay  time.Duration
	checkInterval time.Duration

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup

	// Track rebuilding proxies to avoid duplicate rebuilds
	rebuildingMu sync.Mutex
	rebuilding   map[string]bool

	// Semaphore to limit concurrent rebuilds
	rebuildSemaphore chan struct{}
}

// NewLifecycleManager creates a new lifecycle manager
func NewLifecycleManager(
	ctx context.Context,
	pool *ProxyPool,
	logger *slog.Logger,
	rebuildFunc RebuildFunc,
	lifetime time.Duration,
	rebuildDelay time.Duration,
	rebuildConcurrency int,
) *LifecycleManager {
	ctx, cancel := context.WithCancel(ctx)

	if rebuildConcurrency <= 0 {
		rebuildConcurrency = 2
	}

	return &LifecycleManager{
		pool:             pool,
		logger:           logger.With("subsystem", "lifecycle"),
		rebuildFunc:      rebuildFunc,
		lifetime:         lifetime,
		rebuildDelay:     rebuildDelay,
		checkInterval:    10 * time.Second, // Check every 10 seconds
		ctx:              ctx,
		cancel:           cancel,
		rebuilding:       make(map[string]bool),
		rebuildSemaphore: make(chan struct{}, rebuildConcurrency),
	}
}

// Start starts the lifecycle manager
func (lm *LifecycleManager) Start() {
	if lm.lifetime <= 0 {
		lm.logger.Info("proxy lifetime management disabled (lifetime=0)")
		return
	}

	lm.logger.Info("starting lifecycle manager",
		"lifetime", lm.lifetime,
		"check_interval", lm.checkInterval)

	lm.wg.Add(1)
	go lm.run()
}

// Stop stops the lifecycle manager
func (lm *LifecycleManager) Stop() {
	lm.cancel()
	lm.wg.Wait()
	lm.logger.Info("lifecycle manager stopped")
}

// run is the main loop for lifecycle management
func (lm *LifecycleManager) run() {
	defer lm.wg.Done()

	ticker := time.NewTicker(lm.checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-lm.ctx.Done():
			return
		case <-ticker.C:
			lm.checkProxies()
		}
	}
}

// checkProxies checks all proxies for expiration
func (lm *LifecycleManager) checkProxies() {
	proxies := lm.pool.GetAllProxies()
	now := time.Now()

	for _, p := range proxies {
		// Check if proxy has exceeded its lifetime
		if now.Sub(p.CreatedAt) > lm.lifetime {
			lm.TriggerRebuild(p.ID, p.Index)
		}
	}
}

// TriggerRebuild triggers a rebuild for a specific proxy
func (lm *LifecycleManager) TriggerRebuild(proxyID string, proxyIndex int) {
	lm.rebuildingMu.Lock()
	if lm.rebuilding[proxyID] {
		lm.rebuildingMu.Unlock()
		return // Already rebuilding
	}
	lm.rebuilding[proxyID] = true
	lm.rebuildingMu.Unlock()

	lm.logger.Info("triggering proxy rebuild",
		"proxy_id", proxyID,
		"proxy_index", proxyIndex,
		"reason", "lifetime_expired")

	// Run rebuild in a separate goroutine
	// Run rebuild in a separate goroutine
	go func() {
		// Acquire semaphore
		lm.rebuildSemaphore <- struct{}{}
		defer func() {
			<-lm.rebuildSemaphore
		}()

		defer func() {
			lm.rebuildingMu.Lock()
			delete(lm.rebuilding, proxyID)
			lm.rebuildingMu.Unlock()
		}()

		// Execute rebuild
		if err := lm.rebuildFunc(proxyIndex); err != nil {
			lm.logger.Error("failed to rebuild proxy",
				"proxy_index", proxyIndex,
				"error", err)

			// Schedule retry if needed (handled by ScheduleRebuild)
			lm.ScheduleRebuild(proxyIndex)
		} else {
			lm.logger.Info("proxy rebuild successful", "proxy_index", proxyIndex)
		}
	}()
}

// ScheduleRebuild schedules a rebuild after a delay (for failed proxies)
func (lm *LifecycleManager) ScheduleRebuild(proxyIndex int) {
	// Add +/- 20% jitter to rebuild delay to avoid thundering herd
	jitter := time.Duration(float64(lm.rebuildDelay) * (0.8 + 0.4*rand.Float64()))

	lm.logger.Info("scheduling proxy rebuild",
		"proxy_index", proxyIndex,
		"delay", jitter)

	time.AfterFunc(jitter, func() {
		// Acquire semaphore
		lm.rebuildSemaphore <- struct{}{}
		defer func() {
			<-lm.rebuildSemaphore
		}()

		// We don't have the ID here easily, but rebuildFunc will handle the replacement
		// Ideally we should pass the ID, but for now we rely on index
		if err := lm.rebuildFunc(proxyIndex); err != nil {
			lm.logger.Error("scheduled rebuild failed, rescheduling",
				"proxy_index", proxyIndex,
				"error", err)
			lm.ScheduleRebuild(proxyIndex) // Infinite retry? Maybe add limit later
		} else {
			lm.logger.Info("scheduled rebuild successful", "proxy_index", proxyIndex)
		}
	})
}
