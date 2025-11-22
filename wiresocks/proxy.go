package wiresocks

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/sagernet/sing/common/buf"

	"github.com/bepass-org/warp-plus/proxy/pkg/mixed"
	"github.com/bepass-org/warp-plus/proxy/pkg/statute"
	"github.com/bepass-org/warp-plus/wireguard/device"
	"github.com/bepass-org/warp-plus/wireguard/tun/netstack"
)

// VirtualTun stores a reference to netstack network and DNS configuration
type VirtualTun struct {
	Tnet      *netstack.Net
	Logger    *slog.Logger
	Dev       *device.Device
	Ctx       context.Context
	pool      buf.Allocator
	ProxyPool *ProxyPool // Reference to proxy pool if enabled
	//pool bufferpool.BufPool
}

var BuffSize = 65536

// StartProxy spawns a socks5 server.
func StartProxy(ctx context.Context, l *slog.Logger, tnet *netstack.Net, bindAddress netip.AddrPort) (netip.AddrPort, error) {
	ln, err := net.Listen("tcp", bindAddress.String())
	if err != nil {
		return netip.AddrPort{}, err // Return error if binding was unsuccessful
	}

	vt := VirtualTun{
		Tnet:   tnet,
		Logger: l.With("subsystem", "vtun"),
		Dev:    nil,
		Ctx:    ctx,
		pool:   buf.DefaultAllocator,
	}

	proxy := mixed.NewProxy(
		mixed.WithListener(ln),
		mixed.WithLogger(l),
		mixed.WithContext(ctx),
		mixed.WithUserHandler(func(request *statute.ProxyRequest) error {
			return vt.generalHandler(request)
		}),
	)
	go func() {
		_ = proxy.ListenAndServe()
	}()
	go func() {
		<-vt.Ctx.Done()
		vt.Stop()
	}()

	return ln.Addr().(*net.TCPAddr).AddrPort(), nil
}

// StartProxyPool starts a proxy pool with multiple SOCKS5 servers
func StartProxyPool(ctx context.Context, l *slog.Logger, config *ProxyPoolConfig, tnets []*netstack.Net) (*ProxyPool, error) {
	if !config.Enabled {
		return nil, nil
	}

	if len(config.Proxies) == 0 {
		return nil, errors.New("no proxies configured in pool")
	}

	if len(tnets) != len(config.Proxies) {
		return nil, errors.New("number of network stacks must match number of proxies")
	}

	// Create load balancer
	balancer, err := NewLoadBalancer(config.Strategy)
	if err != nil {
		return nil, err
	}

	// Create proxy pool
	pool := NewProxyPool(ctx, l, balancer)

	// Add proxies to pool
	for i, proxyConf := range config.Proxies {
		bind, err := netip.ParseAddrPort(proxyConf.Bind)
		if err != nil {
			return nil, err
		}

		proxyID := fmt.Sprintf("proxy-%d", i)
		maxConns := proxyConf.GetMaxConnections(config.MaxConnectionsPerProxy)
		weight := proxyConf.GetWeight()

		instance := NewProxyInstance(proxyID, i, bind, tnets[i], weight, maxConns)
		if err := pool.AddProxy(instance); err != nil {
			return nil, err
		}

		// Start SOCKS5 server for this proxy instance
		ln, err := net.Listen("tcp", bind.String())
		if err != nil {
			return nil, err
		}

		vt := VirtualTun{
			Tnet:      tnets[i],
			Logger:    l.With("subsystem", "vtun", "proxy_id", proxyID),
			Dev:       nil,
			Ctx:       ctx,
			pool:      buf.DefaultAllocator,
			ProxyPool: pool,
		}

		proxy := mixed.NewProxy(
			mixed.WithListener(ln),
			mixed.WithLogger(l.With("proxy_id", proxyID)),
			mixed.WithContext(ctx),
			mixed.WithUserHandler(func(request *statute.ProxyRequest) error {
				return vt.generalHandlerWithPool(request, instance)
			}),
		)

		go func(proxyID string) {
			if err := proxy.ListenAndServe(); err != nil {
				l.Error("proxy server error", "proxy_id", proxyID, "error", err)
			}
		}(proxyID)

		go func() {
			<-ctx.Done()
			vt.Stop()
		}()

		l.Info("proxy instance started", "proxy_id", proxyID, "bind", bind.String())
	}

	// Start health checker if enabled
	if config.Enabled {
		healthCheck := NewHealthChecker(
			ctx,
			pool,
			l,
			config.GetHealthCheckInterval(),
			config.GetHealthCheckTimeout(),
			config.AutoRecover,
		)
		pool.SetHealthChecker(healthCheck)
		healthCheck.Start()
	}

	l.Info("proxy pool started",
		"proxy_count", len(config.Proxies),
		"strategy", config.Strategy,
		"health_check_interval", config.HealthCheckInterval)

	return pool, nil
}

func (vt *VirtualTun) generalHandler(req *statute.ProxyRequest) error {
	vt.Logger.Debug("handling connection", "protocol", req.Network, "destination", req.Destination)
	if vt.Tnet == nil {
		return errors.New("proxy network stack is not initialized")
	}
	conn, err := vt.Tnet.Dial(req.Network, req.Destination)
	if err != nil {
		return err
	}

	timeout := 0 * time.Second
	switch req.Network {
	case "udp", "udp4", "udp6":
		timeout = 15 * time.Second
	}

	// Close the connections when this function exits
	defer conn.Close()
	defer req.Conn.Close()
	// Channel to notify when copy operation is done
	done := make(chan error, 1)
	// Copy data from req.Conn to conn
	go func() {
		buf1 := vt.pool.Get(BuffSize)
		defer func(pool buf.Allocator, buf []byte) {
			_ = pool.Put(buf)
		}(vt.pool, buf1)
		_, err := copyConnTimeout(conn, req.Conn, buf1, timeout)
		if errors.Is(err, syscall.ECONNRESET) {
			done <- nil
			return
		}
		done <- err
	}()
	// Copy data from conn to req.Conn
	go func() {
		buf2 := vt.pool.Get(BuffSize)
		defer func(pool buf.Allocator, buf []byte) {
			_ = pool.Put(buf)
		}(vt.pool, buf2)
		_, err := copyConnTimeout(req.Conn, conn, buf2, timeout)
		done <- err
	}()
	// Wait for one of the copy operations to finish
	err = <-done
	if err != nil {
		vt.Logger.Warn(err.Error())
	}

	// Close connections and wait for the other copy operation to finish
	<-done
	return nil
}

// generalHandlerWithPool handles connections using a specific proxy instance in the pool
func (vt *VirtualTun) generalHandlerWithPool(req *statute.ProxyRequest, instance *ProxyInstance) error {
	vt.Logger.Debug("handling connection", "protocol", req.Network, "destination", req.Destination)

	// Increment active connections for this proxy
	instance.IncrementActiveConns()
	defer instance.DecrementActiveConns()

	if vt.Tnet == nil {
		instance.IncrementErrors()
		return errors.New("proxy network stack is not initialized")
	}

	conn, err := vt.Tnet.Dial(req.Network, req.Destination)
	if err != nil {
		instance.IncrementErrors()

		// Mark proxy as unhealthy on connection errors
		if vt.ProxyPool != nil && vt.ProxyPool.healthCheck != nil {
			_ = vt.ProxyPool.healthCheck.MarkProxyUnhealthy(instance.ID, err)
		}

		return err
	}

	timeout := 0 * time.Second
	switch req.Network {
	case "udp", "udp4", "udp6":
		timeout = 15 * time.Second
	}

	// Close the connections when this function exits
	defer conn.Close()
	defer req.Conn.Close()
	// Channel to notify when copy operation is done
	done := make(chan error, 1)
	// Copy data from req.Conn to conn
	go func() {
		buf1 := vt.pool.Get(BuffSize)
		defer func(pool buf.Allocator, buf []byte) {
			_ = pool.Put(buf)
		}(vt.pool, buf1)
		_, err := copyConnTimeout(conn, req.Conn, buf1, timeout)
		if errors.Is(err, syscall.ECONNRESET) {
			done <- nil
			return
		}
		if err != nil {
			instance.IncrementErrors()
		}
		done <- err
	}()
	// Copy data from conn to req.Conn
	go func() {
		buf2 := vt.pool.Get(BuffSize)
		defer func(pool buf.Allocator, buf []byte) {
			_ = pool.Put(buf)
		}(vt.pool, buf2)
		_, err := copyConnTimeout(req.Conn, conn, buf2, timeout)
		if err != nil {
			instance.IncrementErrors()
		}
		done <- err
	}()
	// Wait for one of the copy operations to finish
	err = <-done
	if err != nil {
		vt.Logger.Warn(err.Error())
	}

	// Close connections and wait for the other copy operation to finish
	<-done
	return nil
}

func (vt *VirtualTun) Stop() {
	if vt.Dev != nil {
		if err := vt.Dev.Down(); err != nil {
			vt.Logger.Warn(err.Error())
		}
	}
}

var errInvalidWrite = errors.New("invalid write result")

func copyConnTimeout(dst net.Conn, src net.Conn, buf []byte, timeout time.Duration) (written int64, err error) {
	if buf != nil && len(buf) == 0 {
		panic("empty buffer in CopyBuffer")
	}

	for {
		deadline := time.Time{}
		if timeout != 0 {
			deadline = time.Now().Add(timeout)
		}
		if err := src.SetReadDeadline(deadline); err != nil {
			return 0, err
		}

		nr, er := src.Read(buf)
		if nr > 0 {
			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errInvalidWrite
				}
			}
			written += int64(nw)
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = io.ErrShortWrite
				break
			}
		}
		if er != nil {
			if er != io.EOF {
				err = er
			}
			break
		}
	}
	return written, err
}
