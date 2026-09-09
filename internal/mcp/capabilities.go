package mcp

import (
	"context"
	"encoding/json"
	"net/url"
	"strings"

	"github.com/ehmo/gomoufox/internal/buildinfo"
	"github.com/ehmo/gomoufox/internal/policy"
)

func (s *Server) browserCapabilities(_ context.Context, args json.RawMessage) Response {
	var in struct{}
	if err := decode(args, &in); err != nil {
		return mcpError("invalid_arguments")
	}
	cfg := s.cfg
	scoped := policy.HasExplicitTargetScope(cfg)
	gates := map[string]bool{
		"--enable-eval": cfg.EnableEval, "--allow-localhost": cfg.AllowLocalhost,
		"--allow-browser-fetch": cfg.AllowBrowserFetch, "--allow-browser-file-fetch": cfg.AllowBrowserFileFetch,
		"--allow-cookie-values": cfg.AllowCookieValues, "--allow-cookie-mutation": cfg.AllowCookieMutation,
		"--allow-snapshot-values": cfg.AllowSnapshotValues, "--allow-session-export": cfg.AllowSessionExport,
		"--allow-session-import": cfg.AllowSessionImport, "--allow-session-proxy": cfg.AllowSessionProxy,
		"--allow-har-recording": cfg.AllowHARRecording, "--allow-har-sensitive-values": cfg.AllowHARSensitiveValues,
		"--allow-file-upload": cfg.AllowFileUpload, "--allow-file-download": cfg.AllowFileDownload,
	}
	features := make(map[string]any)
	for name, enabled := range map[string]bool{
		"browser_evaluate":    cfg.EnableEval,
		"browser_fetch":       cfg.AllowBrowserFetch && scoped,
		"browser_fetch_form":  cfg.AllowBrowserFetch && cfg.AllowBrowserFileFetch && scoped,
		"browser_upload_file": cfg.AllowFileUpload, "browser_download": cfg.AllowFileDownload,
		"session_save": cfg.AllowSessionExport, "session_load": cfg.AllowSessionImport,
		"browser_har_start": cfg.AllowHARRecording, "browser_har_stop": cfg.AllowHARRecording,
		"browser_console_messages": true, "browser_network_requests": true, "browser_performance_snapshot": true,
	} {
		available := toolInToolset(name, s.toolset)
		features[name] = map[string]bool{"listed": available, "callable": available && enabled}
	}
	return ok(map[string]any{
		"version": buildinfo.Version, "toolset": s.toolset, "gates": gates, "features": features,
		"limits": map[string]any{
			"response_bytes": cfg.MaxResponseBytes, "input_bytes": cfg.MaxInputBytes,
			"script_bytes": policy.ScriptInputBytes, "screenshot_bytes": policy.DefaultScreenshotBytes,
			"full_page_screenshot_bytes": policy.FullPageScreenshotBytes,
			"upload_file_bytes":          maxUploadFileBytes, "download_file_bytes": maxDownloadFileBytes,
			"max_sessions": cfg.MaxSessions, "session_ttl_ms": cfg.SessionTTL.Milliseconds(),
		},
		"network": map[string]any{
			"schemes": cfg.AllowedSchemes, "localhost": cfg.AllowLocalhost, "private_ips": cfg.AllowPrivateIPs,
			"explicit_target_scope": scoped, "allowed_origins": capabilityScope(cfg.AllowedOrigins, true),
			"allowed_hosts": capabilityScope(cfg.AllowedHosts, false),
		},
		"diagnostics": []string{"browser_console_messages", "browser_network_requests", "browser_performance_snapshot"},
		"hint":        "Check feature callability before use. Optional operations still require their gates. Network policy and site behavior still apply. To change gates or use unlisted diagnostics, update the configured MCP command (diagnostics: --toolset full), restart the client connection, then inspect again.",
	})
}

func capabilityScope(values []string, origins bool) map[string]any {
	items := make([]string, 0, 8)
	for _, value := range values {
		if len(items) == 8 {
			break
		}
		value = strings.TrimSpace(value)
		if origins {
			u, err := url.Parse(value)
			if err != nil || u.Host == "" {
				continue
			}
			value = u.Scheme + "://" + u.Host
		} else if strings.ContainsAny(value, "@/?#\\\r\n\t ") {
			continue
		}
		if value == "" || len(value) > 256 {
			continue
		}
		items = append(items, value)
	}
	return map[string]any{"items": items, "total": len(values), "omitted": len(values) - len(items)}
}
