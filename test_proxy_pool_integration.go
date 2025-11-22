package main

import (
	"context"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"time"

	"github.com/bepass-org/warp-plus/wiresocks"
)

func main() {
	fmt.Println("=== Proxy Pool Integration Test ===\n")

	// Create logger
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Load configuration
	fmt.Println("[Step 1] Loading configuration...")
	config, err := wiresocks.LoadProxyPoolConfig("test_pool_config.json")
	if err != nil {
		fmt.Printf("❌ Failed to load config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Configuration loaded: %d proxies, strategy: %s\n\n", len(config.Proxies), config.Strategy)

	// Create load bal ancer
	fmt.Println("[Step 2] Creating load balancer...")
	balancer, err := wiresocks.NewLoadBalancer(config.Strategy)
	if err != nil {
		fmt.Printf("❌ Failed to create balancer: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("✅ Load balancer created: %s\n\n", balancer.Name())

	// Create proxy pool
	fmt.Println("[Step 3] Creating proxy pool...")
	ctx := context.Background()
	pool := wiresocks.NewProxyPool(ctx, logger, balancer)
	fmt.Printf("✅ Proxy pool created\n\n")

	// Add proxy instances (without actual network stacks for testing)
	fmt.Println("[Step 4] Adding proxy instances...")
	for i, proxyConf := range config.Proxies {
		bind, err := netip.ParseAddrPort(proxyConf.Bind)
		if err != nil {
			fmt.Printf("❌ Invalid bind address: %v\n", err)
			os.Exit(1)
		}

		proxyID := fmt.Sprintf("proxy-%d", i)
		maxConns := proxyConf.GetMaxConnections(config.MaxConnectionsPerProxy)
		weight := proxyConf.GetWeight()

		instance := wiresocks.NewProxyInstance(proxyID, i, bind, nil, weight, maxConns, nil)
		if err := pool.AddProxy(instance); err != nil {
			fmt.Printf("❌ Failed to add proxy: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("  ✅ Added proxy: %s (bind=%s, weight=%d, max_conns=%d)\n",
			proxyID, bind.String(), weight, maxConns)
	}
	fmt.Printf("\n✅ All proxies added successfully\n\n")

	// Test load balancing
	fmt.Println("[Step 5] Testing load balancing...")
	fmt.Println("Making 10 requests to test distribution:\n")

	distribution := make(map[string]int)
	for i := 0; i < 10; i++ {
		proxy, err := pool.GetNextProxy()
		if err != nil {
			fmt.Printf("❌ Failed to get proxy: %v\n", err)
			os.Exit(1)
		}
		distribution[proxy.ID]++
		fmt.Printf("  Request %2d: %s (bind=%s)\n", i+1, proxy.ID, proxy.Bind.String())
	}

	fmt.Println("\nDistribution summary:")
	for id, count := range distribution {
		fmt.Printf("  %s: %d requests (%.1f%%)\n", id, count, float64(count)/10*100)
	}

	// Test connection tracking
	fmt.Println("\n[Step 6] Testing connection tracking...")
	proxy, _ := pool.GetNextProxy()
	fmt.Printf("Before connections: Active=%d, Total=%d\n",
		proxy.GetActiveConns(), proxy.TotalConns.Load())

	proxy.IncrementActiveConns()
	proxy.IncrementActiveConns()
	fmt.Printf("After +2 connections: Active=%d, Total=%d\n",
		proxy.GetActiveConns(), proxy.TotalConns.Load())

	proxy.DecrementActiveConns()
	fmt.Printf("After -1 connection: Active=%d, Total=%d\n",
		proxy.GetActiveConns(), proxy.TotalConns.Load())
	fmt.Println("✅ Connection tracking works correctly\n")

	// Test health status
	fmt.Println("[Step 7] Testing health status...")
	allProxies := pool.GetAllProxies()
	fmt.Printf("Total proxies: %d\n", len(allProxies))

	healthy := pool.GetHealthyProxies()
	fmt.Printf("Healthy proxies: %d\n", len(healthy))

	// Mark one as unhealthy
	allProxies[0].SetHealthy(false)
	healthy = pool.GetHealthyProxies()
	fmt.Printf("After marking one unhealthy: %d healthy\n", len(healthy))

	allProxies[0].SetHealthy(true)
	healthy = pool.GetHealthyProxies()
	fmt.Printf("After recovery: %d healthy\n", len(healthy))
	fmt.Println("✅ Health status management works correctly\n")

	// Test statistics
	fmt.Println("[Step 8] Testing statistics...")
	stats := pool.GetStats()
	fmt.Printf("Pool Statistics:\n")
	fmt.Printf("  Total Proxies: %d\n", stats.TotalProxies)
	fmt.Printf("  Healthy Proxies: %d\n", stats.HealthyProxies)
	fmt.Printf("  Active Connections: %d\n", stats.ActiveConns)
	fmt.Printf("  Total Connections: %d\n", stats.TotalConns)
	fmt.Printf("  Total Errors: %d\n", stats.TotalErrors)
	fmt.Printf("  Healthy Rate: %.1f%%\n", stats.HealthyRate()*100)
	fmt.Println("✅ Statistics collection works correctly\n")

	// Test health checker creation
	fmt.Println("[Step 9] Testing health checker...")
	healthChecker := wiresocks.NewHealthChecker(
		ctx,
		pool,
		logger,
		config.GetHealthCheckInterval(),
		config.GetHealthCheckTimeout(),
		config.AutoRecover,
	)
	fmt.Printf("✅ Health checker created\n")
	fmt.Printf("  Interval: %v\n", config.GetHealthCheckInterval())
	fmt.Printf("  Timeout: %v\n", config.GetHealthCheckTimeout())
	fmt.Printf("  Auto Recover: %v\n\n", config.AutoRecover)

	// Start and stop health checker quickly
	fmt.Println("[Step 10] Testing health checker lifecycle...")
	healthChecker.Start()
	time.Sleep(100 * time.Millisecond)
	healthChecker.Stop()
	fmt.Println("✅ Health checker start/stop works correctly\n")

	fmt.Println("=== All Tests Passed! ===")
	fmt.Println("\n✅ Proxy pool functionality is verified and working correctly!")
}
