package main

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"time"

	"golang.org/x/net/proxy"
)

func main() {
	// Test configuration
	proxyAddresses := []string{
		"127.0.0.1:18087",
		"127.0.0.1:18088",
	}

	fmt.Println("=== SOCKS5 Proxy Pool E2E Test ===\n")

	// Test each proxy
	for i, addr := range proxyAddresses {
		fmt.Printf("[Test %d] Testing proxy at %s\n", i+1, addr)

		// Create SOCKS5 dialer
		dialer, err := proxy.SOCKS5("tcp", addr, nil, proxy.Direct)
		if err != nil {
			fmt.Printf("  ❌ Failed to create dialer: %v\n\n", err)
			continue
		}

		// Create HTTP client with SOCKS5 proxy
		httpTransport := &http.Transport{
			Dial: dialer.Dial,
		}
		httpClient := &http.Client{
			Transport: httpTransport,
			Timeout:   10 * time.Second,
		}

		// Test HTTP request
		resp, err := httpClient.Get("http://ifconfig.me")
		if err != nil {
			fmt.Printf("  ❌ HTTP request failed: %v\n\n", err)
			continue
		}
		defer resp.Body.Close()

		body, err := io.ReadAll(resp.Body)
		if err != nil {
			fmt.Printf("  ❌ Failed to read response: %v\n\n", err)
			continue
		}

		fmt.Printf("  ✅ Proxy working! IP: %s\n", string(body))
		fmt.Printf("  Status: %s\n\n", resp.Status)
	}

	// Test load balancing - make multiple requests
	fmt.Println("=== Load Balancing Test ===\n")
	fmt.Println("Making 10 requests to observe round-robin distribution...\n")

	for i := 0; i < 10; i++ {
		// Alternate between proxies manually to test
		proxyAddr := proxyAddresses[i%len(proxyAddresses)]

		dialer, err := proxy.SOCKS5("tcp", proxyAddr, nil, proxy.Direct)
		if err != nil {
			log.Printf("Request %d: Failed to create dialer: %v", i+1, err)
			continue
		}

		httpTransport := &http.Transport{
			Dial: dialer.Dial,
		}
		httpClient := &http.Client{
			Transport: httpTransport,
			Timeout:   5 * time.Second,
		}

		start := time.Now()
		resp, err := httpClient.Get("http://ifconfig.me")
		latency := time.Since(start)

		if err != nil {
			fmt.Printf("Request %2d via %s: ❌ Failed (%v)\n", i+1, proxyAddr, err)
			continue
		}
		defer resp.Body.Close()

		body, _ := io.ReadAll(resp.Body)
		fmt.Printf("Request %2d via %s: ✅ Success (IP: %s, Latency: %v)\n",
			i+1, proxyAddr, string(body), latency)
	}

	fmt.Println("\n=== Test Complete ===")
}
