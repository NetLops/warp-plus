package wiresocks

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/bepass-org/warp-plus/wireguard/tun/netstack"
)

// ProxyInstance represents a single proxy instance in the pool
type ProxyInstance struct {
	ID              string
	Index           int // Index in the configuration array
	Bind            netip.AddrPort
	Tnet            *netstack.Net
	ActiveConns     atomic.Int64
	TotalConns      atomic.Uint64
	Errors          atomic.Uint64
	Healthy         atomic.Bool
	Weight          int
	MaxConnections  int
	CreatedAt       time.Time
	LastHealthCheck time.Time
	mu              sync.RWMutex
}

// ProxyPool manages multiple proxy instances with load balancing
type ProxyPool struct {
	instances   []*ProxyInstance
	balancer    LoadBalancer
	healthCheck *HealthChecker
	logger      *slog.Logger
	ctx         context.Context
	mu          sync.RWMutex
}

// LoadBalancer defines the interface for load balancing strategies
type LoadBalancer interface {
	Select(instances []*ProxyInstance) (*ProxyInstance, error)
	Name() string
}

// NewProxyInstance creates a new proxy instance
func NewProxyInstance(id string, index int, bind netip.AddrPort, tnet *netstack.Net, weight, maxConns int) *ProxyInstance {
	instance := &ProxyInstance{
		ID:             id,
		Index:          index,
		Bind:           bind,
		Tnet:           tnet,
		Weight:         weight,
		MaxConnections: maxConns,
		CreatedAt:      time.Now(),
	}
	instance.Healthy.Store(true)
	instance.ActiveConns.Store(0)
	instance.TotalConns.Store(0)
	instance.Errors.Store(0)
	return instance
}

// IncrementActiveConns increments the active connection count
func (p *ProxyInstance) IncrementActiveConns() {
	p.ActiveConns.Add(1)
	p.TotalConns.Add(1)
}

// DecrementActiveConns decrements the active connection count
func (p *ProxyInstance) DecrementActiveConns() {
	p.ActiveConns.Add(-1)
}

// IncrementErrors increments the error count
func (p *ProxyInstance) IncrementErrors() {
	p.Errors.Add(1)
}

// IsHealthy returns whether the proxy is healthy
func (p *ProxyInstance) IsHealthy() bool {
	return p.Healthy.Load()
}

// SetHealthy sets the health status
func (p *ProxyInstance) SetHealthy(healthy bool) {
	p.Healthy.Store(healthy)
	p.mu.Lock()
	p.LastHealthCheck = time.Now()
	p.mu.Unlock()
}

// GetActiveConns returns the current active connection count
func (p *ProxyInstance) GetActiveConns() int64 {
	return p.ActiveConns.Load()
}

// CanAcceptConnection checks if the proxy can accept more connections
func (p *ProxyInstance) CanAcceptConnection() bool {
	if !p.IsHealthy() {
		return false
	}
	if p.MaxConnections > 0 && p.GetActiveConns() >= int64(p.MaxConnections) {
		return false
	}
	return true
}

// NewProxyPool creates a new proxy pool
func NewProxyPool(ctx context.Context, logger *slog.Logger, balancer LoadBalancer) *ProxyPool {
	return &ProxyPool{
		instances: make([]*ProxyInstance, 0),
		balancer:  balancer,
		logger:    logger.With("subsystem", "proxypool"),
		ctx:       ctx,
	}
}

// AddProxy adds a proxy instance to the pool
func (pp *ProxyPool) AddProxy(instance *ProxyInstance) error {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	// Check if proxy with same ID already exists
	for _, p := range pp.instances {
		if p.ID == instance.ID {
			return fmt.Errorf("proxy with ID %s already exists", instance.ID)
		}
	}

	pp.instances = append(pp.instances, instance)
	pp.logger.Info("proxy added to pool", "id", instance.ID, "bind", instance.Bind.String())
	return nil
}

// RemoveProxy removes a proxy instance from the pool
func (pp *ProxyPool) RemoveProxy(id string) error {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	for i, p := range pp.instances {
		if p.ID == id {
			pp.instances = append(pp.instances[:i], pp.instances[i+1:]...)
			pp.logger.Info("proxy removed from pool", "id", id)
			return nil
		}
	}

	return fmt.Errorf("proxy with ID %s not found", id)
}

// GetProxy returns a proxy instance by ID
func (pp *ProxyPool) GetProxy(id string) (*ProxyInstance, error) {
	pp.mu.RLock()
	defer pp.mu.RUnlock()

	for _, p := range pp.instances {
		if p.ID == id {
			return p, nil
		}
	}

	return nil, fmt.Errorf("proxy with ID %s not found", id)
}

// GetNextProxy selects the next available proxy using the configured load balancer
func (pp *ProxyPool) GetNextProxy() (*ProxyInstance, error) {
	pp.mu.RLock()
	defer pp.mu.RUnlock()

	if len(pp.instances) == 0 {
		return nil, errors.New("no proxies available in pool")
	}

	// Filter healthy and available proxies
	available := make([]*ProxyInstance, 0)
	for _, p := range pp.instances {
		if p.CanAcceptConnection() {
			available = append(available, p)
		}
	}

	if len(available) == 0 {
		return nil, errors.New("no healthy proxies available")
	}

	return pp.balancer.Select(available)
}

// GetAllProxies returns all proxy instances
func (pp *ProxyPool) GetAllProxies() []*ProxyInstance {
	pp.mu.RLock()
	defer pp.mu.RUnlock()

	proxies := make([]*ProxyInstance, len(pp.instances))
	copy(proxies, pp.instances)
	return proxies
}

// GetHealthyProxies returns only healthy proxy instances
func (pp *ProxyPool) GetHealthyProxies() []*ProxyInstance {
	pp.mu.RLock()
	defer pp.mu.RUnlock()

	healthy := make([]*ProxyInstance, 0)
	for _, p := range pp.instances {
		if p.IsHealthy() {
			healthy = append(healthy, p)
		}
	}
	return healthy
}

// Size returns the number of proxies in the pool
func (pp *ProxyPool) Size() int {
	pp.mu.RLock()
	defer pp.mu.RUnlock()
	return len(pp.instances)
}

// SetHealthChecker sets the health checker for the pool
func (pp *ProxyPool) SetHealthChecker(hc *HealthChecker) {
	pp.mu.Lock()
	defer pp.mu.Unlock()
	pp.healthCheck = hc
}

// GetStats returns statistics for the entire pool
func (pp *ProxyPool) GetStats() PoolStats {
	pp.mu.RLock()
	defer pp.mu.RUnlock()

	stats := PoolStats{
		TotalProxies:   len(pp.instances),
		HealthyProxies: 0,
		TotalConns:     0,
		ActiveConns:    0,
		TotalErrors:    0,
		ProxyStats:     make([]ProxyStats, 0, len(pp.instances)),
	}

	for _, p := range pp.instances {
		if p.IsHealthy() {
			stats.HealthyProxies++
		}

		activeConns := p.GetActiveConns()
		totalConns := p.TotalConns.Load()
		errors := p.Errors.Load()

		stats.ActiveConns += activeConns
		stats.TotalConns += totalConns
		stats.TotalErrors += errors

		p.mu.RLock()
		lastCheck := p.LastHealthCheck
		p.mu.RUnlock()

		stats.ProxyStats = append(stats.ProxyStats, ProxyStats{
			ID:              p.ID,
			Bind:            p.Bind.String(),
			Healthy:         p.IsHealthy(),
			ActiveConns:     activeConns,
			TotalConns:      totalConns,
			Errors:          errors,
			Weight:          p.Weight,
			LastHealthCheck: lastCheck,
		})
	}

	return stats
}

// ============== Load Balancer Implementations ==============

// RoundRobinBalancer implements round-robin load balancing
type RoundRobinBalancer struct {
	counter atomic.Uint64
}

func NewRoundRobinBalancer() *RoundRobinBalancer {
	return &RoundRobinBalancer{}
}

func (rb *RoundRobinBalancer) Select(instances []*ProxyInstance) (*ProxyInstance, error) {
	if len(instances) == 0 {
		return nil, errors.New("no instances available")
	}

	idx := rb.counter.Add(1) - 1
	return instances[idx%uint64(len(instances))], nil
}

func (rb *RoundRobinBalancer) Name() string {
	return "round-robin"
}

// LeastConnectionsBalancer selects the proxy with the least active connections
type LeastConnectionsBalancer struct{}

func NewLeastConnectionsBalancer() *LeastConnectionsBalancer {
	return &LeastConnectionsBalancer{}
}

func (lb *LeastConnectionsBalancer) Select(instances []*ProxyInstance) (*ProxyInstance, error) {
	if len(instances) == 0 {
		return nil, errors.New("no instances available")
	}

	var selected *ProxyInstance
	minConns := int64(-1)

	for _, p := range instances {
		conns := p.GetActiveConns()
		if minConns == -1 || conns < minConns {
			minConns = conns
			selected = p
		}
	}

	return selected, nil
}

func (lb *LeastConnectionsBalancer) Name() string {
	return "least-connections"
}

// RandomBalancer randomly selects a proxy
type RandomBalancer struct {
	rng *rand.Rand
	mu  sync.Mutex
}

func NewRandomBalancer() *RandomBalancer {
	return &RandomBalancer{
		rng: rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (rb *RandomBalancer) Select(instances []*ProxyInstance) (*ProxyInstance, error) {
	if len(instances) == 0 {
		return nil, errors.New("no instances available")
	}

	rb.mu.Lock()
	idx := rb.rng.Intn(len(instances))
	rb.mu.Unlock()

	return instances[idx], nil
}

func (rb *RandomBalancer) Name() string {
	return "random"
}

// WeightedRoundRobinBalancer implements weighted round-robin load balancing
type WeightedRoundRobinBalancer struct {
	counter atomic.Uint64
}

func NewWeightedRoundRobinBalancer() *WeightedRoundRobinBalancer {
	return &WeightedRoundRobinBalancer{}
}

func (wrb *WeightedRoundRobinBalancer) Select(instances []*ProxyInstance) (*ProxyInstance, error) {
	if len(instances) == 0 {
		return nil, errors.New("no instances available")
	}

	// Build weighted list
	weighted := make([]*ProxyInstance, 0)
	for _, p := range instances {
		weight := p.Weight
		if weight <= 0 {
			weight = 1
		}
		for i := 0; i < weight; i++ {
			weighted = append(weighted, p)
		}
	}

	if len(weighted) == 0 {
		return instances[0], nil
	}

	idx := wrb.counter.Add(1) - 1
	return weighted[idx%uint64(len(weighted))], nil
}

func (wrb *WeightedRoundRobinBalancer) Name() string {
	return "weighted-round-robin"
}

// NewLoadBalancer creates a load balancer based on strategy name
func NewLoadBalancer(strategy string) (LoadBalancer, error) {
	switch strategy {
	case "round-robin":
		return NewRoundRobinBalancer(), nil
	case "least-connections":
		return NewLeastConnectionsBalancer(), nil
	case "random":
		return NewRandomBalancer(), nil
	case "weighted-round-robin":
		return NewWeightedRoundRobinBalancer(), nil
	default:
		return nil, fmt.Errorf("unknown load balancing strategy: %s", strategy)
	}
}
