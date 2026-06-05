package sidecar

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestParseEndpointAcceptsLoopbackAndANSI(t *testing.T) {
	input := "noise\nWebsocket endpoint:\x1b[93m ws://127.0.0.1:1234/token \x1b[0m\n"
	got, err := ParseEndpoint(context.Background(), strings.NewReader(input), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ws://127.0.0.1:1234/token" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestParseEndpointUsesDefaultTimeout(t *testing.T) {
	got, err := ParseEndpoint(context.Background(), strings.NewReader("ws://127.0.0.1:1234/token\n"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ws://127.0.0.1:1234/token" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestParseEndpointAcceptsStudioLocalhost(t *testing.T) {
	input := "Websocket endpoint: ws://localhost:1234/token\n"
	got, err := ParseEndpoint(context.Background(), strings.NewReader(input), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ws://localhost:1234/token" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestParseEndpointRejectsWildcardAndPublic(t *testing.T) {
	for _, input := range []string{
		"ws://0.0.0.0:1234/token\n",
		"ws://[::]:1234/token\n",
		"ws://8.8.8.8:1234/token\n",
	} {
		if _, err := ParseEndpoint(context.Background(), strings.NewReader(input), time.Second); err == nil {
			t.Fatalf("expected endpoint rejected: %q", input)
		}
	}
}

func TestParseEndpointHandlesLongDiagnosticLines(t *testing.T) {
	long := strings.Repeat("x", 128*1024)
	input := long + "\nws://[::1]:5555/token\n"
	got, err := ParseEndpoint(context.Background(), strings.NewReader(input), time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got != "ws://[::1]:5555/token" {
		t.Fatalf("endpoint = %q", got)
	}
}

func TestRedactEndpoint(t *testing.T) {
	got := RedactEndpoint("ws://localhost:1234/raw-secret")
	if got != "ws://localhost:1234/<redacted>" {
		t.Fatalf("redacted = %q", got)
	}
	if got := RedactEndpoint("ws-secret-without-slash"); got != "ws-secret-without-slash" {
		t.Fatalf("redacted without slash = %q", got)
	}
}

func TestParseEndpointErrorAndTimeoutEdges(t *testing.T) {
	if _, err := ParseEndpoint(context.Background(), strings.NewReader("process exited\n"), time.Second); err == nil {
		t.Fatal("endpoint parser accepted empty output")
	}
	if _, err := ParseEndpoint(context.Background(), errReader{}, time.Second); err == nil {
		t.Fatal("endpoint parser ignored reader error")
	}
	reader, writer := io.Pipe()
	defer func() { _ = reader.Close() }()
	defer func() { _ = writer.Close() }()
	if _, err := ParseEndpoint(context.Background(), reader, time.Millisecond); !errors.Is(err, ErrTimeout) {
		t.Fatalf("timeout err = %v", err)
	}
	if err := validateEndpointHost(context.Background(), "ws://example.com/token"); err == nil {
		t.Fatal("invalid endpoint host accepted")
	}
}

func TestValidateEndpointHostResolverEdges(t *testing.T) {
	restore := replaceEndpointResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return nil, errors.New("resolver failed")
	})
	if err := validateEndpointHost(context.Background(), "ws://localhost:1234/token"); !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("resolver failure err = %v", err)
	}
	restore()

	restore = replaceEndpointResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return nil, nil
	})
	if err := validateEndpointHost(context.Background(), "ws://localhost:1234/token"); !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("empty resolver err = %v", err)
	}
	restore()

	restore = replaceEndpointResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
	})
	if err := validateEndpointHost(context.Background(), "ws://localhost:1234/token"); !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("non-loopback resolver err = %v", err)
	}
	restore()
}

func TestParseEndpointPropagatesResolverRejection(t *testing.T) {
	restore := replaceEndpointResolver(t, func(context.Context, string) ([]net.IPAddr, error) {
		return []net.IPAddr{{IP: net.ParseIP("203.0.113.10")}}, nil
	})
	defer restore()
	if _, err := ParseEndpoint(context.Background(), strings.NewReader("ws://localhost:1234/token\n"), time.Second); !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("parse resolver rejection err = %v", err)
	}
}

type errReader struct{}

func (errReader) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func replaceEndpointResolver(t *testing.T, fn func(context.Context, string) ([]net.IPAddr, error)) func() {
	t.Helper()
	old := lookupEndpointIPAddrs
	lookupEndpointIPAddrs = fn
	restore := func() { lookupEndpointIPAddrs = old }
	t.Cleanup(restore)
	return restore
}
