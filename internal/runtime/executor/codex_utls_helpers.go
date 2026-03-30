package executor

import (
	"bufio"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	utls "github.com/refraction-networking/utls"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v6/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
	"golang.org/x/net/proxy"
)

func resolveCodexProxySetting(cfg *config.Config, auth *cliproxyauth.Auth) (proxyutil.Setting, error) {
	proxyURL := ""
	if auth != nil {
		proxyURL = strings.TrimSpace(auth.ProxyURL)
	}
	if proxyURL == "" && cfg != nil {
		proxyURL = strings.TrimSpace(cfg.ProxyURL)
	}
	return proxyutil.Parse(proxyURL)
}

func codexProxyModeLabel(cfg *config.Config, auth *cliproxyauth.Auth) string {
	setting, err := resolveCodexProxySetting(cfg, auth)
	if err != nil {
		return "invalid"
	}
	switch setting.Mode {
	case proxyutil.ModeDirect:
		return "direct"
	case proxyutil.ModeProxy:
		if setting.URL != nil {
			return setting.URL.Scheme
		}
		return "proxy"
	default:
		return "inherit"
	}
}

func logCodexUTLSFingerprint(scope, serverName string) {
	scope = strings.TrimSpace(scope)
	if scope == "" {
		scope = "transport"
	}
	log.Infof("codex %s: using fixed uTLS fingerprint server=%s ja3=%s ja3_hash=%s ja4=%s", scope, strings.TrimSpace(serverName), codexResponsesWebsocketJA3FullString, codexResponsesWebsocketJA3Hash, codexResponsesWebsocketJA4)
}

func dialCodexTCP(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, network, addr string) (net.Conn, error) {
	baseDialer := &net.Dialer{
		Timeout:   30 * time.Second,
		KeepAlive: 30 * time.Second,
	}
	if ctx == nil {
		ctx = context.Background()
	}

	setting, errParse := resolveCodexProxySetting(cfg, auth)
	if errParse != nil {
		log.Errorf("codex transport: %v", errParse)
		return baseDialer.DialContext(ctx, network, addr)
	}

	switch setting.Mode {
	case proxyutil.ModeDirect, proxyutil.ModeInherit:
		return baseDialer.DialContext(ctx, network, addr)
	case proxyutil.ModeProxy:
	default:
		return baseDialer.DialContext(ctx, network, addr)
	}

	switch setting.URL.Scheme {
	case "socks5":
		var proxyAuth *proxy.Auth
		if setting.URL.User != nil {
			username := setting.URL.User.Username()
			password, _ := setting.URL.User.Password()
			proxyAuth = &proxy.Auth{User: username, Password: password}
		}
		socksDialer, errSOCKS5 := proxy.SOCKS5("tcp", setting.URL.Host, proxyAuth, proxy.Direct)
		if errSOCKS5 != nil {
			return nil, fmt.Errorf("codex transport: create SOCKS5 dialer failed: %w", errSOCKS5)
		}
		return dialCodexViaContext(ctx, socksDialer, network, addr)
	case "http", "https":
		return dialCodexHTTPTunnel(ctx, baseDialer, setting.URL, network, addr)
	default:
		return nil, fmt.Errorf("codex transport: unsupported proxy scheme: %s", setting.URL.Scheme)
	}
}

func dialCodexViaContext(ctx context.Context, dialer proxy.Dialer, network, addr string) (net.Conn, error) {
	if dialer == nil {
		return nil, fmt.Errorf("codex transport: proxy dialer is nil")
	}
	if contextDialer, ok := dialer.(proxy.ContextDialer); ok {
		return contextDialer.DialContext(ctx, network, addr)
	}
	type dialResult struct {
		conn net.Conn
		err  error
	}
	resultCh := make(chan dialResult, 1)
	go func() {
		conn, err := dialer.Dial(network, addr)
		resultCh <- dialResult{conn: conn, err: err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case result := <-resultCh:
		return result.conn, result.err
	}
}

func dialCodexHTTPTunnel(ctx context.Context, baseDialer *net.Dialer, proxyURL *url.URL, network, addr string) (net.Conn, error) {
	if proxyURL == nil {
		return nil, fmt.Errorf("codex transport: proxy URL is nil")
	}
	proxyAddr := proxyURL.Host
	if !strings.Contains(proxyAddr, ":") {
		if strings.EqualFold(proxyURL.Scheme, "https") {
			proxyAddr += ":443"
		} else {
			proxyAddr += ":80"
		}
	}

	conn, err := baseDialer.DialContext(ctx, network, proxyAddr)
	if err != nil {
		return nil, err
	}

	if strings.EqualFold(proxyURL.Scheme, "https") {
		logCodexUTLSFingerprint("proxy", proxyURL.Hostname())
		conn, err = wrapCodexUTLSConn(ctx, conn, proxyURL.Hostname(), true)
		if err != nil {
			return nil, err
		}
	}

	if err := sendCodexProxyConnect(ctx, conn, proxyURL, addr); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func sendCodexProxyConnect(ctx context.Context, conn net.Conn, proxyURL *url.URL, targetAddr string) error {
	if conn == nil {
		return fmt.Errorf("codex transport: proxy connection is nil")
	}
	if proxyURL == nil {
		return fmt.Errorf("codex transport: proxy URL is nil")
	}
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			_ = conn.SetDeadline(deadline)
			defer conn.SetDeadline(time.Time{})
		}
	}

	request := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Opaque: targetAddr},
		Host:   targetAddr,
		Header: make(http.Header),
	}
	request.Header.Set("Host", targetAddr)
	if proxyURL.User != nil {
		username := proxyURL.User.Username()
		password, _ := proxyURL.User.Password()
		auth := base64.StdEncoding.EncodeToString([]byte(username + ":" + password))
		request.Header.Set("Proxy-Authorization", "Basic "+auth)
	}

	if err := request.Write(conn); err != nil {
		return err
	}

	reader := bufio.NewReader(conn)
	resp, err := http.ReadResponse(reader, request)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		if len(body) == 0 {
			return fmt.Errorf("codex transport: proxy CONNECT failed: %s", resp.Status)
		}
		return fmt.Errorf("codex transport: proxy CONNECT failed: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if reader.Buffered() > 0 {
		return fmt.Errorf("codex transport: proxy CONNECT left buffered bytes")
	}
	return nil
}

func wrapCodexUTLSConn(ctx context.Context, rawConn net.Conn, serverName string, insecureSkipVerify bool) (net.Conn, error) {
	if rawConn == nil {
		return nil, fmt.Errorf("codex transport: raw connection is nil")
	}
	serverName = strings.TrimSpace(serverName)
	if serverName == "" {
		_ = rawConn.Close()
		return nil, fmt.Errorf("codex transport: server name is empty")
	}

	utls.EnableWeakCiphers()
	cfg := &utls.Config{
		ServerName:         serverName,
		InsecureSkipVerify: insecureSkipVerify,
		MinVersion:         utls.VersionTLS12,
		MaxVersion:         utls.VersionTLS12,
	}
	tlsConn := utls.UClient(rawConn, cfg, utls.HelloCustom)
	spec := buildCodexTLS12Spec()
	if err := tlsConn.ApplyPreset(&spec); err != nil {
		_ = rawConn.Close()
		return nil, err
	}
	tlsConn.HandshakeState.Hello.SessionId = nil
	if ctx != nil {
		if deadline, ok := ctx.Deadline(); ok {
			_ = tlsConn.SetDeadline(deadline)
			defer tlsConn.SetDeadline(time.Time{})
		}
	}
	if err := tlsConn.HandshakeContext(ctx); err != nil {
		_ = tlsConn.Close()
		return nil, err
	}
	if !insecureSkipVerify {
		if err := tlsConn.VerifyHostname(serverName); err != nil {
			_ = tlsConn.Close()
			return nil, err
		}
	}
	return tlsConn, nil
}

func buildCodexTLS12Spec() utls.ClientHelloSpec {
	return utls.ClientHelloSpec{
		TLSVersMin: utls.VersionTLS12,
		TLSVersMax: utls.VersionTLS12,
		CipherSuites: []uint16{
			utls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			utls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			utls.FAKE_TLS_DHE_RSA_WITH_AES_256_GCM_SHA384,
			utls.FAKE_TLS_DHE_RSA_WITH_AES_128_GCM_SHA256,
			utls.DISABLED_TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA384,
			utls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA256,
			utls.DISABLED_TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA384,
			utls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA256,
			utls.TLS_ECDHE_ECDSA_WITH_AES_256_CBC_SHA,
			utls.TLS_ECDHE_ECDSA_WITH_AES_128_CBC_SHA,
			utls.TLS_ECDHE_RSA_WITH_AES_256_CBC_SHA,
			utls.TLS_ECDHE_RSA_WITH_AES_128_CBC_SHA,
			utls.TLS_RSA_WITH_AES_256_GCM_SHA384,
			utls.TLS_RSA_WITH_AES_128_GCM_SHA256,
			utls.DISABLED_TLS_RSA_WITH_AES_256_CBC_SHA256,
			utls.TLS_RSA_WITH_AES_128_CBC_SHA256,
			utls.TLS_RSA_WITH_AES_256_CBC_SHA,
			utls.TLS_RSA_WITH_AES_128_CBC_SHA,
			utls.TLS_RSA_WITH_3DES_EDE_CBC_SHA,
		},
		CompressionMethods: []uint8{0},
		Extensions: []utls.TLSExtension{
			&utls.SNIExtension{},
			&utls.SupportedCurvesExtension{Curves: []utls.CurveID{
				utls.X25519,
				utls.CurveP256,
				utls.CurveP384,
			}},
			&utls.SupportedPointsExtension{SupportedPoints: []byte{0}},
			&utls.SignatureAlgorithmsExtension{SupportedSignatureAlgorithms: []utls.SignatureScheme{
				utls.PSSWithSHA256,
				utls.PSSWithSHA384,
				utls.PSSWithSHA512,
				utls.PKCS1WithSHA256,
				utls.PKCS1WithSHA384,
				utls.PKCS1WithSHA1,
				utls.ECDSAWithP256AndSHA256,
				utls.ECDSAWithP384AndSHA384,
				utls.ECDSAWithSHA1,
				utls.FakeSHA1WithDSA,
				utls.PKCS1WithSHA512,
				utls.ECDSAWithP521AndSHA512,
			}},
			&utls.SessionTicketExtension{},
			&utls.ExtendedMasterSecretExtension{},
			&utls.RenegotiationInfoExtension{Renegotiation: utls.RenegotiateOnceAsClient},
		},
	}
}

func hostNoPort(addr string) string {
	host, _, err := net.SplitHostPort(addr)
	if err == nil {
		return host
	}
	return strings.Trim(addr, "[]")
}
