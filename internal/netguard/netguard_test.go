package netguard

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"strings"
	"testing"

	"github.com/ehmo/gomoufox/internal/policy"
)

type fakeResolver map[string][]string

func (f fakeResolver) LookupIPAddr(ctx context.Context, host string) ([]net.IPAddr, error) {
	values := f[host]
	out := make([]net.IPAddr, 0, len(values))
	for _, value := range values {
		out = append(out, net.IPAddr{IP: net.ParseIP(value)})
	}
	return out, nil
}

func TestValidateBlocksSchemesPrivateAndIMDS(t *testing.T) {
	v := NewValidator(policy.DefaultConfig(), fakeResolver{
		"example.com":              {"93.184.216.34"},
		"private.example":          {"10.0.0.1"},
		"metadata.google.internal": {"169.254.169.254"},
	})
	if _, err := v.Validate(context.Background(), "https://example.com/path"); err != nil {
		t.Fatalf("expected public URL allowed: %v", err)
	}
	blocked := []string{
		"file:///etc/passwd",
		"http://private.example/",
		"http://metadata.google.internal/",
		"http://127.0.0.1:8080/",
		"http://[::1]/",
	}
	for _, raw := range blocked {
		if _, err := v.Validate(context.Background(), raw); err == nil {
			t.Fatalf("expected %s blocked", raw)
		}
	}
}

func TestValidateDefaultsAndRejectsDisallowedSchemeAndEmptyHost(t *testing.T) {
	v := NewValidator(policy.Config{}, fakeResolver{"example.com": {"93.184.216.34"}})
	if len(v.Config.AllowedSchemes) != 2 || v.Config.AllowedSchemes[0] != "http" || v.Config.AllowedSchemes[1] != "https" {
		t.Fatalf("default schemes = %#v", v.Config.AllowedSchemes)
	}
	if _, err := v.Validate(context.Background(), "http://example.com/path"); err != nil {
		t.Fatalf("default scheme validate err = %v", err)
	}
	if _, err := v.Validate(context.Background(), "ftp://example.com/path"); !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), "scheme") {
		t.Fatalf("ftp err = %v", err)
	}
	if _, err := v.Validate(context.Background(), "http://:80/path"); !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), "empty host") {
		t.Fatalf("empty host err = %v", err)
	}
}

func TestValidateAllowPrivateIPsIsExplicit(t *testing.T) {
	cfg := policy.DefaultConfig()
	cfg.AllowPrivateIPs = true
	v := NewValidator(cfg, fakeResolver{"private.example": {"10.0.0.1"}})
	if _, err := v.Validate(context.Background(), "http://private.example/"); err != nil {
		t.Fatalf("expected explicit private override: %v", err)
	}
}

func TestValidateRejectsMalformedAndUserinfoAndDNSErrors(t *testing.T) {
	v := NewValidator(policy.DefaultConfig(), fakeResolver{
		"empty.example": {"not-an-ip"},
	})
	blocked := []string{
		"://bad",
		"https://",
		"https://user:pass@example.com",
		"http://empty.example",
	}
	for _, raw := range blocked {
		if _, err := v.Validate(context.Background(), raw); !errors.Is(err, ErrBlocked) {
			t.Fatalf("%s err = %v", raw, err)
		}
	}
	v = NewValidator(policy.DefaultConfig(), errorResolver{})
	if _, err := v.Validate(context.Background(), "https://example.com"); !errors.Is(err, ErrBlocked) || !strings.Contains(err.Error(), "DNS resolution failed") {
		t.Fatalf("dns err = %v", err)
	}
}

func TestOriginAndHostAllowlistsAreCanonical(t *testing.T) {
	cfg := policy.DefaultConfig()
	cfg.AllowedOrigins = []string{"https://example.com"}
	cfg.AllowedHosts = []string{".example.com"}
	v := NewValidator(cfg, fakeResolver{
		"api.example.com":      {"93.184.216.34"},
		"example.com.evil":     {"93.184.216.34"},
		"example.com":          {"93.184.216.34"},
		"other.example.com":    {"93.184.216.34"},
		"api.example.com.evil": {"93.184.216.34"},
	})
	if _, err := v.Validate(context.Background(), "https://api.example.com/path"); err == nil {
		t.Fatalf("origin allowlist should reject api.example.com when only example.com origin is allowed")
	}
	cfg.AllowedOrigins = nil
	v = NewValidator(cfg, v.Resolver)
	if _, err := v.Validate(context.Background(), "https://api.example.com/path?q=https://example.com"); err != nil {
		t.Fatalf("expected suffix host allowed: %v", err)
	}
	if _, err := v.Validate(context.Background(), "https://example.com.evil/"); err == nil {
		t.Fatalf("expected suffix bypass blocked")
	}

	cfg.AllowedHosts = []string{"example.com"}
	v = NewValidator(cfg, fakeResolver{"example.com": {"93.184.216.34"}, "api.example.com": {"93.184.216.34"}})
	if _, err := v.Validate(context.Background(), "https://example.com/"); err != nil {
		t.Fatalf("expected exact host allowed: %v", err)
	}
	if _, err := v.Validate(context.Background(), "https://api.example.com/"); err == nil {
		t.Fatalf("expected exact host allowlist to reject subdomain")
	}

	cfg.AllowedHosts = nil
	cfg.AllowedOrigins = []string{"https://example.com:444"}
	v = NewValidator(cfg, fakeResolver{"example.com": {"93.184.216.34"}})
	if decision, err := v.Validate(context.Background(), "https://example.com:444/path"); err != nil || decision.Origin != "https://example.com:444" {
		t.Fatalf("custom origin decision=%#v err=%v", decision, err)
	}
}

func TestIsBlockedAddr(t *testing.T) {
	blocked := []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.169.254", "169.254.1.1", "fd00::1", "fe80::1"}
	for _, value := range blocked {
		if !IsBlockedAddr(netip.MustParseAddr(value)) {
			t.Fatalf("expected %s blocked", value)
		}
	}
	if IsBlockedAddr(netip.MustParseAddr("93.184.216.34")) {
		t.Fatalf("expected public address allowed")
	}
}

func TestValidateProxy(t *testing.T) {
	v := NewValidator(policy.DefaultConfig(), nil)
	if parsed, err := v.ValidateProxy(""); err != nil || parsed != nil {
		t.Fatalf("empty proxy parsed=%v err=%v", parsed, err)
	}
	if _, err := v.ValidateProxy("socks5://user:pass@127.0.0.1:1080"); err != nil {
		t.Fatalf("operator proxy should validate: %v", err)
	}
	if _, err := v.ValidateProxy("http://[::1"); err == nil {
		t.Fatalf("expected invalid proxy URL")
	}
	if _, err := v.ValidateProxy("file:///tmp/proxy"); err == nil {
		t.Fatalf("expected unsupported proxy scheme")
	}
	if _, err := v.ValidateProxy("http://"); err == nil {
		t.Fatalf("expected proxy without host rejected")
	}
}

func TestDefaultResolverAndHostPortHelpers(t *testing.T) {
	if addrs, err := (DefaultResolver{}).LookupIPAddr(context.Background(), "localhost"); err != nil || len(addrs) == 0 {
		t.Fatalf("localhost lookup addrs=%v err=%v", addrs, err)
	}
	if JoinHostPort("example.com", "") != "example.com" {
		t.Fatalf("empty port join mismatch")
	}
	if JoinHostPort("2001:db8::1", "443") != "[2001:db8::1]:443" {
		t.Fatalf("ipv6 join mismatch")
	}
	if JoinHostPort("example.com", "not-a-port") != "example.com" {
		t.Fatalf("nonnumeric port join mismatch")
	}
	if normalizeOrigin("not a url") != "" || defaultPort("ws") != "" || containsFold([]string{" http "}, "https") {
		t.Fatalf("helper defaults mismatch")
	}
}

type errorResolver struct{}

func (errorResolver) LookupIPAddr(context.Context, string) ([]net.IPAddr, error) {
	return nil, errors.New("resolver failed")
}
