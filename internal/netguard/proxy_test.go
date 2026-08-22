package netguard

import (
	"bufio"
	"container/list"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ehmo/gomoufox/internal/policy"
)

func TestFilteringProxyBlocksBeforeDial(t *testing.T) {
	dialer := &recordingDialer{err: errors.New("should not dial")}
	proxy := FilteringProxy{
		Validator: NewValidator(policy.DefaultConfig(), fakeResolver{"private.example": {"10.0.0.1"}}),
		Dial:      dialer.DialContext,
	}
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://private.example/path", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	if reason, ok := ConsumeBlockedMarker(rr.Header().Get(BlockedHeader)); !ok || !strings.Contains(reason, "10.0.0.1") {
		t.Fatalf("blocked response headers = %v", rr.Header())
	}
	if len(dialer.addresses) != 0 {
		t.Fatalf("blocked request dialed: %#v", dialer.addresses)
	}
}

func TestFilteringProxyMarksBlockedConnectResponse(t *testing.T) {
	dialer := &recordingDialer{err: errors.New("should not dial")}
	proxy := FilteringProxy{
		Validator: NewValidator(policy.DefaultConfig(), fakeResolver{"private.example": {"10.0.0.1"}}),
		Dial:      dialer.DialContext,
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodConnect, "http://private.example:443", nil)
	req.Host = "private.example:443"
	proxy.ServeHTTP(rr, req)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
	if reason, ok := ConsumeBlockedMarker(rr.Header().Get(BlockedHeader)); !ok || !strings.Contains(reason, "10.0.0.1") {
		t.Fatalf("blocked response headers = %v", rr.Header())
	}
	if len(dialer.addresses) != 0 {
		t.Fatalf("blocked CONNECT dialed: %#v", dialer.addresses)
	}
}

func TestFilteringProxyRejectsForgedBlockMarker(t *testing.T) {
	if _, ok := ConsumeBlockedMarker("1"); ok {
		t.Fatal("untrusted marker was accepted")
	}
	if _, ok := ConsumeBlockedMarker(""); ok {
		t.Fatal("untrusted marker was accepted")
	}
	dst := http.Header{}
	copyHeader(dst, http.Header{
		BlockedHeader:  []string{"origin_cookie=preserved; HttpOnly"},
		"Content-Type": []string{"text/plain"},
	})
	if got := dst.Get(BlockedHeader); got != "origin_cookie=preserved; HttpOnly" {
		t.Fatalf("origin cookie = %q", got)
	}
	if got := dst.Get("Content-Type"); got != "text/plain" {
		t.Fatalf("ordinary header = %q", got)
	}
}

func TestFilteringProxyBlockMarkerUsesHTTPOnlyDeletionCookie(t *testing.T) {
	proxy := FilteringProxy{Validator: NewValidator(policy.DefaultConfig(), nil)}
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	marker := rr.Header().Get(BlockedHeader)
	for _, attribute := range []string{"Max-Age=0", "Expires=Thu, 01 Jan 1970 00:00:00 GMT", "Secure", "HttpOnly", "SameSite=Strict"} {
		if !strings.Contains(marker, attribute) {
			t.Fatalf("block marker missing %q: %q", attribute, marker)
		}
	}
}

func TestFilteringProxyRejectsReplayedBlockMarker(t *testing.T) {
	proxy := FilteringProxy{Validator: NewValidator(policy.DefaultConfig(), nil)}
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://127.0.0.1/", nil))
	marker := rr.Header().Get(BlockedHeader)
	if _, ok := ConsumeBlockedMarker(marker); !ok {
		t.Fatal("fresh marker was rejected")
	}
	if _, ok := ConsumeBlockedMarker(marker); ok {
		t.Fatal("replayed marker was accepted")
	}
}

func TestBlockedMarkerRegistryRejectsInvalidLengthAndPrunesExpired(t *testing.T) {
	if _, ok := ConsumeBlockedMarker(blockedCookieName + "_short=; Path=/"); ok {
		t.Fatal("short marker was accepted")
	}

	marker := strings.Repeat("a", blockedMarkerBytes*2)
	blockedMarkers.Lock()
	elem := blockedMarkers.order.PushFront(blockedMarkerEntry{
		value: marker, reason: "expired", created: time.Now().Add(-blockedMarkerTTL - time.Second),
	})
	blockedMarkers.entries[marker] = elem
	blockedMarkers.Unlock()
	if _, ok := ConsumeBlockedMarker(blockedCookieName + "_" + marker + "=; Path=/"); ok {
		t.Fatal("expired marker was accepted")
	}

	blockedMarkers.Lock()
	_, exists := blockedMarkers.entries[marker]
	listed := false
	for elem := blockedMarkers.order.Front(); elem != nil; elem = elem.Next() {
		if elem.Value.(blockedMarkerEntry).value == marker {
			listed = true
		}
	}
	blockedMarkers.Unlock()
	if exists || listed {
		t.Fatalf("expired marker remains in registry: map=%v list=%v", exists, listed)
	}
}

func TestBlockedMarkerGenerationFailureCollisionCapacityAndNilRemoval(t *testing.T) {
	oldRead := blockedMarkerRandomRead
	t.Cleanup(func() {
		blockedMarkerRandomRead = oldRead
		blockedMarkers.Lock()
		blockedMarkers.entries = make(map[string]*list.Element)
		blockedMarkers.order.Init()
		blockedMarkers.Unlock()
	})
	blockedMarkerRandomRead = func([]byte) (int, error) { return 0, errors.New("entropy failed") }
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("random read failure did not panic")
			}
		}()
		issueBlockedMarker("blocked")
	}()

	blockedMarkers.Lock()
	blockedMarkers.entries = make(map[string]*list.Element)
	blockedMarkers.order.Init()
	collision := strings.Repeat("00", blockedMarkerBytes)
	elem := blockedMarkers.order.PushBack(blockedMarkerEntry{value: collision, reason: "old", created: time.Now()})
	blockedMarkers.entries[collision] = elem
	blockedMarkers.Unlock()
	calls := 0
	blockedMarkerRandomRead = func(raw []byte) (int, error) {
		calls++
		if calls > 1 {
			for i := range raw {
				raw[i] = 1
			}
		}
		return len(raw), nil
	}
	if marker := issueBlockedMarker("new"); marker == collision || calls != 2 {
		t.Fatalf("marker=%q calls=%d", marker, calls)
	}

	blockedMarkers.Lock()
	blockedMarkers.entries = make(map[string]*list.Element)
	blockedMarkers.order.Init()
	for i := 0; i < blockedMarkerLimit; i++ {
		value := fmt.Sprintf("%032x", i)
		elem := blockedMarkers.order.PushBack(blockedMarkerEntry{value: value, reason: "full", created: time.Now()})
		blockedMarkers.entries[value] = elem
	}
	blockedMarkers.Unlock()
	blockedMarkerRandomRead = func(raw []byte) (int, error) {
		for i := range raw {
			raw[i] = 0xff
		}
		return len(raw), nil
	}
	replacement := issueBlockedMarker("replacement")
	blockedMarkers.Lock()
	_, inserted := blockedMarkers.entries[replacement]
	_, oldestRetained := blockedMarkers.entries[fmt.Sprintf("%032x", 0)]
	if len(blockedMarkers.entries) != blockedMarkerLimit || !inserted || oldestRetained {
		t.Fatalf("marker registry size=%d inserted=%v oldest_retained=%v", len(blockedMarkers.entries), inserted, oldestRetained)
	}
	blockedMarkers.Unlock()
	removeBlockedMarker(nil)
	blockedMarkers.Lock()
	remaining := len(blockedMarkers.entries)
	blockedMarkers.Unlock()
	if remaining != blockedMarkerLimit {
		t.Fatalf("nil removal changed registry size to %d", remaining)
	}
}

func TestFilteringProxyPinsAllowedHTTPDialTarget(t *testing.T) {
	dialer := &recordingDialer{conn: oneShotHTTPConn(t)}
	proxy := FilteringProxy{
		Validator: NewValidator(policy.DefaultConfig(), fakeResolver{"public.example": {"93.184.216.34"}}),
		Dial:      dialer.DialContext,
	}
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://public.example/path", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "ok" || rr.Header().Get("X-Test") != "yes" {
		t.Fatalf("response code=%d headers=%v body=%q", rr.Code, rr.Header(), rr.Body.String())
	}
	if got := strings.Join(dialer.addresses, ","); got != "93.184.216.34:80" {
		t.Fatalf("dial addresses = %s", got)
	}
}

func TestFilteringProxyAllowLocalhostDoesNotAllowRebinding(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("local ok"))
	}))
	defer local.Close()
	cfg := policy.DefaultConfig()
	cfg.AllowLocalhost = true
	dialer := &routingDialer{routes: map[string]string{"127.0.0.1:80": local.Listener.Addr().String()}}
	proxy := FilteringProxy{
		Validator: NewValidator(cfg, fakeResolver{
			"localhost":      {"127.0.0.1"},
			"rebind.example": {"127.0.0.1"},
		}),
		Dial: dialer.DialContext,
	}
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://localhost/path", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "local ok" {
		t.Fatalf("localhost response code=%d body=%q", rr.Code, rr.Body.String())
	}
	rr = httptest.NewRecorder()
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://rebind.example/path", nil))
	if rr.Code != http.StatusForbidden {
		t.Fatalf("rebind response code=%d body=%q", rr.Code, rr.Body.String())
	}
	if got := strings.Join(dialer.addresses, ","); got != "127.0.0.1:80" {
		t.Fatalf("dial addresses = %s", got)
	}
}

func TestFilteringProxyHTTPUpstreamAfterValidation(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		if r.Method != http.MethodGet || r.URL.String() != "http://public.example/path" {
			t.Fatalf("upstream request = %s %s", r.Method, r.URL.String())
		}
		w.Header().Set("X-Upstream", "yes")
		_, _ = w.Write([]byte("via upstream"))
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	proxy := FilteringProxy{
		Validator:     NewValidator(policy.DefaultConfig(), fakeResolver{"public.example": {"93.184.216.34"}, "private.example": {"10.0.0.1"}}),
		UpstreamProxy: upstreamURL,
	}
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://public.example/path", nil))
	if rr.Code != http.StatusOK || rr.Body.String() != "via upstream" || rr.Header().Get("X-Upstream") != "yes" {
		t.Fatalf("upstream response code=%d headers=%v body=%q", rr.Code, rr.Header(), rr.Body.String())
	}
	rr = httptest.NewRecorder()
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://private.example/path", nil))
	if rr.Code != http.StatusForbidden || upstreamCalls != 1 {
		t.Fatalf("blocked upstream code=%d calls=%d", rr.Code, upstreamCalls)
	}
}

func TestFilteringProxyAllowLocalhostStillBlocksUnsafeRedirects(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://10.0.0.1/landing", http.StatusFound)
	}))
	defer local.Close()
	cfg := policy.DefaultConfig()
	cfg.AllowLocalhost = true
	dialer := &routingDialer{routes: map[string]string{"127.0.0.1:80": local.Listener.Addr().String()}}
	proxy := FilteringProxy{
		Validator: NewValidator(cfg, fakeResolver{"localhost": {"127.0.0.1"}}),
		Dial:      dialer.DialContext,
	}
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	resp, err := client.Get("http://localhost/redirect-private")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("redirect final status = %d", resp.StatusCode)
	}
	if got := strings.Join(dialer.addresses, ","); got != "127.0.0.1:80" {
		t.Fatalf("redirect dials = %#v", dialer.addresses)
	}
}

func TestFilteringProxyBlocksBrowserInitiatedRequestsBeforeUpstreamProxy(t *testing.T) {
	var upstreamCalls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamCalls++
		http.Error(w, "blocked request reached upstream proxy", http.StatusBadGateway)
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	cfg := policy.DefaultConfig()
	cfg.AllowedHosts = []string{"allowed.example"}
	proxy := FilteringProxy{
		Validator: NewValidator(cfg, fakeResolver{
			"evil.example":             {"93.184.216.35"},
			"metadata.google.internal": {"169.254.169.254"},
		}),
		UpstreamProxy: upstreamURL,
	}
	targets := []string{
		"http://evil.example/app.js",
		"http://metadata.google.internal/latest/meta-data/",
		"http://169.254.169.254/latest/meta-data/",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, target, nil)
			req.Header.Set("Referer", "http://allowed.example/page")
			proxy.ServeHTTP(rr, req)
			if rr.Code != http.StatusForbidden {
				t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
			}
			if upstreamCalls != 0 {
				t.Fatalf("upstream calls = %d, want 0", upstreamCalls)
			}
		})
	}
}

func TestFilteringProxyBlocksBrowserInitiatedRequestsBeforeDial(t *testing.T) {
	cfg := policy.DefaultConfig()
	cfg.AllowedHosts = []string{"allowed.example"}
	dialer := &recordingDialer{err: errors.New("blocked request should not dial")}
	proxy := FilteringProxy{
		Validator: NewValidator(cfg, fakeResolver{
			"evil.example":             {"93.184.216.35"},
			"metadata.google.internal": {"169.254.169.254"},
		}),
		Dial: dialer.DialContext,
	}
	initiators := []struct {
		name   string
		method string
		path   string
	}{
		{name: "page fetch", method: http.MethodGet, path: "/api/data"},
		{name: "image subresource", method: http.MethodGet, path: "/pixel.png"},
		{name: "script subresource", method: http.MethodGet, path: "/app.js"},
		{name: "frame subresource", method: http.MethodGet, path: "/frame"},
		{name: "worker script", method: http.MethodGet, path: "/worker.js"},
		{name: "meta refresh", method: http.MethodGet, path: "/refresh-target"},
		{name: "form submit", method: http.MethodPost, path: "/submit"},
		{name: "click navigation", method: http.MethodGet, path: "/clicked"},
	}
	targets := []struct {
		name string
		base string
	}{
		{name: "disallowed public host", base: "http://evil.example"},
		{name: "metadata host", base: "http://metadata.google.internal"},
		{name: "private IP literal", base: "http://169.254.169.254"},
	}
	for _, initiator := range initiators {
		for _, target := range targets {
			t.Run(initiator.name+" to "+target.name, func(t *testing.T) {
				dialer.addresses = nil
				var body io.Reader
				if initiator.method == http.MethodPost {
					body = strings.NewReader("field=value")
				}
				req := httptest.NewRequest(initiator.method, target.base+initiator.path, body)
				req.Header.Set("Referer", "http://allowed.example/page")
				rr := httptest.NewRecorder()
				proxy.ServeHTTP(rr, req)
				if rr.Code != http.StatusForbidden {
					t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
				}
				if len(dialer.addresses) != 0 {
					t.Fatalf("blocked browser-initiated request dialed: %#v", dialer.addresses)
				}
			})
		}
	}
}

func TestFilteringProxyBlocksBrowserFollowedRedirectsBeforeBlockedDial(t *testing.T) {
	allowedServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/redirect-public":
			http.Redirect(w, r, "http://evil.example/landing", http.StatusFound)
		case "/redirect-metadata":
			http.Redirect(w, r, "http://metadata.google.internal/latest/meta-data/", http.StatusFound)
		case "/redirect-private-ip":
			http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
		default:
			t.Fatalf("unexpected allowed server path: %s", r.URL.Path)
		}
	}))
	defer allowedServer.Close()
	cfg := policy.DefaultConfig()
	cfg.AllowedHosts = []string{"allowed.example"}
	dialer := &routingDialer{routes: map[string]string{"93.184.216.34:80": allowedServer.Listener.Addr().String()}}
	proxy := FilteringProxy{
		Validator: NewValidator(cfg, fakeResolver{
			"allowed.example":          {"93.184.216.34"},
			"evil.example":             {"93.184.216.35"},
			"metadata.google.internal": {"169.254.169.254"},
		}),
		Dial: dialer.DialContext,
	}
	proxyServer := httptest.NewServer(proxy)
	defer proxyServer.Close()
	proxyURL, err := url.Parse(proxyServer.URL)
	if err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	for _, path := range []string{"/redirect-public", "/redirect-metadata", "/redirect-private-ip"} {
		t.Run(path, func(t *testing.T) {
			before := len(dialer.addresses)
			resp, err := client.Get("http://allowed.example" + path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = resp.Body.Close() }()
			if resp.StatusCode != http.StatusForbidden {
				t.Fatalf("redirect final status = %d", resp.StatusCode)
			}
			got := dialer.addresses[before:]
			if len(got) != 1 || got[0] != "93.184.216.34:80" {
				t.Fatalf("redirect dials = %#v, want only allowed first hop", got)
			}
		})
	}
}

func TestFilteringProxyHTTPDialFailure(t *testing.T) {
	dialer := &recordingDialer{err: errors.New("dial failed")}
	proxy := FilteringProxy{
		Validator: NewValidator(policy.DefaultConfig(), fakeResolver{"public.example": {"93.184.216.34"}}),
		Dial:      dialer.DialContext,
	}
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "http://public.example/path", nil))
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("code = %d body=%s", rr.Code, rr.Body.String())
	}
}

func TestFilteringProxyConnectValidationAndPinning(t *testing.T) {
	dialer := &recordingDialer{err: errors.New("dial failed")}
	proxy := FilteringProxy{
		Validator: NewValidator(policy.DefaultConfig(), fakeResolver{
			"public.example":  {"93.184.216.34"},
			"private.example": {"127.0.0.1"},
		}),
		Dial: dialer.DialContext,
	}
	blockedReq := httptest.NewRequest(http.MethodConnect, "http://private.example:443", nil)
	blockedReq.Host = "private.example:443"
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, blockedReq)
	if rr.Code != http.StatusForbidden || len(dialer.addresses) != 0 {
		t.Fatalf("blocked CONNECT code=%d addresses=%v", rr.Code, dialer.addresses)
	}

	allowedReq := httptest.NewRequest(http.MethodConnect, "http://public.example:443", nil)
	allowedReq.Host = "public.example:443"
	rr = httptest.NewRecorder()
	proxy.ServeHTTP(rr, allowedReq)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("allowed CONNECT with failing dial code=%d", rr.Code)
	}
	if got := strings.Join(dialer.addresses, ","); got != "93.184.216.34:443" {
		t.Fatalf("CONNECT dial addresses = %s", got)
	}
}

func TestFilteringProxyConnectHijackFailureClosesTarget(t *testing.T) {
	target := &scriptedConn{}
	proxy := FilteringProxy{
		Validator: NewValidator(policy.DefaultConfig(), fakeResolver{"public.example": {"93.184.216.34"}}),
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			return target, nil
		},
	}
	req := httptest.NewRequest(http.MethodConnect, "http://public.example:443", nil)
	req.Host = "public.example:443"
	proxy.ServeHTTP(&hijackErrorRecorder{header: http.Header{}}, req)
	if !target.closed {
		t.Fatalf("target connection was not closed")
	}
}

func TestFilteringProxyDialerRejectsInvalidPinnedIP(t *testing.T) {
	dialer := FilteringProxy{}.dialer(Decision{Port: "443"})
	conn, err := dialer(context.Background(), "tcp", "")
	if err == nil || !strings.Contains(err.Error(), "invalid pinned IP") {
		t.Fatalf("dialer err = %v", err)
	}
	if conn != nil {
		t.Fatalf("conn = %#v, want nil", conn)
	}
}

func TestFilteringProxyDialerPrefersIPv4AndFallsBackAcrossValidatedAddresses(t *testing.T) {
	client, server := net.Pipe()
	defer func() { _ = client.Close() }()
	defer func() { _ = server.Close() }()
	var addresses []string
	firstErr := errors.New("first failed")
	proxy := FilteringProxy{Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
		addresses = append(addresses, address)
		if len(addresses) == 1 {
			return nil, firstErr
		}
		return client, nil
	}}
	dialer := proxy.dialer(Decision{
		Port: "443",
		Resolved: []netip.Addr{
			netip.MustParseAddr("2606:4700::6810:7c60"),
			netip.MustParseAddr("104.16.124.96"),
			netip.MustParseAddr("104.16.123.96"),
		},
	})
	conn, err := dialer(context.Background(), "tcp", "")
	if err != nil || conn != client {
		t.Fatalf("conn=%#v err=%v", conn, err)
	}
	if got := strings.Join(addresses, ","); got != "104.16.124.96:443,104.16.123.96:443" {
		t.Fatalf("dial order = %s", got)
	}
}

func TestFilteringProxyConnectViaHTTPUpstream(t *testing.T) {
	connectSeen := make(chan *http.Request, 1)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodConnect {
			t.Fatalf("upstream method = %s", r.Method)
		}
		if r.Host != "public.example:443" || r.Header.Get("Proxy-Authorization") != "Basic dXNlcjpwYXNz" {
			t.Fatalf("upstream connect host=%q auth=%q", r.Host, r.Header.Get("Proxy-Authorization"))
		}
		connectSeen <- r
		w.WriteHeader(http.StatusOK)
	}))
	defer upstream.Close()
	upstreamURL, err := url.Parse(upstream.URL)
	if err != nil {
		t.Fatal(err)
	}
	upstreamURL.User = url.UserPassword("user", "pass")
	proxy := FilteringProxy{
		Validator:     NewValidator(policy.DefaultConfig(), fakeResolver{"public.example": {"93.184.216.34"}}),
		UpstreamProxy: upstreamURL,
	}
	req := httptest.NewRequest(http.MethodConnect, "http://public.example:443", nil)
	req.Host = "public.example:443"
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, req)
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("recorder CONNECT code = %d body=%s", rr.Code, rr.Body.String())
	}
	select {
	case <-connectSeen:
	case <-time.After(time.Second):
		t.Fatal("upstream CONNECT not observed")
	}
}

func TestFilteringProxyUpstreamConnectFailureBranches(t *testing.T) {
	decision := Decision{Host: "public.example", Port: "443"}
	if _, err := (FilteringProxy{UpstreamProxy: &url.URL{Scheme: "socks5", Host: "proxy.example:1080"}}).connect(context.Background(), decision); err == nil {
		t.Fatalf("expected unsupported upstream scheme")
	}

	dialer := &recordingDialer{err: errors.New("dial failed")}
	_, err := (FilteringProxy{UpstreamProxy: &url.URL{Scheme: "http", Host: "proxy.example"}, Dial: dialer.DialContext}).connect(context.Background(), decision)
	if err == nil || strings.Join(dialer.addresses, ",") != "proxy.example:80" {
		t.Fatalf("dial err=%v addresses=%v", err, dialer.addresses)
	}

	cases := []struct {
		name     string
		upstream *url.URL
		conn     *scriptedConn
		want     string
	}{
		{
			name:     "connect write",
			upstream: &url.URL{Scheme: "http", Host: "proxy.example:8080"},
			conn:     &scriptedConn{failWriteAt: 1},
			want:     "write failed",
		},
		{
			name:     "auth write",
			upstream: &url.URL{Scheme: "http", Host: "proxy.example:8080", User: url.UserPassword("user", "pass")},
			conn:     &scriptedConn{failWriteAt: 2},
			want:     "write failed",
		},
		{
			name:     "blank line write",
			upstream: &url.URL{Scheme: "http", Host: "proxy.example:8080"},
			conn:     &scriptedConn{failWriteAt: 2},
			want:     "write failed",
		},
		{
			name:     "read response",
			upstream: &url.URL{Scheme: "http", Host: "proxy.example:8080"},
			conn:     &scriptedConn{readErr: io.ErrUnexpectedEOF},
			want:     "unexpected EOF",
		},
		{
			name:     "non ok response",
			upstream: &url.URL{Scheme: "http", Host: "proxy.example:8080"},
			conn:     &scriptedConn{read: strings.NewReader("HTTP/1.1 407 Proxy Authentication Required\r\nContent-Length: 0\r\n\r\n")},
			want:     "CONNECT failed",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			proxy := FilteringProxy{
				UpstreamProxy: tc.upstream,
				Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
					return tc.conn, nil
				},
			}
			conn, err := proxy.connect(context.Background(), decision)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("connect conn=%#v err=%v, want %q", conn, err, tc.want)
			}
			if !tc.conn.closed {
				t.Fatalf("upstream connection was not closed")
			}
		})
	}
}

func TestFilteringProxyConnectTunnelCopiesBytes(t *testing.T) {
	targetClient, targetServer := net.Pipe()
	targetDone := make(chan error, 1)
	go func() {
		defer func() { _ = targetServer.Close() }()
		buf := make([]byte, 4)
		if _, err := io.ReadFull(targetServer, buf); err != nil {
			targetDone <- err
			return
		}
		if string(buf) != "ping" {
			targetDone <- fmt.Errorf("target read %q", string(buf))
			return
		}
		_, err := targetServer.Write([]byte("pong"))
		targetDone <- err
	}()

	dialer := &recordingDialer{conn: targetClient}
	proxy := FilteringProxy{
		Validator: NewValidator(policy.DefaultConfig(), fakeResolver{"public.example": {"93.184.216.34"}}),
		Dial:      dialer.DialContext,
	}
	server := httptest.NewServer(proxy)
	defer server.Close()
	serverURL, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client, err := net.Dial("tcp", serverURL.Host)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = client.Close() }()
	if err := client.SetDeadline(time.Now().Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprint(client, "CONNECT public.example HTTP/1.1\r\nHost: public.example\r\n\r\n"); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(client)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, "200 Connection Established") {
		t.Fatalf("CONNECT status = %q", status)
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			t.Fatal(err)
		}
		if line == "\r\n" {
			break
		}
	}
	if _, err := client.Write([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(reader, buf); err != nil {
		t.Fatal(err)
	}
	if string(buf) != "pong" {
		t.Fatalf("tunnel read = %q", string(buf))
	}
	select {
	case err := <-targetDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("target did not receive tunneled bytes")
	}
	if got := strings.Join(dialer.addresses, ","); got != "93.184.216.34:443" {
		t.Fatalf("CONNECT dial addresses = %s", got)
	}
}

func TestFilteringProxyRejectsRelativeURLAndHelpers(t *testing.T) {
	proxy := FilteringProxy{Validator: NewValidator(policy.DefaultConfig(), nil)}
	rr := httptest.NewRecorder()
	proxy.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/relative", nil))
	if rr.Code != http.StatusBadRequest {
		t.Fatalf("relative code = %d", rr.Code)
	}
	if ProxyURL("127.0.0.1:1234") != "http://127.0.0.1:1234" || ProxyURL("http://x") != "http://x" {
		t.Fatalf("ProxyURL mismatch")
	}
	if !strings.Contains(proxy.String(), "FilteringProxy") {
		t.Fatalf("String = %s", proxy.String())
	}
}

type recordingDialer struct {
	addresses []string
	conn      net.Conn
	err       error
}

func (d *recordingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.addresses = append(d.addresses, address)
	if d.err != nil {
		return nil, d.err
	}
	if d.conn == nil {
		return nil, errors.New("no connection")
	}
	return d.conn, nil
}

type routingDialer struct {
	addresses []string
	routes    map[string]string
}

func (d *routingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	d.addresses = append(d.addresses, address)
	route := d.routes[address]
	if route == "" {
		return nil, fmt.Errorf("unexpected dial: %s", address)
	}
	nd := net.Dialer{}
	return nd.DialContext(ctx, network, route)
}

type hijackErrorRecorder struct {
	header http.Header
}

func (r *hijackErrorRecorder) Header() http.Header { return r.header }

func (r *hijackErrorRecorder) Write([]byte) (int, error) { return 0, nil }

func (r *hijackErrorRecorder) WriteHeader(int) {}

func (r *hijackErrorRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return nil, nil, errors.New("hijack failed")
}

type scriptedConn struct {
	read        *strings.Reader
	readErr     error
	failWriteAt int
	writes      int
	closed      bool
}

func (c *scriptedConn) Read(p []byte) (int, error) {
	if c.read != nil {
		n, err := c.read.Read(p)
		if err == io.EOF && c.readErr != nil {
			return n, c.readErr
		}
		return n, err
	}
	if c.readErr != nil {
		return 0, c.readErr
	}
	return 0, io.EOF
}

func (c *scriptedConn) Write(p []byte) (int, error) {
	c.writes++
	if c.failWriteAt == c.writes {
		return 0, errors.New("write failed")
	}
	return len(p), nil
}

func (c *scriptedConn) Close() error {
	c.closed = true
	return nil
}

func (c *scriptedConn) LocalAddr() net.Addr { return staticAddr("local") }

func (c *scriptedConn) RemoteAddr() net.Addr { return staticAddr("remote") }

func (c *scriptedConn) SetDeadline(time.Time) error { return nil }

func (c *scriptedConn) SetReadDeadline(time.Time) error { return nil }

func (c *scriptedConn) SetWriteDeadline(time.Time) error { return nil }

type staticAddr string

func (a staticAddr) Network() string { return string(a) }

func (a staticAddr) String() string { return string(a) }

func oneShotHTTPConn(t *testing.T) net.Conn {
	t.Helper()
	client, server := net.Pipe()
	go func() {
		defer func() { _ = server.Close() }()
		req, err := http.ReadRequest(bufio.NewReader(server))
		if err != nil {
			return
		}
		if req.URL.Path != "/path" || req.Host != "public.example" {
			return
		}
		_, _ = fmt.Fprint(server, "HTTP/1.1 200 OK\r\nX-Test: yes\r\nContent-Length: 2\r\n\r\nok")
	}()
	return client
}
