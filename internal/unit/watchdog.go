package unit

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const defaultHTTPWatchdogStatus = 200

func (p *parser) finishWatchdog(spec *ServiceSpec, s *serviceBuilder) {
	mode := strings.ToLower(strings.TrimSpace(s.watchdogMode))
	switch {
	case mode == "":
		if spec.WatchdogSecSet && spec.WatchdogSec > 0 {
			spec.WatchdogMode = WatchdogModeNotify
		}
	case WatchdogMode(mode) == WatchdogModeNotify:
		spec.WatchdogMode = WatchdogModeNotify
	case WatchdogMode(mode) == WatchdogModeTCP:
		spec.WatchdogMode = WatchdogModeTCP
	case WatchdogMode(mode) == WatchdogModeHTTP:
		spec.WatchdogMode = WatchdogModeHTTP
	default:
		p.errorf(s.watchdogModeL, "invalid WatchdogMode %q (supported: notify, tcp, http)", s.watchdogMode)
	}

	ep := strings.TrimSpace(s.watchdogEP)
	if ep != "" {
		spec.WatchdogEndpoint = ep
		spec.WatchdogEndpointSet = true
	}

	switch spec.WatchdogMode {
	case WatchdogModeTCP, WatchdogModeHTTP:
		if !spec.WatchdogSecSet || spec.WatchdogSec <= 0 {
			line := s.watchdogModeL
			if line == 0 {
				line = s.watchdogSecL
			}
			p.errorf(line, "WatchdogMode=%s requires WatchdogSec", spec.WatchdogMode)
		}
		if ep == "" {
			line := s.watchdogEPL
			if line == 0 {
				line = s.watchdogModeL
			}
			p.errorf(line, "WatchdogMode=%s requires WatchdogEndpoint", spec.WatchdogMode)
		}
	case WatchdogModeNotify, "":
		if ep != "" {
			p.errorf(s.watchdogEPL, "WatchdogEndpoint is only valid with WatchdogMode=tcp or http")
		}
	}

	if spec.WatchdogMode == WatchdogModeTCP && ep != "" {
		addr, err := parseTCPWatchdogEndpoint(ep)
		if err != nil {
			p.errorf(s.watchdogEPL, "%s", err.Error())
		} else {
			spec.WatchdogAddr = addr
		}
	}
	if spec.WatchdogMode == WatchdogModeHTTP && ep != "" {
		u, err := parseHTTPWatchdogEndpoint(ep)
		if err != nil {
			p.errorf(s.watchdogEPL, "%s", err.Error())
		} else {
			spec.WatchdogURL = u.String()
			spec.WatchdogAddr = u.Host
		}
	}

	stat := strings.TrimSpace(s.watchdogStat)
	if stat != "" {
		if spec.WatchdogMode != WatchdogModeHTTP {
			p.errorf(s.watchdogStatL, "WatchdogExpectedStatus is only valid with WatchdogMode=http")
		} else {
			n, err := parseHTTPWatchdogStatus(stat)
			if err != nil {
				p.errorf(s.watchdogStatL, "invalid WatchdogExpectedStatus: %s", err.Error())
			} else {
				spec.WatchdogExpectedStatus = n
				spec.WatchdogExpectedStatusSet = true
			}
		}
	}
	if spec.WatchdogMode == WatchdogModeHTTP && !spec.WatchdogExpectedStatusSet {
		spec.WatchdogExpectedStatus = defaultHTTPWatchdogStatus
	}
}

func parseTCPWatchdogEndpoint(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", fmt.Errorf("invalid WatchdogEndpoint: empty host:port")
	}
	if strings.Contains(raw, "://") {
		return "", fmt.Errorf("WatchdogMode=tcp requires host:port, not a URL")
	}
	host, port, err := net.SplitHostPort(raw)
	if err != nil {
		return "", fmt.Errorf("invalid WatchdogEndpoint: %w", err)
	}
	host, err = canonicalizeLoopbackHost(host)
	if err != nil {
		return "", err
	}
	if err := validWatchdogPort(port); err != nil {
		return "", err
	}
	return net.JoinHostPort(host, port), nil
}

func parseHTTPWatchdogEndpoint(raw string) (*url.URL, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, fmt.Errorf("invalid WatchdogEndpoint: empty URL")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("WatchdogEndpoint must be an http or https URL")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("WatchdogEndpoint must be an http or https URL")
	}
	if u.Host == "" {
		return nil, fmt.Errorf("WatchdogEndpoint URL is missing a host")
	}
	if u.User != nil {
		return nil, fmt.Errorf("WatchdogEndpoint must not include userinfo")
	}
	host, err := canonicalizeLoopbackHost(u.Hostname())
	if err != nil {
		return nil, err
	}
	port := u.Port()
	if port == "" {
		if u.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	if err := validWatchdogPort(port); err != nil {
		return nil, err
	}
	u.Host = net.JoinHostPort(host, port)
	u.User = nil
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	return u, nil
}

func parseHTTPWatchdogStatus(raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return 0, fmt.Errorf("not an HTTP status code")
	}
	if n < 100 || n > 599 {
		return 0, fmt.Errorf("%d is not an HTTP status code", n)
	}
	return n, nil
}

func canonicalizeLoopbackHost(host string) (string, error) {
	host = strings.TrimSpace(host)
	if host == "" {
		return "", fmt.Errorf("WatchdogEndpoint host is empty")
	}
	if strings.EqualFold(host, "localhost") {
		// Do not resolve localhost (hosts file / DNS). 127.0.0.1 only.
		return "127.0.0.1", nil
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return "", fmt.Errorf("WatchdogEndpoint host %q is not a loopback address", host)
	}
	if !ip.IsLoopback() {
		return "", fmt.Errorf("WatchdogEndpoint host %q is not a loopback address", host)
	}
	return ip.String(), nil
}

func validWatchdogPort(port string) error {
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid WatchdogEndpoint port %q", port)
	}
	return nil
}

func requireLoopbackAddr(addr string) error {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("invalid WatchdogEndpoint: %w", err)
	}
	if _, err := canonicalizeLoopbackHost(host); err != nil {
		return err
	}
	return validWatchdogPort(port)
}

func requireLoopbackURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("WatchdogEndpoint must be an http or https URL")
	}
	host := u.Hostname()
	if host == "" {
		host = u.Host
	}
	if _, err := canonicalizeLoopbackHost(host); err != nil {
		return nil, err
	}
	return u, nil
}

func dialLoopback(ctx context.Context, network, address string) (net.Conn, error) {
	if err := requireLoopbackAddr(address); err != nil {
		return nil, err
	}
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	canon, err := canonicalizeLoopbackHost(host)
	if err != nil {
		return nil, err
	}
	ip := net.ParseIP(canon)
	if ip != nil {
		if ip.To4() != nil {
			network = "tcp4"
		} else {
			network = "tcp6"
		}
	}
	timeout := 2 * time.Second
	if dl, ok := ctx.Deadline(); ok {
		if r := time.Until(dl); r > 0 && r < timeout {
			timeout = r
		}
	}
	d := net.Dialer{Timeout: timeout}
	c, err := d.DialContext(ctx, network, net.JoinHostPort(canon, port))
	if err != nil {
		return nil, err
	}
	if ra, ok := c.RemoteAddr().(*net.TCPAddr); ok && (ra.IP == nil || !ra.IP.IsLoopback()) {
		_ = c.Close()
		return nil, fmt.Errorf("WatchdogEndpoint connected to non-loopback address %v", ra.IP)
	}
	return c, nil
}

// ProbeTCP is a connect-only watchdog probe. addr must be loopback.
func ProbeTCP(ctx context.Context, addr string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	c, err := dialLoopback(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	_ = c.Close()
	return nil
}

// ProbeHTTP GETs url and requires wantStatus. The URL host must be loopback.
func ProbeHTTP(ctx context.Context, rawURL string, wantStatus int) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if wantStatus <= 0 {
		wantStatus = defaultHTTPWatchdogStatus
	}
	u, err := requireLoopbackURL(rawURL)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return err
	}
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.Proxy = nil
	tr.DialContext = dialLoopback
	tr.DisableKeepAlives = true
	tr.ForceAttemptHTTP2 = false
	client := &http.Client{
		Transport: tr,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if _, err := io.Copy(io.Discard, io.LimitReader(resp.Body, 8<<10)); err != nil {
		return err
	}
	if resp.StatusCode != wantStatus {
		return fmt.Errorf("watchdog http status %d, want %d", resp.StatusCode, wantStatus)
	}
	return nil
}

// ProbeWatchdog runs one tcp or http probe. Notify mode is not probed here.
func (s *ServiceSpec) ProbeWatchdog(ctx context.Context) error {
	if s == nil {
		return fmt.Errorf("no service spec")
	}
	switch s.WatchdogMode {
	case WatchdogModeTCP:
		return ProbeTCP(ctx, s.WatchdogAddr)
	case WatchdogModeHTTP:
		return ProbeHTTP(ctx, s.WatchdogURL, s.WatchdogExpectedStatus)
	default:
		return fmt.Errorf("WatchdogMode %q is not a tcp/http probe", s.WatchdogMode)
	}
}

// WatchdogProbeTimeout is the per-probe deadline. It equals the watchdog
// interval so a hung localhost connect cannot outlast WatchdogSec=. A
// non-positive interval yields 1s.
func WatchdogProbeTimeout(interval time.Duration) time.Duration {
	if interval <= 0 {
		return time.Second
	}
	return interval
}
