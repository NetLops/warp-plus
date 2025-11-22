package app

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"path"
	"runtime"
	"sync"
	"time"

	"golang.org/x/time/rate"

	"github.com/bepass-org/warp-plus/iputils"
	"github.com/bepass-org/warp-plus/psiphon"
	"github.com/bepass-org/warp-plus/warp"
	"github.com/bepass-org/warp-plus/wireguard/device"
	"github.com/bepass-org/warp-plus/wireguard/tun"
	"github.com/bepass-org/warp-plus/wireguard/tun/netstack"
	"github.com/bepass-org/warp-plus/wiresocks"
)

const singleMTU = 1330
const doubleMTU = 1280 // minimum mtu for IPv6, may cause frag reassembly somewhere

type WarpOptions struct {
	Bind            netip.AddrPort
	Endpoint        string
	License         string
	DnsAddr         netip.Addr
	Psiphon         *PsiphonOptions
	Gool            bool
	Scan            *wiresocks.ScanOptions
	CacheDir        string
	FwMark          uint32
	WireguardConfig string
	Reserved        string
	TestURL         string
	ProxyPoolConfig *wiresocks.ProxyPoolConfig // Proxy pool configuration
}

type PsiphonOptions struct {
	Country string
}

func RunWarp(ctx context.Context, l *slog.Logger, opts WarpOptions) error {
	if opts.WireguardConfig != "" {
		if err := runWireguard(ctx, l, opts); err != nil {
			return err
		}

		return nil
	}

	if opts.Psiphon != nil && opts.Gool {
		return errors.New("can't use psiphon and gool at the same time")
	}

	if opts.Psiphon != nil && opts.Psiphon.Country == "" {
		return errors.New("must provide country for psiphon")
	}

	if opts.Endpoint == "" {
		ident, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "primary"), opts.License)
		if err != nil {
			l.Error("failed to load or create primary warp identity")
			return err
		}

		opts.Endpoint = ident.Config.Peers[0].Endpoint.V4[:len(ident.Config.Peers[0].Endpoint.V4)-2] + ":500"
	}

	// Decide Working Scenario
	endpoints := []string{opts.Endpoint, opts.Endpoint}

	if opts.Scan != nil {
		// make primary identity
		ident, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "primary"), opts.License)
		if err != nil {
			l.Error("couldn't load primary warp identity")
			return err
		}

		// Reading the private key from the 'Interface' section
		opts.Scan.PrivateKey = ident.PrivateKey

		// Reading the public key from the 'Peer' section
		opts.Scan.PublicKey = ident.Config.Peers[0].PublicKey

		res, err := wiresocks.RunScan(ctx, l, *opts.Scan)
		if err != nil {
			return err
		}

		l.Debug("scan results", "endpoints", res)

		endpoints = make([]string, len(res))
		for i := 0; i < len(res); i++ {
			endpoints[i] = res[i].AddrPort.String()
		}
	}
	l.Info("using warp endpoints", "endpoints", endpoints)

	// Check if proxy pool is enabled
	if opts.ProxyPoolConfig != nil && opts.ProxyPoolConfig.Enabled {
		l.Info("running in proxy pool mode")
		return runWarpWithProxyPool(ctx, l, opts, endpoints)
	}

	var warpErr error
	switch {
	case opts.Psiphon != nil:
		l.Info("running in Psiphon (cfon) mode")
		// run primary warp on a random tcp port and run psiphon on bind address
		warpErr = runWarpWithPsiphon(ctx, l, opts, endpoints[0])
	case opts.Gool:
		l.Info("running in warp-in-warp (gool) mode")
		// run warp in warp
		warpErr = runWarpInWarp(ctx, l, opts, endpoints[0])
	default:
		l.Info("running in normal warp mode")
		// just run primary warp on bindAddress
		warpErr = runWarp(ctx, l, opts, endpoints[0])
	}

	return warpErr
}

func runWireguard(ctx context.Context, l *slog.Logger, opts WarpOptions) error {
	conf, err := wiresocks.ParseConfig(opts.WireguardConfig)
	if err != nil {
		return err
	}

	// Set up MTU
	conf.Interface.MTU = singleMTU
	// Set up DNS Address
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	// Enable trick and keepalive on all peers in config
	for i, peer := range conf.Peers {
		peer.Trick = true
		peer.KeepAlive = 5

		// Try resolving if the endpoint is a domain
		addr, err := iputils.ParseResolveAddressPort(peer.Endpoint, false, opts.DnsAddr.String())
		if err == nil {
			peer.Endpoint = addr.String()
		}

		conf.Peers[i] = peer
	}

	// Establish wireguard on userspace stack
	var werr error
	var tnet *netstack.Net
	var tunDev tun.Device
	var dev *device.Device
	for _, t := range []string{"t1", "t2"} {
		// Create userspace tun network stack
		tunDev, tnet, werr = netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
		if werr != nil {
			continue
		}

		dev, werr = establishWireguard(l, conf, tunDev, opts.FwMark, t)
		if werr != nil {
			continue
		}

		// Test wireguard connectivity
		werr = usermodeTunTest(ctx, l, tnet, opts.TestURL)
		if werr != nil {
			continue
		}
		break
	}
	if werr != nil {
		return werr
	}

	// Run a proxy on the userspace stack
	// Run a proxy on the userspace stack
	_, cleanup, err := wiresocks.StartProxy(ctx, l, tnet, dev, opts.Bind)
	if err != nil {
		return err
	}
	defer cleanup()

	l.Info("serving proxy", "address", opts.Bind)

	return nil
}

func runWarp(ctx context.Context, l *slog.Logger, opts WarpOptions, endpoint string) error {
	// make primary identity
	ident, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "primary"), opts.License)
	if err != nil {
		l.Error("couldn't load primary warp identity")
		return err
	}

	conf := generateWireguardConfig(ident)

	// Set up MTU
	conf.Interface.MTU = singleMTU
	// Set up DNS Address
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	// Enable trick and keepalive on all peers in config
	for i, peer := range conf.Peers {
		peer.Endpoint = endpoint
		peer.Trick = true
		peer.KeepAlive = 5

		if opts.Reserved != "" {
			r, err := wiresocks.ParseReserved(opts.Reserved)
			if err != nil {
				return err
			}
			peer.Reserved = r
		}

		conf.Peers[i] = peer
	}

	// Establish wireguard on userspace stack
	var werr error
	var tnet *netstack.Net
	var tunDev tun.Device
	var dev *device.Device
	for _, t := range []string{"t1", "t2"} {
		tunDev, tnet, werr = netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
		if werr != nil {
			continue
		}

		dev, werr = establishWireguard(l, &conf, tunDev, opts.FwMark, t)
		if werr != nil {
			continue
		}

		// Test wireguard connectivity
		werr = usermodeTunTest(ctx, l, tnet, opts.TestURL)
		if werr != nil {
			continue
		}
		break
	}
	if werr != nil {
		return werr
	}

	// Run a proxy on the userspace stack
	// Run a proxy on the userspace stack
	_, cleanup, err := wiresocks.StartProxy(ctx, l, tnet, dev, opts.Bind)
	if err != nil {
		return err
	}
	defer cleanup()

	l.Info("serving proxy", "address", opts.Bind)
	return nil
}

func runWarpInWarp(ctx context.Context, l *slog.Logger, opts WarpOptions, endpoint string) error {
	// make primary identity
	ident1, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "primary"), opts.License)
	if err != nil {
		l.Error("couldn't load primary warp identity")
		return err
	}

	conf := generateWireguardConfig(ident1)

	// Set up MTU
	conf.Interface.MTU = singleMTU
	// Set up DNS Address
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	// Enable trick and keepalive on all peers in config
	for i, peer := range conf.Peers {
		peer.Endpoint = endpoint
		peer.Trick = true
		peer.KeepAlive = 5

		if opts.Reserved != "" {
			r, err := wiresocks.ParseReserved(opts.Reserved)
			if err != nil {
				return err
			}
			peer.Reserved = r
		}

		conf.Peers[i] = peer
	}

	// Establish wireguard on userspace stack and bind the wireguard sockets to the default interface and apply
	var werr error
	var tnet1 *netstack.Net
	var tunDev tun.Device
	for _, t := range []string{"t1", "t2"} {
		// Create userspace tun network stack
		tunDev, tnet1, werr = netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
		if werr != nil {
			continue
		}

		_, werr = establishWireguard(l.With("gool", "outer"), &conf, tunDev, opts.FwMark, t)
		if werr != nil {
			continue
		}

		// Test wireguard connectivity
		werr = usermodeTunTest(ctx, l, tnet1, opts.TestURL)
		if werr != nil {
			continue
		}
		break
	}
	if werr != nil {
		return werr
	}

	// Create a UDP port forward between localhost and the remote endpoint
	addr, err := wiresocks.NewVtunUDPForwarder(ctx, netip.MustParseAddrPort("127.0.0.1:0"), endpoint, tnet1, singleMTU)
	if err != nil {
		return err
	}

	// make secondary
	ident2, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "secondary"), opts.License)
	if err != nil {
		l.Error("couldn't load secondary warp identity")
		return err
	}

	conf = generateWireguardConfig(ident2)

	// Set up MTU
	conf.Interface.MTU = doubleMTU
	// Set up DNS Address
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	// Enable keepalive on all peers in config
	for i, peer := range conf.Peers {
		peer.Endpoint = addr.String()
		peer.KeepAlive = 20

		if opts.Reserved != "" {
			r, err := wiresocks.ParseReserved(opts.Reserved)
			if err != nil {
				return err
			}
			peer.Reserved = r
		}

		conf.Peers[i] = peer
	}

	// Create userspace tun network stack
	tunDev, tnet2, err := netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
	if err != nil {
		return err
	}

	// Establish wireguard on userspace stack
	dev2, err := establishWireguard(l.With("gool", "inner"), &conf, tunDev, opts.FwMark, "t0")
	if err != nil {
		return err
	}

	// Test wireguard connectivity
	if err := usermodeTunTest(ctx, l, tnet2, opts.TestURL); err != nil {
		return err
	}

	_, cleanup, err := wiresocks.StartProxy(ctx, l, tnet2, dev2, opts.Bind)
	if err != nil {
		return err
	}
	defer cleanup()

	l.Info("serving proxy", "address", opts.Bind)
	return nil
}

func runWarpWithPsiphon(ctx context.Context, l *slog.Logger, opts WarpOptions, endpoint string) error {
	// make primary identity
	ident, err := warp.LoadOrCreateIdentity(l, path.Join(opts.CacheDir, "primary"), opts.License)
	if err != nil {
		l.Error("couldn't load primary warp identity")
		return err
	}

	conf := generateWireguardConfig(ident)

	// Set up MTU
	conf.Interface.MTU = singleMTU
	// Set up DNS Address
	conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

	// Enable trick and keepalive on all peers in config
	for i, peer := range conf.Peers {
		peer.Endpoint = endpoint
		peer.Trick = true
		peer.KeepAlive = 5

		if opts.Reserved != "" {
			r, err := wiresocks.ParseReserved(opts.Reserved)
			if err != nil {
				return err
			}
			peer.Reserved = r
		}

		conf.Peers[i] = peer
	}

	// Establish wireguard on userspace stack
	var werr error
	var tnet *netstack.Net
	var tunDev tun.Device
	var dev *device.Device
	for _, t := range []string{"t1", "t2"} {
		tunDev, tnet, werr = netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
		if werr != nil {
			continue
		}

		dev, werr = establishWireguard(l, &conf, tunDev, opts.FwMark, t)
		if werr != nil {
			continue
		}

		// Test wireguard connectivity
		werr = usermodeTunTest(ctx, l, tnet, opts.TestURL)
		if werr != nil {
			continue
		}
		break
	}
	if werr != nil {
		return werr
	}

	// Run a proxy on the userspace stack
	// Run a proxy on the userspace stack
	warpBind, cleanup, err := wiresocks.StartProxy(ctx, l, tnet, dev, netip.MustParseAddrPort("127.0.0.1:0"))
	if err != nil {
		return err
	}
	defer cleanup()

	// run psiphon
	err = psiphon.RunPsiphon(ctx, l.With("subsystem", "psiphon"), warpBind, opts.CacheDir, opts.Bind, opts.Psiphon.Country)
	if err != nil {
		return fmt.Errorf("unable to run psiphon %w", err)
	}

	l.Info("serving proxy", "address", opts.Bind)
	return nil
}

func generateWireguardConfig(i *warp.Identity) wiresocks.Configuration {
	priv, _ := wiresocks.EncodeBase64ToHex(i.PrivateKey)
	pub, _ := wiresocks.EncodeBase64ToHex(i.Config.Peers[0].PublicKey)
	clientID, _ := base64.StdEncoding.DecodeString(i.Config.ClientID)
	return wiresocks.Configuration{
		Interface: &wiresocks.InterfaceConfig{
			PrivateKey: priv,
			Addresses: []netip.Addr{
				netip.MustParseAddr(i.Config.Interface.Addresses.V4),
				netip.MustParseAddr(i.Config.Interface.Addresses.V6),
			},
		},
		Peers: []wiresocks.PeerConfig{{
			PublicKey:    pub,
			PreSharedKey: "0000000000000000000000000000000000000000000000000000000000000000",
			AllowedIPs: []netip.Prefix{
				netip.MustParsePrefix("0.0.0.0/0"),
				netip.MustParsePrefix("::/0"),
			},
			Endpoint: i.Config.Peers[0].Endpoint.Host,
			Reserved: [3]byte{clientID[0], clientID[1], clientID[2]},
		}},
	}
}

// runWarpWithProxyPool runs warp in proxy pool mode with multiple endpoints
func runWarpWithProxyPool(ctx context.Context, l *slog.Logger, opts WarpOptions, endpoints []string) error {
	config := opts.ProxyPoolConfig
	if config == nil || !config.Enabled {
		return errors.New("proxy pool not enabled")
	}

	// Handle bulk proxy creation
	if config.NumProxies > 0 {
		l.Info("generating bulk proxy configuration",
			"num_proxies", config.NumProxies,
			"start_port", config.StartPort,
			"bind_host", config.BindHost)

		bindHost := config.BindHost
		if bindHost == "" {
			bindHost = "127.0.0.1"
		}

		for i := 0; i < config.NumProxies; i++ {
			port := config.StartPort + i
			bindAddr := fmt.Sprintf("%s:%d", bindHost, port)

			config.Proxies = append(config.Proxies, wiresocks.ProxyConfig{
				Bind: bindAddr,
				// Endpoint, Weight, MaxConnections use defaults or empty (auto-assigned later)
			})
		}
	}

	numProxies := len(config.Proxies)
	if numProxies == 0 {
		return errors.New("no proxies configured")
	}

	// Determine concurrency
	concurrentInit := config.GetConcurrentInit()
	if concurrentInit == 0 {
		concurrentInit = runtime.NumCPU() * 2
		if concurrentInit > numProxies {
			concurrentInit = numProxies
		}
		if concurrentInit < 1 {
			concurrentInit = 1
		}
	}

	l.Info("initializing proxy pool",
		"proxy_count", numProxies,
		"concurrent_workers", concurrentInit,
		"init_timeout", config.GetInitTimeout())

	// Create network stacks for each proxy
	tnets := make([]*netstack.Net, numProxies)

	// Worker pool for initialization
	type initJob struct {
		index int
	}
	type initResult struct {
		index int
		tnet  *netstack.Net
		dev   *device.Device
		err   error
	}

	jobs := make(chan initJob, numProxies)
	results := make(chan initResult, numProxies)

	// Create rate limiter for identity registration (2 requests per second, burst 5)
	// This helps avoid 429 Too Many Requests errors from the API
	regLimiter := rate.NewLimiter(2, 5)

	// initProxy initializes a single proxy and returns the netstack
	initProxy := func(i int) (*device.Device, *netstack.Net, error) {
		proxyConf := config.Proxies[i]

		// Create a context with timeout for this initialization
		initCtx, cancel := context.WithTimeout(ctx, config.GetInitTimeout())
		defer cancel()

		// Determine endpoint for this proxy
		endpoint := opts.Endpoint
		if proxyConf.Endpoint != "" {
			endpoint = proxyConf.Endpoint
		} else if i < len(endpoints) {
			endpoint = endpoints[i]
		} else {
			endpoint = endpoints[0]
		}

		// Create identity for this proxy
		identPath := path.Join(opts.CacheDir, fmt.Sprintf("pool-proxy-%d", i))

		// Wait for rate limiter before attempting registration
		if err := regLimiter.Wait(initCtx); err != nil {
			return nil, nil, fmt.Errorf("rate limiter wait failed: %w", err)
		}

		ident, err := warp.LoadOrCreateIdentity(l, identPath, opts.License)
		if err != nil {
			return nil, nil, fmt.Errorf("couldn't load proxy identity: %w", err)
		}

		conf := generateWireguardConfig(ident)

		// Set up MTU
		conf.Interface.MTU = singleMTU
		// Set up DNS Address
		conf.Interface.DNS = []netip.Addr{opts.DnsAddr}

		// Configure peer
		for j, peer := range conf.Peers {
			peer.Endpoint = endpoint
			peer.Trick = true
			peer.KeepAlive = 5

			if opts.Reserved != "" {
				r, err := wiresocks.ParseReserved(opts.Reserved)
				if err != nil {
					return nil, nil, err
				}
				peer.Reserved = r
			}

			conf.Peers[j] = peer
		}

		// Establish wireguard on userspace stack
		var werr error
		var tnet *netstack.Net
		var tunDev tun.Device
		var dev *device.Device

		// Try t1 then t2
		for _, t := range []string{"t1", "t2"} {
			select {
			case <-initCtx.Done():
				return nil, nil, initCtx.Err()
			default:
			}

			tunDev, tnet, werr = netstack.CreateNetTUN(conf.Interface.Addresses, conf.Interface.DNS, conf.Interface.MTU)
			if werr != nil {
				continue
			}

			dev, werr = establishWireguard(l.With("proxy_index", i), &conf, tunDev, opts.FwMark, t)
			if werr != nil {
				continue
			}

			// Test wireguard connectivity
			werr = usermodeTunTest(initCtx, l, tnet, opts.TestURL)
			if werr != nil {
				continue
			}
			break
		}

		if werr != nil {
			return nil, nil, fmt.Errorf("failed to establish wireguard: %w", werr)
		}

		l.Info("wireguard tunnel established", "proxy_index", i, "endpoint", endpoint)
		return dev, tnet, nil
	}

	// Start workers
	var wg sync.WaitGroup
	for w := 0; w < concurrentInit; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := range jobs {
				dev, tnet, err := initProxy(job.index)
				results <- initResult{index: job.index, tnet: tnet, dev: dev, err: err}
			}
		}()
	}

	// Send jobs
	for i := 0; i < numProxies; i++ {
		jobs <- initJob{index: i}
	}
	close(jobs)

	// Wait for workers in background to close results channel
	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect results
	successCount := 0
	failCount := 0
	devs := make([]*device.Device, numProxies)

	for res := range results {
		if res.err != nil {
			failCount++
			l.Error("failed to initialize proxy", "index", res.index, "error", res.err)
			if !config.ContinueOnError {
				return fmt.Errorf("proxy initialization failed at index %d: %w", res.index, res.err)
			}
		} else {
			successCount++
			tnets[res.index] = res.tnet
			devs[res.index] = res.dev
		}

		// Log progress every 10 completed or when done
		total := successCount + failCount
		if total%10 == 0 || total == numProxies {
			l.Info("initialization progress",
				"completed", total,
				"total", numProxies,
				"success", successCount,
				"failed", failCount)
		}
	}

	if successCount == 0 {
		return errors.New("all proxies failed to initialize")
	}

	// Start the proxy pool
	pool, err := wiresocks.StartProxyPool(ctx, l, config, tnets, devs)
	if err != nil {
		return fmt.Errorf("failed to start proxy pool: %w", err)
	}

	// Initialize Lifecycle Manager
	rebuildFunc := func(index int) error {
		l.Info("rebuilding proxy", "index", index)

		// 1. Initialize new network stack
		dev, tnet, err := initProxy(index)
		if err != nil {
			return err
		}

		// 2. Update pool with new stack
		// We need to remove the old proxy instance to ensure its listener is closed
		// and resources are released BEFORE we try to bind to the same port.

		// Remove old proxy (this calls the cleanup function we set in StartProxy)
		oldProxyID := fmt.Sprintf("proxy-%d", index)
		if err := pool.RemoveProxy(oldProxyID); err != nil {
			l.Warn("failed to remove old proxy during rebuild (might not exist)", "id", oldProxyID, "error", err)
		}

		proxyConf := config.Proxies[index]
		bindAddr, err := netip.ParseAddrPort(proxyConf.Bind)
		if err != nil {
			return err
		}

		// Generate ID same as StartProxyPool does
		id := fmt.Sprintf("proxy-%d", index)

		// Start new proxy listener
		// We don't need to run this in a goroutine anymore because we've already cleaned up the old one
		// and we want to ensure it starts successfully before returning.

		// Wait a tiny bit to ensure OS releases the port (though Close() should be enough)
		time.Sleep(10 * time.Millisecond)

		_, cleanup, err := wiresocks.StartProxy(ctx, l, tnet, dev, bindAddr)
		if err != nil {
			l.Error("failed to start proxy listener during rebuild", "error", err)
			return err
		}

		// Create new instance with the cleanup function
		newInstance := wiresocks.NewProxyInstance(
			id,
			index,
			bindAddr,
			tnet,
			proxyConf.GetWeight(),
			proxyConf.GetMaxConnections(config.MaxConnectionsPerProxy),
			cleanup,
		)

		if err := pool.AddProxy(newInstance); err != nil {
			cleanup() // Cleanup if adding to pool fails
			return err
		}

		return nil
	}

	lifecycleMgr := wiresocks.NewLifecycleManager(
		ctx,
		pool,
		l,
		rebuildFunc,
		config.GetProxyLifetime(),
		config.GetRebuildDelay(),
		config.GetRebuildConcurrency(),
	)
	lifecycleMgr.Start()

	// Schedule rebuild for failed proxies
	for i, tnet := range tnets {
		if tnet == nil {
			lifecycleMgr.ScheduleRebuild(i)
		}
	}

	// Log pool statistics periodically
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				lifecycleMgr.Stop()
				return
			case <-ticker.C:
				stats := pool.GetStats()
				l.Info("proxy pool stats",
					"total_proxies", stats.TotalProxies,
					"healthy_proxies", stats.HealthyProxies,
					"active_connections", stats.ActiveConns,
					"total_connections", stats.TotalConns,
					"total_errors", stats.TotalErrors,
					"error_rate", fmt.Sprintf("%.2f%%", stats.ErrorRate()*100),
				)
			}
		}
	}()

	l.Info("proxy pool is ready")
	return nil
}
