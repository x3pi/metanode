package proxy

import (
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"time"

	"github.com/meta-node-blockchain/meta-node/cmd/rpc-client/app"
)

type RpcReverseProxy struct {
	ReverseProxy         *httputil.ReverseProxy
	ReadonlyReverseProxy *httputil.ReverseProxy
	ReadonlyWSSServerURL string
	AppCtx               *app.Context
}

// New tạo RpcReverseProxy instance
func New(appCtx *app.Context) (*RpcReverseProxy, error) {
	cfg := appCtx.Cfg
	// Parse target URLs
	targetHTTPURL, err := url.Parse(cfg.RPCServerURL)
	if err != nil {
		return nil, fmt.Errorf("invalid RPC server URL '%s': %w", cfg.RPCServerURL, err)
	}

	// Create HTTP reverse proxy
	defaultProxy := httputil.NewSingleHostReverseProxy(targetHTTPURL)
	defaultProxy.Transport = createCustomTransport()

	// Create readonly proxy if configured
	var readonlyProxy *httputil.ReverseProxy
	if cfg.ReadonlyRPCServerURL != "" {
		readonlyTargetURL, err := url.Parse(cfg.ReadonlyRPCServerURL)
		if err != nil {
			return nil, fmt.Errorf("invalid readonly RPC server URL '%s': %w", cfg.ReadonlyRPCServerURL, err)
		}
		readonlyProxy = httputil.NewSingleHostReverseProxy(readonlyTargetURL)
		readonlyProxy.Transport = createCustomTransport()
	}

	proxy := &RpcReverseProxy{
		ReverseProxy:         defaultProxy,
		ReadonlyReverseProxy: readonlyProxy,
		ReadonlyWSSServerURL: cfg.ReadonlyWSSServerURL,
		AppCtx:               appCtx,
	}

	// Set error handlers
	proxy.ReverseProxy.ErrorHandler = proxy.errorHandler
	if proxy.ReadonlyReverseProxy != nil {
		proxy.ReadonlyReverseProxy.ErrorHandler = proxy.readonlyErrorHandler
	}

	return proxy, nil
}

// Close đóng các resources
func (p *RpcReverseProxy) Close() error {
	if p.AppCtx != nil {
		return p.AppCtx.Close()
	}
	return nil
}
func createCustomTransport() *http.Transport {
	return &http.Transport{
		// Connection pooling
		MaxIdleConns:        1000, // Tổng số kết nối idle tối đa
		MaxIdleConnsPerHost: 500,  // Số kết nối idle tối đa mỗi host
		MaxConnsPerHost:     0,    // Không giới hạn số kết nối mỗi host (0 = unlimited)

		// Timeouts
		IdleConnTimeout:       90 * time.Second,  // Đóng kết nối idle sau 90s
		TLSHandshakeTimeout:   60 * time.Second,  // Timeout TLS handshake
		ExpectContinueTimeout: 10 * time.Second,  // Timeout cho 100-continue
		ResponseHeaderTimeout: 240 * time.Second, // Timeout đợi response header

		// Performance
		DisableCompression: true,  // Tắt auto compression (proxy nên giữ nguyên)
		DisableKeepAlives:  false, // Bật HTTP keep-alive (reuse connections)
		ForceAttemptHTTP2:  false, // Không ép HTTP/2 (dùng HTTP/1.1)

		// TLS Config - Skip certificate verification
		TLSClientConfig: &tls.Config{
			InsecureSkipVerify: true, // Bỏ qua xác thực TLS certificate
		},

		// Dialer config
		DialContext: (&net.Dialer{
			Timeout:   120 * time.Second, // Timeout kết nối TCP
			KeepAlive: 120 * time.Second, // TCP keep-alive interval
		}).DialContext,
	}
}
