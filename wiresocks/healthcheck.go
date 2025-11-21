package wiresocks

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// HealthChecker manages health checking for proxy instances
type HealthChecker struct {
	pool            *ProxyPool
	interval        time.Duration
	timeout         time.Duration
	autoRecover     bool
	logger          *slog.Logger
	ctx             context.Context
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	testDestination string
}

// HealthCheckResult represents the result of a health check
type HealthCheckResult struct {
	ProxyID   string
	Healthy   bool
	Error     error
	Latency   time.Duration
	CheckedAt time.Time
}

// NewHealthChecker creates a new health checker
func NewHealthChecker(ctx context.Context, pool *ProxyPool, logger *slog.Logger, interval, timeout time.Duration, autoRecover bool) *HealthChecker {
	ctx, cancel := context.WithCancel(ctx)
	return &HealthChecker{
		pool:            pool,
		interval:        interval,
		timeout:         timeout,
		autoRecover:     autoRecover,
		logger:          logger.With("subsystem", "healthcheck"),
		ctx:             ctx,
		cancel:          cancel,
		testDestination: "1.1.1.1:80", // Cloudflare DNS as default test endpoint
	}
}

// SetTestDestination sets a custom test destination for health checks
func (hc *HealthChecker) SetTestDestination(dest string) {
	hc.testDestination = dest
}

// Start begins the health check loop
func (hc *HealthChecker) Start() {
	hc.wg.Add(1)
	go hc.healthCheckLoop()
	hc.logger.Info("health checker started", "interval", hc.interval, "timeout", hc.timeout)
}

// Stop stops the health check loop
func (hc *HealthChecker) Stop() {
	hc.cancel()
	hc.wg.Wait()
	hc.logger.Info("health checker stopped")
}

// healthCheckLoop runs the periodic health check
func (hc *HealthChecker) healthCheckLoop() {
	defer hc.wg.Done()

	ticker := time.NewTicker(hc.interval)
	defer ticker.Stop()

	// Run initial health check immediately
	hc.checkAllProxies()

	for {
		select {
		case <-hc.ctx.Done():
			return
		case <-ticker.C:
			hc.checkAllProxies()
		}
	}
}

// checkAllProxies performs health checks on all proxies
func (hc *HealthChecker) checkAllProxies() {
	proxies := hc.pool.GetAllProxies()
	if len(proxies) == 0 {
		return
	}

	hc.logger.Debug("running health checks", "proxy_count", len(proxies))

	var wg sync.WaitGroup
	results := make(chan HealthCheckResult, len(proxies))

	for _, proxy := range proxies {
		wg.Add(1)
		go func(p *ProxyInstance) {
			defer wg.Done()
			result := hc.checkProxy(p)
			results <- result
		}(proxy)
	}

	// Wait for all checks to complete
	go func() {
		wg.Wait()
		close(results)
	}()

	// Process results
	healthy := 0
	unhealthy := 0
	for result := range results {
		hc.handleHealthCheckResult(result)
		if result.Healthy {
			healthy++
		} else {
			unhealthy++
		}
	}

	hc.logger.Debug("health check completed", "healthy", healthy, "unhealthy", unhealthy)
}

// checkProxy performs a health check on a single proxy
func (hc *HealthChecker) checkProxy(proxy *ProxyInstance) HealthCheckResult {
	result := HealthCheckResult{
		ProxyID:   proxy.ID,
		CheckedAt: time.Now(),
	}

	start := time.Now()
	err := hc.testConnection(proxy)
	result.Latency = time.Since(start)

	if err != nil {
		result.Healthy = false
		result.Error = err
		hc.logger.Debug("proxy health check failed",
			"proxy_id", proxy.ID,
			"bind", proxy.Bind.String(),
			"error", err,
			"latency", result.Latency)
	} else {
		result.Healthy = true
		hc.logger.Debug("proxy health check passed",
			"proxy_id", proxy.ID,
			"bind", proxy.Bind.String(),
			"latency", result.Latency)
	}

	return result
}

// testConnection tests connectivity through a proxy
func (hc *HealthChecker) testConnection(proxy *ProxyInstance) error {
	if proxy.Tnet == nil {
		return fmt.Errorf("proxy network stack is nil")
	}

	ctx, cancel := context.WithTimeout(hc.ctx, hc.timeout)
	defer cancel()

	// Use the proxy's network stack to dial
	conn, err := proxy.Tnet.DialContext(ctx, "tcp", hc.testDestination)
	if err != nil {
		return fmt.Errorf("failed to connect: %w", err)
	}
	defer conn.Close()

	// Connection successful
	return nil
}

// handleHealthCheckResult processes a health check result
func (hc *HealthChecker) handleHealthCheckResult(result HealthCheckResult) {
	proxy, err := hc.pool.GetProxy(result.ProxyID)
	if err != nil {
		hc.logger.Error("failed to get proxy for health check result", "proxy_id", result.ProxyID, "error", err)
		return
	}

	wasHealthy := proxy.IsHealthy()
	isHealthy := result.Healthy

	// Update health status
	proxy.SetHealthy(isHealthy)

	// Handle state transitions
	if wasHealthy && !isHealthy {
		// Proxy became unhealthy
		hc.onProxyFailure(proxy, result.Error)
	} else if !wasHealthy && isHealthy && hc.autoRecover {
		// Proxy recovered
		hc.onProxyRecover(proxy)
	}
}

// onProxyFailure handles a proxy failure event
func (hc *HealthChecker) onProxyFailure(proxy *ProxyInstance, err error) {
	hc.logger.Warn("proxy marked as unhealthy",
		"proxy_id", proxy.ID,
		"bind", proxy.Bind.String(),
		"error", err,
		"active_connections", proxy.GetActiveConns())

	// Note: We don't close existing connections immediately
	// They will naturally end, and new connections will use healthy proxies
}

// onProxyRecover handles a proxy recovery event
func (hc *HealthChecker) onProxyRecover(proxy *ProxyInstance) {
	hc.logger.Info("proxy recovered and marked as healthy",
		"proxy_id", proxy.ID,
		"bind", proxy.Bind.String())
}

// CheckProxyNow performs an immediate health check on a specific proxy
func (hc *HealthChecker) CheckProxyNow(proxyID string) (*HealthCheckResult, error) {
	proxy, err := hc.pool.GetProxy(proxyID)
	if err != nil {
		return nil, err
	}

	result := hc.checkProxy(proxy)
	hc.handleHealthCheckResult(result)
	return &result, nil
}

// MarkProxyUnhealthy manually marks a proxy as unhealthy
func (hc *HealthChecker) MarkProxyUnhealthy(proxyID string, reason error) error {
	proxy, err := hc.pool.GetProxy(proxyID)
	if err != nil {
		return err
	}

	wasHealthy := proxy.IsHealthy()
	proxy.SetHealthy(false)

	if wasHealthy {
		hc.onProxyFailure(proxy, reason)
	}

	return nil
}

// MarkProxyHealthy manually marks a proxy as healthy
func (hc *HealthChecker) MarkProxyHealthy(proxyID string) error {
	proxy, err := hc.pool.GetProxy(proxyID)
	if err != nil {
		return err
	}

	wasHealthy := proxy.IsHealthy()
	proxy.SetHealthy(true)

	if !wasHealthy && hc.autoRecover {
		hc.onProxyRecover(proxy)
	}

	return nil
}
