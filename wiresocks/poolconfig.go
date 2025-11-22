package wiresocks

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"time"
)

// ProxyConfig represents configuration for a single proxy
type ProxyConfig struct {
	Bind           string `json:"bind"`
	Endpoint       string `json:"endpoint"`
	Weight         int    `json:"weight"`
	MaxConnections int    `json:"max_connections"`
}

// ProxyPoolConfig represents the configuration for the proxy pool
type ProxyPoolConfig struct {
	Enabled                bool   `json:"enabled"`
	Strategy               string `json:"strategy"`
	HealthCheckInterval    string `json:"health_check_interval"`
	HealthCheckTimeout     string `json:"health_check_timeout"`
	MaxConnectionsPerProxy int    `json:"max_connections_per_proxy"`
	AutoRecover            bool   `json:"auto_recover"`
	// Concurrent initialization settings
	ConcurrentInit  int    `json:"concurrent_init"`   // Number of workers for concurrent init, 0 = auto
	InitTimeout     string `json:"init_timeout"`      // Timeout for single proxy init
	ContinueOnError bool   `json:"continue_on_error"` // Continue if some proxies fail
	// Lifecycle management settings
	ProxyLifetime      string `json:"proxy_lifetime"`      // Proxy lifetime duration string
	RebuildDelay       string `json:"rebuild_delay"`       // Rebuild delay duration string
	RebuildConcurrency int    `json:"rebuild_concurrency"` // Max concurrent rebuilds

	// Bulk creation settings
	NumProxies int    `json:"num_proxies"` // Number of proxies to create automatically
	StartPort  int    `json:"start_port"`  // Start port for automatic creation
	BindHost   string `json:"bind_host"`   // Bind host for automatic creation (default: 127.0.0.1)

	Proxies []ProxyConfig `json:"proxies"`
}

// Validate validates the proxy pool configuration
func (c *ProxyPoolConfig) Validate() error {
	if !c.Enabled {
		return nil
	}

	if len(c.Proxies) == 0 {
		return errors.New("at least one proxy must be configured")
	}

	// Validate strategy
	validStrategies := map[string]bool{
		"round-robin":          true,
		"least-connections":    true,
		"random":               true,
		"weighted-round-robin": true,
	}
	if !validStrategies[c.Strategy] {
		return fmt.Errorf("invalid strategy: %s (must be one of: round-robin, least-connections, random, weighted-round-robin)", c.Strategy)
	}

	// Validate health check interval
	if c.HealthCheckInterval != "" {
		if _, err := time.ParseDuration(c.HealthCheckInterval); err != nil {
			return fmt.Errorf("invalid health_check_interval: %w", err)
		}
	}

	// Validate health check timeout
	if c.HealthCheckTimeout != "" {
		if _, err := time.ParseDuration(c.HealthCheckTimeout); err != nil {
			return fmt.Errorf("invalid health_check_timeout: %w", err)
		}
	}

	// Validate init timeout
	if c.InitTimeout != "" {
		if _, err := time.ParseDuration(c.InitTimeout); err != nil {
			return fmt.Errorf("invalid init_timeout: %w", err)
		}
	}

	// Validate proxy lifetime
	if c.ProxyLifetime != "" {
		if _, err := time.ParseDuration(c.ProxyLifetime); err != nil {
			return fmt.Errorf("invalid proxy_lifetime: %w", err)
		}
	}

	// Validate rebuild delay
	if c.RebuildDelay != "" {
		if _, err := time.ParseDuration(c.RebuildDelay); err != nil {
			return fmt.Errorf("invalid rebuild_delay: %w", err)
		}
	}

	// Validate bulk creation settings
	if c.NumProxies > 0 {
		if c.StartPort <= 0 || c.StartPort > 65535 {
			return fmt.Errorf("invalid start_port: %d", c.StartPort)
		}
		// Check for port overflow
		if c.StartPort+c.NumProxies-1 > 65535 {
			return fmt.Errorf("port range overflow: start_port %d + num_proxies %d > 65535", c.StartPort, c.NumProxies)
		}
	}

	// Validate each proxy config
	for i, proxy := range c.Proxies {
		if err := proxy.Validate(); err != nil {
			return fmt.Errorf("proxy %d: %w", i, err)
		}
	}

	return nil
}

// GetHealthCheckInterval returns the health check interval as a duration
func (c *ProxyPoolConfig) GetHealthCheckInterval() time.Duration {
	if c.HealthCheckInterval == "" {
		return 30 * time.Second
	}
	d, _ := time.ParseDuration(c.HealthCheckInterval)
	return d
}

// GetHealthCheckTimeout returns the health check timeout as a duration
func (c *ProxyPoolConfig) GetHealthCheckTimeout() time.Duration {
	if c.HealthCheckTimeout == "" {
		return 5 * time.Second
	}
	d, _ := time.ParseDuration(c.HealthCheckTimeout)
	return d
}

// GetInitTimeout returns the init timeout as a duration
func (c *ProxyPoolConfig) GetInitTimeout() time.Duration {
	if c.InitTimeout == "" {
		return 30 * time.Second
	}
	d, _ := time.ParseDuration(c.InitTimeout)
	return d
}

// GetProxyLifetime returns the proxy lifetime as a duration
func (c *ProxyPoolConfig) GetProxyLifetime() time.Duration {
	if c.ProxyLifetime == "" {
		return 0 // 0 means permanent
	}
	d, _ := time.ParseDuration(c.ProxyLifetime)
	return d
}

// GetRebuildDelay returns the rebuild delay as a duration
func (c *ProxyPoolConfig) GetRebuildDelay() time.Duration {
	if c.RebuildDelay == "" {
		return 10 * time.Second
	}
	d, _ := time.ParseDuration(c.RebuildDelay)
	return d
}

// GetRebuildConcurrency returns the max concurrent rebuilds
func (c *ProxyPoolConfig) GetRebuildConcurrency() int {
	if c.RebuildConcurrency <= 0 {
		return 2 // Default to 2
	}
	return c.RebuildConcurrency
}

// GetConcurrentInit returns the number of concurrent init workers
func (c *ProxyPoolConfig) GetConcurrentInit() int {
	if c.ConcurrentInit <= 0 {
		return 0 // 0 means auto
	}
	return c.ConcurrentInit
}

// Validate validates a single proxy configuration
func (c *ProxyConfig) Validate() error {
	if c.Bind == "" {
		return errors.New("bind address is required")
	}

	// Validate bind address format
	if _, err := netip.ParseAddrPort(c.Bind); err != nil {
		return fmt.Errorf("invalid bind address: %w", err)
	}

	// Endpoint can be empty (will use default or scan)
	// Weight defaults to 1 if not set
	if c.Weight < 0 {
		return errors.New("weight cannot be negative")
	}

	if c.MaxConnections < 0 {
		return errors.New("max_connections cannot be negative")
	}

	return nil
}

// GetWeight returns the weight, defaulting to 1 if not set
func (c *ProxyConfig) GetWeight() int {
	if c.Weight <= 0 {
		return 1
	}
	return c.Weight
}

// GetMaxConnections returns the max connections, using global default if not set
func (c *ProxyConfig) GetMaxConnections(globalDefault int) int {
	if c.MaxConnections > 0 {
		return c.MaxConnections
	}
	if globalDefault > 0 {
		return globalDefault
	}
	return 0 // 0 means unlimited
}

// LoadProxyPoolConfig loads proxy pool configuration from a file
// Supports two formats:
// 1. Standalone proxy pool config (direct ProxyPoolConfig JSON)
// 2. Full config with "proxy_pool" key (for backward compatibility)
func LoadProxyPoolConfig(filename string) (*ProxyPoolConfig, error) {
	data, err := os.ReadFile(filename)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	// First, try to parse as standalone proxy pool config
	var poolConfig ProxyPoolConfig
	if err := json.Unmarshal(data, &poolConfig); err == nil {
		// Check if this looks like a valid standalone config
		if poolConfig.Strategy != "" || len(poolConfig.Proxies) > 0 {
			return &poolConfig, nil
		}
	}

	// If standalone parse failed or looks empty, try parsing as full config with "proxy_pool" key
	var fullConfig struct {
		ProxyPool *ProxyPoolConfig `json:"proxy_pool"`
	}
	if err := json.Unmarshal(data, &fullConfig); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if fullConfig.ProxyPool == nil {
		return nil, nil // No proxy pool config found
	}

	return fullConfig.ProxyPool, nil
}

// DefaultProxyPoolConfig returns a default proxy pool configuration
func DefaultProxyPoolConfig() *ProxyPoolConfig {
	return &ProxyPoolConfig{
		Enabled:                false,
		Strategy:               "round-robin",
		HealthCheckInterval:    "30s",
		HealthCheckTimeout:     "5s",
		MaxConnectionsPerProxy: 1000,
		AutoRecover:            true,
		ConcurrentInit:         0,
		InitTimeout:            "30s",
		ContinueOnError:        true,
		ProxyLifetime:          "0",
		RebuildDelay:           "10s",
		RebuildConcurrency:     2,
		NumProxies:             0,
		StartPort:              0,
		BindHost:               "127.0.0.1",
		Proxies:                []ProxyConfig{},
	}
}
