package wiresocks

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"testing"
)

func TestRoundRobinBalancer(t *testing.T) {
	balancer := NewRoundRobinBalancer()

	// Create test instances
	instances := []*ProxyInstance{
		NewProxyInstance("proxy-0", mustParseAddrPort("127.0.0.1:8087"), nil, 1, 1000),
		NewProxyInstance("proxy-1", mustParseAddrPort("127.0.0.1:8088"), nil, 1, 1000),
		NewProxyInstance("proxy-2", mustParseAddrPort("127.0.0.1:8089"), nil, 1, 1000),
	}

	// Test round-robin selection
	selected := make(map[string]int)
	for i := 0; i < 9; i++ {
		proxy, err := balancer.Select(instances)
		if err != nil {
			t.Fatalf("Select failed: %v", err)
		}
		selected[proxy.ID]++
	}

	// Each proxy should be selected 3 times
	for id, count := range selected {
		if count != 3 {
			t.Errorf("Proxy %s selected %d times, expected 3", id, count)
		}
	}
}

func TestLeastConnectionsBalancer(t *testing.T) {
	balancer := NewLeastConnectionsBalancer()

	instances := []*ProxyInstance{
		NewProxyInstance("proxy-0", mustParseAddrPort("127.0.0.1:8087"), nil, 1, 1000),
		NewProxyInstance("proxy-1", mustParseAddrPort("127.0.0.1:8088"), nil, 1, 1000),
		NewProxyInstance("proxy-2", mustParseAddrPort("127.0.0.1:8089"), nil, 1, 1000),
	}

	// Set different connection counts
	instances[0].ActiveConns.Store(10)
	instances[1].ActiveConns.Store(5)
	instances[2].ActiveConns.Store(15)

	proxy, err := balancer.Select(instances)
	if err != nil {
		t.Fatalf("Select failed: %v", err)
	}

	// Should select proxy-1 with least connections (5)
	if proxy.ID != "proxy-1" {
		t.Errorf("Expected proxy-1, got %s", proxy.ID)
	}
}

func TestRandomBalancer(t *testing.T) {
	balancer := NewRandomBalancer()

	instances := []*ProxyInstance{
		NewProxyInstance("proxy-0", mustParseAddrPort("127.0.0.1:8087"), nil, 1, 1000),
		NewProxyInstance("proxy-1", mustParseAddrPort("127.0.0.1:8088"), nil, 1, 1000),
	}

	// Test random selection
	selected := make(map[string]bool)
	for i := 0; i < 100; i++ {
		proxy, err := balancer.Select(instances)
		if err != nil {
			t.Fatalf("Select failed: %v", err)
		}
		selected[proxy.ID] = true
	}

	// Both proxies should be selected at least once in 100 attempts
	if len(selected) != 2 {
		t.Errorf("Expected both proxies to be selected, got %d", len(selected))
	}
}

func TestWeightedRoundRobinBalancer(t *testing.T) {
	balancer := NewWeightedRoundRobinBalancer()

	instances := []*ProxyInstance{
		NewProxyInstance("proxy-0", mustParseAddrPort("127.0.0.1:8087"), nil, 1, 1000),
		NewProxyInstance("proxy-1", mustParseAddrPort("127.0.0.1:8088"), nil, 2, 1000),
		NewProxyInstance("proxy-2", mustParseAddrPort("127.0.0.1:8089"), nil, 3, 1000),
	}

	// Test weighted selection
	selected := make(map[string]int)
	totalWeight := 1 + 2 + 3 // 6
	for i := 0; i < totalWeight*10; i++ {
		proxy, err := balancer.Select(instances)
		if err != nil {
			t.Fatalf("Select failed: %v", err)
		}
		selected[proxy.ID]++
	}

	// Check weight distribution (approximately)
	// proxy-0: weight 1, should get ~1/6 = 10 selections
	// proxy-1: weight 2, should get ~2/6 = 20 selections
	// proxy-2: weight 3, should get ~3/6 = 30 selections
	if selected["proxy-0"] != 10 {
		t.Errorf("proxy-0 selected %d times, expected 10", selected["proxy-0"])
	}
	if selected["proxy-1"] != 20 {
		t.Errorf("proxy-1 selected %d times, expected 20", selected["proxy-1"])
	}
	if selected["proxy-2"] != 30 {
		t.Errorf("proxy-2 selected %d times, expected 30", selected["proxy-2"])
	}
}

func TestProxyPool_AddRemoveProxy(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	balancer := NewRoundRobinBalancer()
	pool := NewProxyPool(ctx, logger, balancer)

	// Add proxy
	instance := NewProxyInstance("proxy-0", mustParseAddrPort("127.0.0.1:8087"), nil, 1, 1000)
	err := pool.AddProxy(instance)
	if err != nil {
		t.Fatalf("AddProxy failed: %v", err)
	}

	if pool.Size() != 1 {
		t.Errorf("Expected pool size 1, got %d", pool.Size())
	}

	// Try to add duplicate
	err = pool.AddProxy(instance)
	if err == nil {
		t.Error("Expected error when adding duplicate proxy")
	}

	// Remove proxy
	err = pool.RemoveProxy("proxy-0")
	if err != nil {
		t.Fatalf("RemoveProxy failed: %v", err)
	}

	if pool.Size() != 0 {
		t.Errorf("Expected pool size 0, got %d", pool.Size())
	}

	// Try to remove non-existent proxy
	err = pool.RemoveProxy("proxy-999")
	if err == nil {
		t.Error("Expected error when removing non-existent proxy")
	}
}

func TestProxyPool_GetNextProxy(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	balancer := NewRoundRobinBalancer()
	pool := NewProxyPool(ctx, logger, balancer)

	// Test with empty pool
	_, err := pool.GetNextProxy()
	if err == nil {
		t.Error("Expected error when getting proxy from empty pool")
	}

	// Add proxies
	for i := 0; i < 3; i++ {
		instance := NewProxyInstance(
			mustSprintf("proxy-%d", i),
			mustParseAddrPort(mustSprintf("127.0.0.1:%d", 8087+i)),
			nil, 1, 1000,
		)
		pool.AddProxy(instance)
	}

	// Get proxy from pool
	proxy, err := pool.GetNextProxy()
	if err != nil {
		t.Fatalf("GetNextProxy failed: %v", err)
	}
	if proxy == nil {
		t.Error("Expected non-nil proxy")
	}
}

func TestProxyPool_HealthyProxies(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	balancer := NewRoundRobinBalancer()
	pool := NewProxyPool(ctx, logger, balancer)

	// Add proxies
	for i := 0; i < 3; i++ {
		instance := NewProxyInstance(
			mustSprintf("proxy-%d", i),
			mustParseAddrPort(mustSprintf("127.0.0.1:%d", 8087+i)),
			nil, 1, 1000,
		)
		pool.AddProxy(instance)
	}

	// Mark one as unhealthy
	proxy, _ := pool.GetProxy("proxy-1")
	proxy.SetHealthy(false)

	healthy := pool.GetHealthyProxies()
	if len(healthy) != 2 {
		t.Errorf("Expected 2 healthy proxies, got %d", len(healthy))
	}
}

func TestProxyInstance_Connections(t *testing.T) {
	instance := NewProxyInstance("proxy-0", mustParseAddrPort("127.0.0.1:8087"), nil, 1, 10)

	// Test increment/decrement
	instance.IncrementActiveConns()
	if instance.GetActiveConns() != 1 {
		t.Errorf("Expected 1 active connection, got %d", instance.GetActiveConns())
	}

	instance.DecrementActiveConns()
	if instance.GetActiveConns() != 0 {
		t.Errorf("Expected 0 active connections, got %d", instance.GetActiveConns())
	}

	// Test max connections
	for i := 0; i < 10; i++ {
		instance.IncrementActiveConns()
	}

	if instance.CanAcceptConnection() {
		t.Error("Should not accept connection when at max")
	}
}

func TestProxyPoolConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *ProxyPoolConfig
		wantErr bool
	}{
		{
			name: "valid config",
			config: &ProxyPoolConfig{
				Enabled:  true,
				Strategy: "round-robin",
				Proxies: []ProxyConfig{
					{Bind: "127.0.0.1:8087", Weight: 1},
				},
			},
			wantErr: false,
		},
		{
			name: "disabled config",
			config: &ProxyPoolConfig{
				Enabled: false,
			},
			wantErr: false,
		},
		{
			name: "no proxies",
			config: &ProxyPoolConfig{
				Enabled:  true,
				Strategy: "round-robin",
				Proxies:  []ProxyConfig{},
			},
			wantErr: true,
		},
		{
			name: "invalid strategy",
			config: &ProxyPoolConfig{
				Enabled:  true,
				Strategy: "invalid-strategy",
				Proxies: []ProxyConfig{
					{Bind: "127.0.0.1:8087"},
				},
			},
			wantErr: true,
		},
		{
			name: "invalid bind address",
			config: &ProxyPoolConfig{
				Enabled:  true,
				Strategy: "round-robin",
				Proxies: []ProxyConfig{
					{Bind: "invalid-address"},
				},
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if (err != nil) != tt.wantErr {
				t.Errorf("Validate() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestPoolStats(t *testing.T) {
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelError}))
	balancer := NewRoundRobinBalancer()
	pool := NewProxyPool(ctx, logger, balancer)

	// Add proxies
	for i := 0; i < 3; i++ {
		instance := NewProxyInstance(
			mustSprintf("proxy-%d", i),
			mustParseAddrPort(mustSprintf("127.0.0.1:%d", 8087+i)),
			nil, 1, 1000,
		)
		instance.IncrementActiveConns()
		instance.IncrementErrors()
		pool.AddProxy(instance)
	}

	stats := pool.GetStats()
	if stats.TotalProxies != 3 {
		t.Errorf("Expected 3 total proxies, got %d", stats.TotalProxies)
	}
	if stats.HealthyProxies != 3 {
		t.Errorf("Expected 3 healthy proxies, got %d", stats.HealthyProxies)
	}
	if stats.ActiveConns != 3 {
		t.Errorf("Expected 3 active connections, got %d", stats.ActiveConns)
	}
	if stats.TotalErrors != 3 {
		t.Errorf("Expected 3 total errors, got %d", stats.TotalErrors)
	}
}

// Helper functions for tests

func mustParseAddrPort(s string) netip.AddrPort {
	addr, err := netip.ParseAddrPort(s)
	if err != nil {
		panic(err)
	}
	return addr
}

func mustSprintf(format string, args ...interface{}) string {
	return fmt.Sprintf(format, args...)
}
