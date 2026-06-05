package sidecar

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"
)

var (
	WSEndpointRE = regexp.MustCompile(`ws://(127\.0\.0\.1|\[::1\]|localhost):(\d+)/(\S+)`)
	AnyWSRE      = regexp.MustCompile(`ws://([^\s/]+)/(\S+)`)
	ansiRE       = regexp.MustCompile(`\x1b\[[0-9;]*m`)

	lookupEndpointIPAddrs = net.DefaultResolver.LookupIPAddr
)

func ParseEndpoint(ctx context.Context, r io.Reader, timeout time.Duration) (string, error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	endpoints := make(chan string, 1)
	errs := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := ansiRE.ReplaceAllString(scanner.Text(), "")
			if endpoint := WSEndpointRE.FindString(line); endpoint != "" {
				if err := validateEndpointHost(ctx, endpoint); err != nil {
					errs <- err
					return
				}
				endpoints <- endpoint
				return
			}
			if match := AnyWSRE.FindString(line); match != "" {
				errs <- fmt.Errorf("%w: non-loopback websocket endpoint rejected: %s", ErrSidecarStart, RedactEndpoint(match))
				return
			}
		}
		if err := scanner.Err(); err != nil {
			errs <- fmt.Errorf("%w: %v", ErrSidecarStart, err)
			return
		}
		errs <- fmt.Errorf("%w: exited before websocket endpoint", ErrSidecarStart)
	}()
	select {
	case endpoint := <-endpoints:
		return endpoint, nil
	case err := <-errs:
		return "", err
	case <-ctx.Done():
		return "", fmt.Errorf("%w: %v", ErrTimeout, ctx.Err())
	}
}

func validateEndpointHost(ctx context.Context, endpoint string) error {
	matches := WSEndpointRE.FindStringSubmatch(endpoint)
	if len(matches) < 2 {
		return fmt.Errorf("%w: invalid websocket endpoint: %s", ErrSidecarStart, RedactEndpoint(endpoint))
	}
	host := strings.Trim(matches[1], "[]")
	if host == "127.0.0.1" || host == "::1" {
		return nil
	}
	addrs, err := lookupEndpointIPAddrs(ctx, host)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSidecarStart, err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("%w: websocket endpoint host %q did not resolve", ErrSidecarStart, host)
	}
	for _, addr := range addrs {
		if !addr.IP.IsLoopback() {
			return fmt.Errorf("%w: websocket endpoint host %q resolved to non-loopback %s", ErrSidecarStart, host, addr.IP)
		}
	}
	return nil
}

func RedactEndpoint(endpoint string) string {
	i := strings.LastIndex(endpoint, "/")
	if i < 0 {
		return endpoint
	}
	return endpoint[:i+1] + "<redacted>"
}
