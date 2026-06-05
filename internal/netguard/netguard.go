package netguard

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"

	"github.com/ehmo/gomoufox/internal/policy"
)

var ErrBlocked = errors.New("url blocked by guardrail")

type Resolver interface {
	LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error)
}

type DefaultResolver struct{}

func (DefaultResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	return net.DefaultResolver.LookupIPAddr(ctx, host)
}

type Validator struct {
	Config   policy.Config
	Resolver Resolver
}

type Decision struct {
	URL        *url.URL
	Origin     string
	Host       string
	Port       string
	Resolved   []netip.Addr
	PinnedIP   netip.Addr
	WasProxied bool
}

func NewValidator(cfg policy.Config, resolver Resolver) Validator {
	if resolver == nil {
		resolver = DefaultResolver{}
	}
	if len(cfg.AllowedSchemes) == 0 {
		cfg.AllowedSchemes = []string{"http", "https"}
	}
	return Validator{Config: cfg, Resolver: resolver}
}

func (v Validator) Validate(ctx context.Context, rawURL string) (Decision, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return Decision{}, fmt.Errorf("%w: invalid URL", ErrBlocked)
	}
	if parsed.Scheme == "" || parsed.Host == "" {
		return Decision{}, fmt.Errorf("%w: URL must include scheme and host", ErrBlocked)
	}
	scheme := strings.ToLower(parsed.Scheme)
	if !containsFold(v.Config.AllowedSchemes, scheme) {
		return Decision{}, fmt.Errorf("%w: scheme %q is not allowed", ErrBlocked, parsed.Scheme)
	}
	if parsed.User != nil {
		return Decision{}, fmt.Errorf("%w: URL userinfo is not allowed", ErrBlocked)
	}
	host := canonicalHost(parsed.Hostname())
	if host == "" {
		return Decision{}, fmt.Errorf("%w: empty host", ErrBlocked)
	}
	if isIMDSHost(host) && !v.Config.AllowPrivateIPs {
		return Decision{}, fmt.Errorf("%w: metadata host %q is blocked", ErrBlocked, host)
	}
	port := parsed.Port()
	if port == "" {
		port = defaultPort(scheme)
	}
	origin := scheme + "://" + host
	if port != "" && port != defaultPort(scheme) {
		origin += ":" + port
	}
	if !v.originAllowed(origin) || !v.hostAllowed(host) {
		return Decision{}, fmt.Errorf("%w: host/origin not allowed", ErrBlocked)
	}
	addrs, err := v.resolve(ctx, host)
	if err != nil {
		return Decision{}, err
	}
	if len(addrs) == 0 {
		return Decision{}, fmt.Errorf("%w: host did not resolve", ErrBlocked)
	}
	if !v.Config.AllowPrivateIPs {
		for _, addr := range addrs {
			if IsBlockedAddr(addr) {
				return Decision{}, fmt.Errorf("%w: resolved address %s is blocked", ErrBlocked, addr)
			}
		}
	}
	return Decision{
		URL:      parsed,
		Origin:   origin,
		Host:     host,
		Port:     port,
		Resolved: addrs,
		PinnedIP: addrs[0],
	}, nil
}

func (v Validator) ValidateProxy(rawURL string) (*url.URL, error) {
	if rawURL == "" {
		return nil, nil
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return nil, fmt.Errorf("invalid proxy URL: %w", err)
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("unsupported proxy scheme %q", parsed.Scheme)
	}
	if parsed.Hostname() == "" {
		return nil, errors.New("proxy URL must include host")
	}
	return parsed, nil
}

func (v Validator) resolve(ctx context.Context, host string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{literal.Unmap()}, nil
	}
	addrs, err := v.Resolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: DNS resolution failed for %q: %v", ErrBlocked, host, err)
	}
	out := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		parsed, ok := netip.AddrFromSlice(addr.IP)
		if !ok {
			continue
		}
		out = append(out, parsed.Unmap())
	}
	return out, nil
}

func (v Validator) originAllowed(origin string) bool {
	if len(v.Config.AllowedOrigins) == 0 {
		return true
	}
	for _, allowed := range v.Config.AllowedOrigins {
		if normalizeOrigin(allowed) == origin {
			return true
		}
	}
	return false
}

func (v Validator) hostAllowed(host string) bool {
	if len(v.Config.AllowedHosts) == 0 {
		return true
	}
	for _, allowed := range v.Config.AllowedHosts {
		allowed = canonicalHost(strings.TrimSpace(allowed))
		if strings.HasPrefix(allowed, ".") {
			suffix := strings.TrimPrefix(allowed, ".")
			if host != suffix && strings.HasSuffix(host, "."+suffix) {
				return true
			}
			continue
		}
		if host == allowed {
			return true
		}
	}
	return false
}

func IsBlockedAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	if addr == netip.MustParseAddr("169.254.169.254") {
		return true
	}
	if addr.Is6() && strings.HasPrefix(addr.String(), "fd") {
		return true
	}
	if addr.IsLoopback() || addr.IsPrivate() || addr.IsLinkLocalUnicast() {
		return true
	}
	return false
}

func canonicalHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	return host
}

func isIMDSHost(host string) bool {
	return host == "metadata.google.internal" || host == "169.254.169.254"
}

func containsFold(values []string, value string) bool {
	for _, item := range values {
		if strings.EqualFold(strings.TrimSpace(item), value) {
			return true
		}
	}
	return false
}

func defaultPort(scheme string) string {
	switch scheme {
	case "http":
		return "80"
	case "https":
		return "443"
	default:
		return ""
	}
}

func normalizeOrigin(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return ""
	}
	scheme := strings.ToLower(parsed.Scheme)
	host := canonicalHost(parsed.Hostname())
	port := parsed.Port()
	if port == "" {
		port = defaultPort(scheme)
	}
	if port != "" && port != defaultPort(scheme) {
		return scheme + "://" + host + ":" + port
	}
	return scheme + "://" + host
}

func JoinHostPort(host, port string) string {
	if port == "" {
		return host
	}
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		return "[" + host + "]:" + port
	}
	if _, err := strconv.Atoi(port); err != nil {
		return host
	}
	return net.JoinHostPort(host, port)
}
