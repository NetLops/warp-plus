package main

import (
	"fmt"
	"os"

	"github.com/bepass-org/warp-plus/wiresocks"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("Usage: go run test_config_parse.go <config_file>")
		os.Exit(1)
	}

	configFile := os.Args[1]
	fmt.Printf("=== Testing Configuration Parsing ===\n")
	fmt.Printf("Config file: %s\n\n", configFile)

	config, err := wiresocks.LoadProxyPoolConfig(configFile)
	if err != nil {
		fmt.Printf("❌ Error loading config: %v\n", err)
		os.Exit(1)
	}

	if config == nil {
		fmt.Println("❌ Config is nil - no proxy_pool section found")
		os.Exit(1)
	}

	fmt.Println("✅ Configuration loaded successfully!")
	fmt.Printf("\nProxy Pool Configuration:\n")
	fmt.Printf("  Enabled: %v\n", config.Enabled)
	fmt.Printf("  Strategy: %s\n", config.Strategy)
	fmt.Printf("  Health Check Interval: %s\n", config.HealthCheckInterval)
	fmt.Printf("  Health Check Timeout: %s\n", config.HealthCheckTimeout)
	fmt.Printf("  Max Connections Per Proxy: %d\n", config.MaxConnectionsPerProxy)
	fmt.Printf("  Auto Recover: %v\n", config.AutoRecover)
	fmt.Printf("  Number of Proxies: %d\n\n", len(config.Proxies))

	if len(config.Proxies) > 0 {
		fmt.Println("Proxy Details:")
		for i, proxy := range config.Proxies {
			fmt.Printf("\n  Proxy %d:\n", i+1)
			fmt.Printf("    Bind: %s\n", proxy.Bind)
			fmt.Printf("    Endpoint: %s\n", proxy.Endpoint)
			fmt.Printf("    Weight: %d\n", proxy.Weight)
			fmt.Printf("    Max Connections: %d\n", proxy.MaxConnections)
		}
	}

	// Validate configuration
	fmt.Printf("\n=== Validating Configuration ===\n")
	err = config.Validate()
	if err != nil {
		fmt.Printf("❌ Validation failed: %v\n", err)
		os.Exit(1)
	}

	fmt.Println("✅ Configuration is valid!")
	fmt.Println("\n=== Test Complete ===")
}
