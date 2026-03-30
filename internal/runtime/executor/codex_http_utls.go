package executor

import (
	"context"
	"crypto/tls"
	"fmt"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func newCodexHTTPClient(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, timeout time.Duration) *http.Client {
	_ = ctx
	client := &http.Client{Transport: newCodexUTLSRoundTripper(cfg, auth)}
	if timeout > 0 {
		client.Timeout = timeout
	}
	return client
}

func newCodexUTLSRoundTripper(cfg *config.Config, auth *cliproxyauth.Auth) http.RoundTripper {
	base, ok := http.DefaultTransport.(*http.Transport)
	if !ok || base == nil {
		base = &http.Transport{}
	}
	transport := base.Clone()
	transport.Proxy = nil
	transport.ForceAttemptHTTP2 = false
	transport.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	transport.TLSClientConfig.NextProtos = []string{"http/1.1"}
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	transport.TLSClientConfig.MaxVersion = tls.VersionTLS12
	transport.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return dialCodexTCP(ctx, cfg, auth, network, addr)
	}
	transport.DialTLSContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		rawConn, err := dialCodexTCP(ctx, cfg, auth, network, addr)
		if err != nil {
			return nil, err
		}
		serverName := hostNoPort(addr)
		logCodexUTLSFingerprint("http", serverName)
		conn, err := wrapCodexUTLSConn(ctx, rawConn, serverName, false)
		if err != nil {
			return nil, err
		}
		log.Debugf("codex http: established uTLS transport server=%s proxy=%s", strings.TrimSpace(serverName), codexProxyModeLabel(cfg, auth))
		return conn, nil
	}
	return transport
}

func codexHTTPTransportFromClient(client *http.Client) (*http.Transport, error) {
	if client == nil {
		return nil, fmt.Errorf("codex http client is nil")
	}
	transport, ok := client.Transport.(*http.Transport)
	if !ok || transport == nil {
		return nil, fmt.Errorf("codex http transport type = %T", client.Transport)
	}
	return transport, nil
}
