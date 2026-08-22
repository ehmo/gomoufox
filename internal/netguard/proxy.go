package netguard

import (
	"bufio"
	"container/list"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

type DialFunc func(ctx context.Context, network, address string) (net.Conn, error)

type FilteringProxy struct {
	Validator     Validator
	Dial          DialFunc
	UpstreamProxy *url.URL
}

const (
	// BlockedHeader uses the Fetch-forbidden Set-Cookie response header so page
	// JavaScript cannot read a guardrail marker, even for a same-origin block.
	BlockedHeader     = "Set-Cookie"
	blockedCookieName = "__Host-gomoufox_blocked"
)

const (
	blockedMarkerBytes = 32
	blockedMarkerLimit = 1024
	blockedMarkerTTL   = 2 * time.Minute
)

type blockedMarkerEntry struct {
	value   string
	reason  string
	created time.Time
}

var blockedMarkers = struct {
	sync.Mutex
	entries map[string]*list.Element
	order   list.List
}{entries: make(map[string]*list.Element)}

var blockedMarkerRandomRead = rand.Read

func issueBlockedMarker(reason string) string {
	for {
		raw := make([]byte, blockedMarkerBytes)
		if _, err := blockedMarkerRandomRead(raw); err != nil {
			panic(fmt.Sprintf("generate filtering proxy block marker: %v", err))
		}
		value := hex.EncodeToString(raw)
		now := time.Now()
		blockedMarkers.Lock()
		if _, exists := blockedMarkers.entries[value]; exists {
			blockedMarkers.Unlock()
			continue
		}
		pruneBlockedMarkers(now)
		for len(blockedMarkers.entries) >= blockedMarkerLimit {
			removeBlockedMarker(blockedMarkers.order.Front())
		}
		elem := blockedMarkers.order.PushBack(blockedMarkerEntry{value: value, reason: reason, created: now})
		blockedMarkers.entries[value] = elem
		blockedMarkers.Unlock()
		return value
	}
}

// ConsumeBlockedMarker accepts a marker once and returns the proxy's block
// reason. Single-use markers prevent an origin from replaying a marker learned
// from an earlier blocked request.
func ConsumeBlockedMarker(value string) (string, bool) {
	cookie, _, _ := strings.Cut(value, ";")
	name, cookieValue, ok := strings.Cut(cookie, "=")
	prefix := blockedCookieName + "_"
	if !ok || cookieValue != "" || !strings.HasPrefix(name, prefix) {
		return "", false
	}
	marker := strings.TrimPrefix(name, prefix)
	if len(marker) != hex.EncodedLen(blockedMarkerBytes) {
		return "", false
	}
	now := time.Now()
	blockedMarkers.Lock()
	defer blockedMarkers.Unlock()
	pruneBlockedMarkers(now)
	elem, ok := blockedMarkers.entries[marker]
	if !ok {
		return "", false
	}
	reason := elem.Value.(blockedMarkerEntry).reason
	removeBlockedMarker(elem)
	return reason, true
}

func pruneBlockedMarkers(now time.Time) {
	for elem := blockedMarkers.order.Front(); elem != nil; elem = blockedMarkers.order.Front() {
		entry := elem.Value.(blockedMarkerEntry)
		if now.Sub(entry.created) <= blockedMarkerTTL {
			return
		}
		removeBlockedMarker(elem)
	}
}

func removeBlockedMarker(elem *list.Element) {
	if elem == nil {
		return
	}
	entry := elem.Value.(blockedMarkerEntry)
	delete(blockedMarkers.entries, entry.value)
	blockedMarkers.order.Remove(elem)
}

func (p FilteringProxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
		return
	}
	p.handleHTTP(w, r)
}

func (p FilteringProxy) handleHTTP(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.String()
	if r.URL.Scheme == "" || r.URL.Host == "" {
		http.Error(w, "proxy requires absolute-form URL", http.StatusBadRequest)
		return
	}
	decision, err := p.Validator.Validate(r.Context(), rawURL)
	if err != nil {
		writeBlockedResponse(w, err)
		return
	}
	transport := &http.Transport{}
	if p.UpstreamProxy == nil {
		transport.DialContext = p.dialer(decision)
	} else {
		transport.Proxy = http.ProxyURL(p.UpstreamProxy)
		transport.DialContext = p.baseDialer()
	}
	out := r.Clone(r.Context())
	out.RequestURI = ""
	out.URL = decision.URL
	resp, err := transport.RoundTrip(out)
	if err != nil {
		http.Error(w, "proxy dial failed", http.StatusBadGateway)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	copyHeader(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}

func (p FilteringProxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	host := r.Host
	if !strings.Contains(host, ":") {
		host = net.JoinHostPort(host, "443")
	}
	rawURL := "https://" + host
	decision, err := p.Validator.Validate(r.Context(), rawURL)
	if err != nil {
		writeBlockedResponse(w, err)
		return
	}
	conn, err := p.connect(r.Context(), decision)
	if err != nil {
		http.Error(w, "proxy dial failed", http.StatusBadGateway)
		return
	}
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		_ = conn.Close()
		http.Error(w, "hijacking unsupported", http.StatusInternalServerError)
		return
	}
	client, _, err := hijacker.Hijack()
	if err != nil {
		_ = conn.Close()
		return
	}
	_, _ = client.Write([]byte("HTTP/1.1 200 Connection Established\r\n\r\n"))
	go tunnel(client, conn)
	go tunnel(conn, client)
}

func writeBlockedResponse(w http.ResponseWriter, err error) {
	marker := issueBlockedMarker(err.Error())
	w.Header().Set(BlockedHeader, fmt.Sprintf(
		"%s_%s=; Path=/; Max-Age=0; Expires=Thu, 01 Jan 1970 00:00:00 GMT; Secure; HttpOnly; SameSite=Strict",
		blockedCookieName, marker,
	))
	http.Error(w, "url blocked by guardrail", http.StatusForbidden)
}

func (p FilteringProxy) dialer(decision Decision) DialFunc {
	return func(ctx context.Context, network, _ string) (net.Conn, error) {
		var lastErr error
		for _, addr := range dialAddresses(decision) {
			address := JoinHostPort(addr.String(), decision.Port)
			conn, err := p.baseDialer()(ctx, network, address)
			if err == nil {
				return conn, nil
			}
			lastErr = err
		}
		if lastErr != nil {
			return nil, lastErr
		}
		return nil, errors.New("invalid pinned IP")
	}
}

func dialAddresses(decision Decision) []netip.Addr {
	addrs := decision.Resolved
	if len(addrs) == 0 {
		addrs = []netip.Addr{decision.PinnedIP}
	}
	ordered := make([]netip.Addr, 0, len(addrs))
	for _, addr := range addrs {
		if addr.IsValid() && addr.Is4() {
			ordered = append(ordered, addr)
		}
	}
	for _, addr := range addrs {
		if addr.IsValid() && !addr.Is4() {
			ordered = append(ordered, addr)
		}
	}
	return ordered
}

func (p FilteringProxy) baseDialer() DialFunc {
	if p.Dial != nil {
		return p.Dial
	}
	nd := net.Dialer{}
	return nd.DialContext
}

func (p FilteringProxy) connect(ctx context.Context, decision Decision) (net.Conn, error) {
	if p.UpstreamProxy == nil {
		return p.dialer(decision)(ctx, "tcp", "")
	}
	if strings.ToLower(p.UpstreamProxy.Scheme) != "http" {
		return nil, fmt.Errorf("unsupported upstream proxy scheme %q", p.UpstreamProxy.Scheme)
	}
	upstream := p.UpstreamProxy.Host
	if !strings.Contains(upstream, ":") {
		upstream = net.JoinHostPort(upstream, "80")
	}
	conn, err := p.baseDialer()(ctx, "tcp", upstream)
	if err != nil {
		return nil, err
	}
	target := JoinHostPort(decision.Host, decision.Port)
	if _, err := fmt.Fprintf(conn, "CONNECT %s HTTP/1.1\r\nHost: %s\r\n", target, target); err != nil {
		_ = conn.Close()
		return nil, err
	}
	if p.UpstreamProxy.User != nil {
		user := p.UpstreamProxy.User.String()
		encoded := base64.StdEncoding.EncodeToString([]byte(user))
		if _, err := fmt.Fprintf(conn, "Proxy-Authorization: Basic %s\r\n", encoded); err != nil {
			_ = conn.Close()
			return nil, err
		}
	}
	if _, err := io.WriteString(conn, "\r\n"); err != nil {
		_ = conn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), &http.Request{Method: http.MethodConnect})
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_ = conn.Close()
		return nil, fmt.Errorf("upstream proxy CONNECT failed: %s", resp.Status)
	}
	return conn, nil
}

func tunnel(dst, src net.Conn) {
	defer func() { _ = dst.Close() }()
	defer func() { _ = src.Close() }()
	_, _ = io.Copy(dst, src)
}

func copyHeader(dst, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

func ProxyURL(addr string) string {
	if !strings.HasPrefix(addr, "http://") {
		addr = "http://" + addr
	}
	return addr
}

func (p FilteringProxy) String() string {
	return fmt.Sprintf("FilteringProxy{%v}", p.Validator.Config)
}
