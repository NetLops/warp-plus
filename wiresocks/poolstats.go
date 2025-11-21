package wiresocks

import (
	"time"
)

// ProxyStats contains statistics for a single proxy instance
type ProxyStats struct {
	ID              string    `json:"id"`
	Bind            string    `json:"bind"`
	Healthy         bool      `json:"healthy"`
	ActiveConns     int64     `json:"active_connections"`
	TotalConns      uint64    `json:"total_connections"`
	Errors          uint64    `json:"errors"`
	Weight          int       `json:"weight"`
	LastHealthCheck time.Time `json:"last_health_check"`
}

// PoolStats contains statistics for the entire proxy pool
type PoolStats struct {
	TotalProxies   int          `json:"total_proxies"`
	HealthyProxies int          `json:"healthy_proxies"`
	TotalConns     uint64       `json:"total_connections"`
	ActiveConns    int64        `json:"active_connections"`
	TotalErrors    uint64       `json:"total_errors"`
	ProxyStats     []ProxyStats `json:"proxy_stats"`
}

// ErrorRate calculates the error rate for a proxy
func (ps *ProxyStats) ErrorRate() float64 {
	if ps.TotalConns == 0 {
		return 0
	}
	return float64(ps.Errors) / float64(ps.TotalConns)
}

// ErrorRate calculates the overall error rate for the pool
func (ps *PoolStats) ErrorRate() float64 {
	if ps.TotalConns == 0 {
		return 0
	}
	return float64(ps.TotalErrors) / float64(ps.TotalConns)
}

// HealthyRate calculates the percentage of healthy proxies
func (ps *PoolStats) HealthyRate() float64 {
	if ps.TotalProxies == 0 {
		return 0
	}
	return float64(ps.HealthyProxies) / float64(ps.TotalProxies)
}
