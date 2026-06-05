package mcp

import (
	"embed"
	"fmt"
	"strings"
)

//go:embed scripts/*.js
var mcpScripts embed.FS

var (
	mcpFetchExpression                 = mustMCPBrowserScript("fetch.js")
	boundedContentExpression           = mustMCPBrowserScript("bounded_content.js")
	viewportMetricsExpression          = mustMCPBrowserScript("viewport_metrics.js")
	mcpInternalProbePatchExpression    = mustMCPBrowserScript("internal_probe_patch.js")
	mcpInternalProbeExpression         = mustMCPBrowserScript("internal_probe.js")
	mcpInternalProbeRestoreExpression  = mustMCPBrowserScript("internal_probe_restore.js")
	fullPageMetricsExpression          = mustMCPBrowserScript("full_page_metrics.js")
	selectorMetricsExpression          = mustMCPBrowserScript("selector_metrics.js")
	snapshotExpression                 = strings.ReplaceAll(mustMCPBrowserScript("snapshot.js"), "__MAX_SNAPSHOT_VALUE_LENGTH__", fmt.Sprintf("%d", maxSnapshotValueLength))
	pageErrorObserverInstallExpression = mustMCPBrowserScript("page_error_observer_install.js")
	pageErrorObserverDrainExpression   = mustMCPBrowserScript("page_error_observer_drain.js")
	performanceSnapshotExpression      = mustMCPBrowserScript("performance_snapshot.js")
)

func mustMCPBrowserScript(name string) string {
	data, err := mcpScripts.ReadFile("scripts/" + name)
	if err != nil {
		panic(fmt.Sprintf("missing MCP browser script %s: %v", name, err))
	}
	return strings.TrimSpace(string(data))
}
