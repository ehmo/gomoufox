package gomoufox

import (
	"github.com/ehmo/gomoufox/internal/pwbridge"
)

func connectOptions(cfg launchConfig) pwbridge.ConnectOptions {
	return pwbridge.ConnectOptions{Timeout: cfg.connectTimeout}
}

func toPWBridgeContextOptions(cfg contextConfig) pwbridge.ContextOptions {
	out := pwbridge.ContextOptions{
		Locale:           cfg.Locale,
		TimezoneID:       cfg.TimezoneID,
		ExtraHTTPHeaders: cfg.ExtraHTTPHeaders,
	}
	if cfg.Viewport != nil {
		out.Viewport = &pwbridge.Viewport{Width: cfg.Viewport.Width, Height: cfg.Viewport.Height}
	}
	if cfg.StorageState != nil {
		out.StorageState = toBridgeStorageState(cfg.StorageState)
	}
	if cfg.Proxy != nil {
		out.Proxy = &pwbridge.Proxy{Server: cfg.Proxy.Server, Username: cfg.Proxy.Username, Password: cfg.Proxy.Password}
	}
	if cfg.HTTPCredentials != nil {
		out.HTTPCredentials = &pwbridge.HTTPCredentials{
			Username: cfg.HTTPCredentials.Username,
			Password: cfg.HTTPCredentials.Password,
		}
	}
	return out
}

func toBridgeStorageState(state *StorageState) *pwbridge.StorageState {
	if state == nil {
		return nil
	}
	out := &pwbridge.StorageState{
		Cookies: toBridgeCookies(state.Cookies),
		Origins: make([]pwbridge.Origin, 0, len(state.Origins)),
	}
	for _, origin := range state.Origins {
		o := pwbridge.Origin{Origin: origin.Origin, LocalStorage: make([]pwbridge.LSEntry, 0, len(origin.LocalStorage))}
		for _, item := range origin.LocalStorage {
			o.LocalStorage = append(o.LocalStorage, pwbridge.LSEntry{Name: item.Name, Value: item.Value})
		}
		out.Origins = append(out.Origins, o)
	}
	return out
}

func fromBridgeStorageState(state *pwbridge.StorageState) *StorageState {
	if state == nil {
		return nil
	}
	out := &StorageState{
		Cookies: fromBridgeCookieSlice(state.Cookies),
		Origins: make([]Origin, 0, len(state.Origins)),
	}
	for _, origin := range state.Origins {
		o := Origin{Origin: origin.Origin, LocalStorage: make([]LSEntry, 0, len(origin.LocalStorage))}
		for _, item := range origin.LocalStorage {
			o.LocalStorage = append(o.LocalStorage, LSEntry{Name: item.Name, Value: item.Value})
		}
		out.Origins = append(out.Origins, o)
	}
	return out
}

func toBridgeCookies(cookies []Cookie) []pwbridge.Cookie {
	out := make([]pwbridge.Cookie, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, pwbridge.Cookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: c.Expires, HTTPOnly: c.HTTPOnly, Secure: c.Secure, SameSite: c.SameSite,
		})
	}
	return out
}

func fromBridgeCookies(cookies []pwbridge.Cookie, err error) ([]Cookie, error) {
	if err != nil {
		return nil, err
	}
	return fromBridgeCookieSlice(cookies), nil
}

func fromBridgeCookieSlice(cookies []pwbridge.Cookie) []Cookie {
	out := make([]Cookie, 0, len(cookies))
	for _, c := range cookies {
		out = append(out, Cookie{
			Name: c.Name, Value: c.Value, Domain: c.Domain, Path: c.Path,
			Expires: c.Expires, HTTPOnly: c.HTTPOnly, Secure: c.Secure, SameSite: c.SameSite,
		})
	}
	return out
}
