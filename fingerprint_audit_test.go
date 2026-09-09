package gomoufox

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFingerprintAuditRejectsFailedAndMissingProbes(t *testing.T) {
	for _, failure := range []string{"python failed", "go python failed", "node failed", "compact reports", "missing field", "missing webgl field"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			paths := make([]string, 3)
			for i, name := range []string{"python", "go-python", "node"} {
				paths[i] = filepath.Join(root, name+".json")
				writeFingerprintAuditFixture(t, paths[i], name, "Linux x86_64", "UTC")
				data, err := os.ReadFile(paths[i])
				if err != nil {
					t.Fatal(err)
				}
				var report map[string]any
				if err := json.Unmarshal(data, &report); err != nil {
					t.Fatal(err)
				}
				result := report["results"].([]any)[0].(map[string]any)
				detector := result["detector"].(map[string]any)
				switch {
				case failure == "python failed" && i == 0, failure == "go python failed" && i == 1, failure == "node failed" && i == 2:
					result["outcome"] = "failed"
					result["error"] = "launch failed"
				case failure == "compact reports" && i > 0:
					delete(result, "detector")
				case failure == "missing field" && i > 0:
					delete(detector, "hardwareConcurrency")
				case failure == "missing webgl field" && i > 0:
					delete(detector["webgl"].(map[string]any), "vendor")
				}
				data, err = json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(paths[i], data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			out, err := exec.Command("python3", "-B", "scripts/fingerprint-audit.py", "--out", filepath.Join(root, "audit"), "--python-report", paths[0], "--gomoufox-python-report", paths[1], "--gomoufox-node-report", paths[2]).CombinedOutput()
			if err == nil || !strings.Contains(string(out), "Fingerprint probe failed") {
				t.Fatalf("audit accepted incomplete probe: err=%v\n%s", err, out)
			}
		})
	}
}

func writeFingerprintAuditFixture(t *testing.T, path, runtimeName, platform, timezone string) {
	t.Helper()
	report := fmt.Sprintf(`{
  "started_at": "2026-06-03T00:00:00+00:00",
  "finished_at": "2026-06-03T00:00:01+00:00",
  "runtime": %q,
  "summary": {
    "total": 1,
    "passed": 1,
    "blocked": 0,
    "failed": 0,
    "peak_rss_mib": 100.0,
    "peak_cpu_percent": 25.0
  },
  "results": [
    {
      "name": "fingerprint-local",
      "url": "http://127.0.0.1:7777/fingerprint.html",
      "kind": "local-fingerprint",
      "outcome": "passed",
      "status": 200,
      "duration_ms": 1000,
      "resources": {
        "peak_rss_mib": 100.0,
        "max_cpu_percent": 25.0
      },
      "detector": {
        "webdriver": false,
        "userAgent": "fixture-agent",
        "appVersion": "5.0",
        "vendor": "",
        "productSub": "20100101",
        "deviceMemory": null,
        "maxTouchPoints": 0,
        "cookieEnabled": true,
        "doNotTrack": null,
        "pdfViewerEnabled": true,
        "plugins": 5,
        "platform": %q,
        "languages": ["en-US", "en"],
        "hardwareConcurrency": 8,
        "screenWidth": 1366,
        "screenHeight": 768,
        "screenAvailWidth": 1366,
        "screenAvailHeight": 728,
        "colorDepth": 24,
        "pixelDepth": 24,
        "devicePixelRatio": 1,
        "timezone": %q,
        "webgl": {
          "supported": true,
          "vendor": "Mozilla",
          "renderer": "WebGL",
          "unmaskedVendor": "Mozilla",
          "unmaskedRenderer": "WebGL"
        },
        "webrtc": {"supported": true},
        "fonts": {"Arial": true},
        "canvas": {"dataURLLength": 1234}
      }
    }
  ]
}`, runtimeName, platform, timezone)
	if err := os.WriteFile(path, []byte(report), 0o600); err != nil {
		t.Fatal(err)
	}
}
