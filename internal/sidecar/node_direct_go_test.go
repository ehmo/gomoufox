package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestBuildNodeDirectSpecGoExplicitCompleteFingerprint(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	cache := t.TempDir()
	replaceUserCacheDir(t, cache, nil)
	fakeCachedBrowser(t, filepath.Join(cache, "camoufox"))
	t.Setenv("AWS_SECRET_ACCESS_KEY", "do-not-leak")
	t.Setenv("GOMOUFOX_DAEMON_TOKEN", "do-not-leak")
	cfg := Config{
		VenvDir:       venv,
		Headless:      0,
		BlockWebGL:    true,
		BrowserArgs:   []string{"--safe-mode"},
		FirefoxPrefs:  map[string]any{"browser.test.pref": true},
		ExtraEnv:      []string{"GOMOUFOX_TEST_ENV=1"},
		Fingerprint:   explicitCompleteFingerprintForTest(),
		LaunchProxy:   &ProxyConfig{Server: "http://127.0.0.1:7777", Username: "user", Password: "pass"},
		MainWorldEval: true,
	}

	spec, err := buildNodeDirectSpecGo(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(spec.NodeJS, filepath.Join("playwright", nodeExecutableName())) {
		t.Fatalf("nodejs = %s", spec.NodeJS)
	}
	if !strings.HasSuffix(spec.LaunchScript, filepath.Join("camoufox", "launchServer.js")) {
		t.Fatalf("launch script = %s", spec.LaunchScript)
	}
	if !strings.HasSuffix(spec.CWD, filepath.Join("playwright", "package")) {
		t.Fatalf("cwd = %s", spec.CWD)
	}
	payload := decodeNodeDirectPayloadForTest(t, spec.StdinBase64)
	if payload["headless"] != true {
		t.Fatalf("payload headless = %#v", payload["headless"])
	}
	if got := payload["args"].([]any); len(got) != 1 || got[0] != "--safe-mode" {
		t.Fatalf("payload args = %#v", got)
	}
	proxy := payload["proxy"].(map[string]any)
	if proxy["server"] != "http://127.0.0.1:7777" || proxy["username"] != "user" || proxy["password"] != "pass" {
		t.Fatalf("proxy = %#v", proxy)
	}
	prefs := payload["firefoxUserPrefs"].(map[string]any)
	if prefs["webgl.disabled"] != true || prefs["browser.test.pref"] != true {
		t.Fatalf("prefs = %#v", prefs)
	}
	env := payload["env"].(map[string]any)
	if env["GOMOUFOX_TEST_ENV"] != "1" {
		t.Fatalf("env = %#v", env["GOMOUFOX_TEST_ENV"])
	}
	if _, ok := env["AWS_SECRET_ACCESS_KEY"]; ok {
		t.Fatalf("env leaked AWS secret: %#v", env)
	}
	if _, ok := env["GOMOUFOX_DAEMON_TOKEN"]; ok {
		t.Fatalf("env leaked daemon token: %#v", env)
	}
	camouConfig := decodeCamouConfigForTest(t, env)
	if camouConfig["navigator.userAgent"] != "gomoufox-test" || camouConfig["allowMainWorld"] != true {
		t.Fatalf("camou config = %#v", camouConfig)
	}
	if _, ok := camouConfig["addons"]; !ok {
		t.Fatalf("camou config missing addons: %#v", camouConfig)
	}
}

func TestBuildNodeDirectSpecGoGeneratedFingerprintDoesNotNeedPython(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	cache := t.TempDir()
	replaceUserCacheDir(t, cache, nil)
	fakeCachedBrowser(t, filepath.Join(cache, "camoufox"))
	spec, err := buildNodeDirectSpec(context.Background(), filepath.Join(t.TempDir(), "missing-python"), Config{
		VenvDir:     venv,
		OS:          "linux",
		BlockWebGL:  true,
		BlockWebRTC: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeNodeDirectPayloadForTest(t, spec.StdinBase64)
	if got, ok := payload["args"].([]any); !ok || len(got) != 0 {
		t.Fatalf("payload args = %#v, want empty array", payload["args"])
	}
	env := payload["env"].(map[string]any)
	camouConfig := decodeCamouConfigForTest(t, env)
	for _, key := range []string{
		"navigator.userAgent",
		"navigator.platform",
		"navigator.hardwareConcurrency",
		"screen.width",
		"screen.height",
		"window.outerWidth",
		"window.outerHeight",
		"window.history.length",
		"fonts",
		"fonts:spacing_seed",
		"canvas:aaOffset",
		"canvas:aaCapOffset",
		"addons",
	} {
		if _, ok := camouConfig[key]; !ok {
			t.Fatalf("generated config missing %s: %#v", key, camouConfig)
		}
	}
	prefs := payload["firefoxUserPrefs"].(map[string]any)
	if prefs["webgl.disabled"] != true || prefs["media.peerconnection.enabled"] != false {
		t.Fatalf("prefs = %#v", prefs)
	}
}

func TestBuildNodeDirectSpecGoGeneratedFingerprintSamplesWebGL(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	cache := t.TempDir()
	replaceUserCacheDir(t, cache, nil)
	fakeCachedBrowser(t, filepath.Join(cache, "camoufox"))
	spec, err := buildNodeDirectSpec(context.Background(), filepath.Join(t.TempDir(), "missing-python"), Config{
		VenvDir:     venv,
		OS:          "linux",
		BlockWebRTC: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeNodeDirectPayloadForTest(t, spec.StdinBase64)
	camouConfig := decodeCamouConfigForTest(t, payload["env"].(map[string]any))
	for _, key := range []string{"webGl:vendor", "webGl:renderer", "webGl:parameters", "webGl2:parameters"} {
		if _, ok := camouConfig[key]; !ok {
			t.Fatalf("generated WebGL config missing %s: %#v", key, camouConfig)
		}
	}
	prefs := payload["firefoxUserPrefs"].(map[string]any)
	if prefs["webgl.force-enabled"] != true {
		t.Fatalf("prefs missing webgl.force-enabled: %#v", prefs)
	}
	if _, disabled := prefs["webgl.disabled"]; disabled {
		t.Fatalf("webgl should not be disabled: %#v", prefs)
	}
}

func TestBuildNodeDirectSpecGoGeneratedPayloadMatchesPythonShapeLive(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 to compare Go launch payload shape with pinned Python Camoufox")
	}
	python, err := VenvPython("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		OS:          "linux",
		BlockWebGL:  true,
		BlockWebRTC: true,
		BlockImages: true,
		Window:      &Size{Width: 1200, Height: 800},
		Screen:      &Size{Width: 1440, Height: 900},
		FirefoxPrefs: map[string]any{
			"browser.test.pref": true,
		},
		BrowserArgs: []string{"--safe-mode"},
		Fonts:       []string{"Inter", "Arial"},
	}
	pythonPayload, err := BuildPythonLaunchPayload(context.Background(), python, cfg)
	if err != nil {
		t.Fatal(err)
	}
	goSpec, err := buildNodeDirectSpecGo(cfg)
	if err != nil {
		t.Fatal(err)
	}
	goPayload := decodeNodeDirectPayloadForTest(t, goSpec.StdinBase64)
	for _, key := range []string{"args", "env", "executablePath", "firefoxUserPrefs", "headless"} {
		if _, ok := goPayload[key]; !ok {
			t.Fatalf("go payload missing %s: %#v", key, goPayload)
		}
		if _, ok := pythonPayload[key]; !ok {
			t.Fatalf("python payload missing %s: %#v", key, pythonPayload)
		}
	}
	goConfig := decodeCamouConfigForTest(t, goPayload["env"].(map[string]any))
	pythonConfig := decodeCamouConfigForTest(t, pythonPayload["env"].(map[string]any))
	for _, key := range []string{
		"navigator.userAgent",
		"navigator.platform",
		"navigator.hardwareConcurrency",
		"screen.width",
		"screen.height",
		"screen.availWidth",
		"screen.availHeight",
		"window.outerWidth",
		"window.outerHeight",
		"window.history.length",
		"fonts",
		"fonts:spacing_seed",
		"canvas:aaOffset",
		"canvas:aaCapOffset",
		"addons",
	} {
		if _, ok := goConfig[key]; !ok {
			t.Fatalf("go config missing %s: %#v", key, goConfig)
		}
		if _, ok := pythonConfig[key]; !ok {
			t.Fatalf("python config missing %s: %#v", key, pythonConfig)
		}
	}
}

func TestBuildNodeDirectSpecGoFailsClosedForUnsupportedOptions(t *testing.T) {
	_, err := buildNodeDirectSpecGo(Config{Persistent: true, BlockWebGL: true, Fingerprint: explicitCompleteFingerprintForTest()})
	if !errors.Is(err, errGoLaunchPlanUnsupported) {
		t.Fatalf("persistent err = %v", err)
	}
	_, err = buildNodeDirectSpecGo(Config{GeoIP: true, BlockWebGL: true, Fingerprint: explicitCompleteFingerprintForTest()})
	if !errors.Is(err, errGoLaunchPlanUnsupported) {
		t.Fatalf("geoip err = %v", err)
	}
}

func TestBuildNodeDirectSpecUsesGoPlanWithoutPythonFallback(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	cache := t.TempDir()
	replaceUserCacheDir(t, cache, nil)
	fakeCachedBrowser(t, filepath.Join(cache, "camoufox"))
	spec, err := buildNodeDirectSpec(context.Background(), filepath.Join(t.TempDir(), "missing-python"), Config{
		VenvDir:     venv,
		BlockWebGL:  true,
		Fingerprint: explicitCompleteFingerprintForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if spec.NodeJS == "" || spec.StdinBase64 == "" {
		t.Fatalf("spec = %#v", spec)
	}
}

func TestBuildNodeDirectSpecGoUsesRuntimeManifestWithoutPythonLayout(t *testing.T) {
	cacheRoot := fakeNodeDirectRuntime(t)
	if matches, err := filepath.Glob(filepath.Join(cacheRoot, "lib", "python*", "site-packages")); err != nil || len(matches) != 0 {
		t.Fatalf("python site-packages present under runtime root: matches=%v err=%v", matches, err)
	}
	spec, err := buildNodeDirectSpec(context.Background(), filepath.Join(t.TempDir(), "missing-python"), Config{
		VenvDir:     cacheRoot,
		BlockWebGL:  true,
		Fingerprint: explicitCompleteFingerprintForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	runtimeRoot := RuntimeAssetCacheRoot(cacheRoot, sidecarGOOS, sidecarGOARCH)
	if spec.NodeJS != runtimeRoot.NodeJS || spec.LaunchScript != runtimeRoot.LaunchServerJS || spec.CWD != runtimeRoot.PlaywrightPackageDir {
		t.Fatalf("spec does not use runtime manifest layout: %#v root=%#v", spec, runtimeRoot)
	}
}

func fakeNodeDirectRuntime(t *testing.T) string {
	t.Helper()
	rootDir := t.TempDir()
	root := RuntimeAssetCacheRoot(rootDir, sidecarGOOS, sidecarGOARCH)
	writeFakeRuntimeRoot(t, root, "node")
	return rootDir
}

func writeFakeRuntimeRoot(t *testing.T, root RuntimeRoot, nodeScript string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(root.NodeJS), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.NodeJS, []byte(nodeScript), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root.PlaywrightPackageDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.PlaywrightPackageDir, "package.json"), []byte(`{"version":"`+RequiredPlaywrightJSON+`"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(root.LaunchServerJS), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.LaunchServerJS, []byte(runtimeLaunchServerJS), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(root.BrowserResourcesDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root.BrowserResourcesDir, "camoufox"), []byte("browser"), 0o700); err != nil {
		t.Fatal(err)
	}
	addon := filepath.Join(root.BrowserResourcesDir, "resources", "addons", "UBO")
	if err := os.MkdirAll(addon, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := NewRuntimeAssetManifest(root, sidecarGOOS, sidecarGOARCH)
	if err := PopulateRuntimeAssetManifest(root, &manifest); err != nil {
		t.Fatal(err)
	}
	if err := WriteRuntimeAssetManifest(root.ManifestPath, manifest); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.ReadyMarkerPath, []byte("ready\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fakeCachedBrowser(t *testing.T, root string) {
	t.Helper()
	exe := filepath.Join(root, browserExecutableCandidates()[0])
	if err := os.MkdirAll(filepath.Dir(exe), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("browser"), 0o700); err != nil {
		t.Fatal(err)
	}
	addon := filepath.Join(browserResourcesDirForExecutable(exe), "addons", "UBO")
	if err := os.MkdirAll(addon, 0o700); err != nil {
		t.Fatal(err)
	}
}

func explicitCompleteFingerprintForTest() map[string]any {
	return map[string]any{
		"navigator.userAgent":           "gomoufox-test",
		"navigator.platform":            "MacIntel",
		"navigator.hardwareConcurrency": 4,
		"screen.width":                  1200,
		"screen.height":                 800,
		"screen.availWidth":             1200,
		"screen.availHeight":            800,
		"window.outerWidth":             1200,
		"window.outerHeight":            800,
		"window.screenX":                0,
		"window.screenY":                0,
	}
}

func decodeCamouConfigForTest(t *testing.T, env map[string]any) map[string]any {
	t.Helper()
	var chunks []string
	for i := 1; ; i++ {
		value, ok := env["CAMOU_CONFIG_"+strconv.Itoa(i)]
		if !ok {
			break
		}
		chunks = append(chunks, value.(string))
	}
	if len(chunks) == 0 {
		t.Fatal("missing CAMOU_CONFIG chunks")
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(strings.Join(chunks, "")), &out); err != nil {
		t.Fatal(err)
	}
	return out
}
