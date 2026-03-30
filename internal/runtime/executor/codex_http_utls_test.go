package executor

import (
	"context"
	"crypto/tls"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

func TestNewCodexHTTPClientUsesUTLSTransport(t *testing.T) {
	t.Parallel()

	client := newCodexHTTPClient(context.Background(), &config.Config{}, nil, 0)
	transport, err := codexHTTPTransportFromClient(client)
	if err != nil {
		t.Fatalf("codexHTTPTransportFromClient() error = %v", err)
	}
	if transport.Proxy != nil {
		t.Fatal("expected Codex HTTP transport to disable standard proxy handling")
	}
	if transport.DialContext == nil {
		t.Fatal("expected Codex HTTP transport DialContext to be set")
	}
	if transport.DialTLSContext == nil {
		t.Fatal("expected Codex HTTP transport DialTLSContext to be set")
	}
	if transport.ForceAttemptHTTP2 {
		t.Fatal("expected Codex HTTP transport to disable HTTP/2")
	}
	if transport.TLSNextProto == nil {
		t.Fatal("expected Codex HTTP transport to clear TLSNextProto")
	}
	if transport.TLSClientConfig == nil {
		t.Fatal("expected Codex HTTP transport TLSClientConfig")
	}
	if got := transport.TLSClientConfig.NextProtos; len(got) != 1 || got[0] != "http/1.1" {
		t.Fatalf("NextProtos = %v, want [http/1.1]", got)
	}
	if got := transport.TLSClientConfig.MinVersion; got != tls.VersionTLS12 {
		t.Fatalf("MinVersion = %d, want %d", got, tls.VersionTLS12)
	}
	if got := transport.TLSClientConfig.MaxVersion; got != tls.VersionTLS12 {
		t.Fatalf("MaxVersion = %d, want %d", got, tls.VersionTLS12)
	}
}

func TestCodexProxyModeLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  *config.Config
		auth *cliproxyauth.Auth
		want string
	}{
		{
			name: "inherit by default",
			cfg:  &config.Config{},
			want: "inherit",
		},
		{
			name: "direct auth overrides global proxy",
			cfg:  &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://global-proxy.example.com:8080"}},
			auth: &cliproxyauth.Auth{ProxyURL: "direct"},
			want: "direct",
		},
		{
			name: "socks5 proxy from auth",
			auth: &cliproxyauth.Auth{ProxyURL: "socks5://127.0.0.1:1080"},
			want: "socks5",
		},
		{
			name: "http proxy from config",
			cfg:  &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "http://127.0.0.1:8080"}},
			want: "http",
		},
		{
			name: "https proxy from config",
			cfg:  &config.Config{SDKConfig: sdkconfig.SDKConfig{ProxyURL: "https://127.0.0.1:8443"}},
			want: "https",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := codexProxyModeLabel(tc.cfg, tc.auth); got != tc.want {
				t.Fatalf("codexProxyModeLabel() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNewProxyAwareWebsocketDialerUsesCodexHelpers(t *testing.T) {
	t.Parallel()

	dialer := newProxyAwareWebsocketDialer(&config.Config{}, nil)
	if dialer.Proxy != nil {
		t.Fatal("expected websocket dialer proxy function to be nil")
	}
	if dialer.NetDialContext == nil {
		t.Fatal("expected websocket dialer NetDialContext")
	}
	if dialer.NetDialTLSContext == nil {
		t.Fatal("expected websocket dialer NetDialTLSContext")
	}
}
