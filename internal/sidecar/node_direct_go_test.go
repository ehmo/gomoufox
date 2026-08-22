package sidecar

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
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
	if _, ok := camouConfig["addons"]; ok {
		t.Fatalf("default config includes addons: %#v", camouConfig)
	}
}

func TestBuildNodeDirectSpecGoUsesOnlyExplicitAddons(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	cache := t.TempDir()
	replaceUserCacheDir(t, cache, nil)
	fakeCachedBrowser(t, filepath.Join(cache, "camoufox"))
	explicitAddon := t.TempDir()

	spec, err := buildNodeDirectSpecGo(Config{
		VenvDir:     venv,
		Addons:      []string{explicitAddon},
		BlockWebGL:  true,
		Fingerprint: explicitCompleteFingerprintForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeNodeDirectPayloadForTest(t, spec.StdinBase64)
	camouConfig := decodeCamouConfigForTest(t, payload["env"].(map[string]any))
	addons, ok := camouConfig["addons"].([]any)
	if !ok || len(addons) != 1 || addons[0] != explicitAddon {
		t.Fatalf("addons = %#v, want only %q", camouConfig["addons"], explicitAddon)
	}
}

func TestBuildNodeDirectSpecGoAppliesPartialFingerprintOverride(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	cache := t.TempDir()
	replaceUserCacheDir(t, cache, nil)
	fakeCachedBrowser(t, filepath.Join(cache, "camoufox"))

	spec, err := buildNodeDirectSpecGo(Config{
		VenvDir:    venv,
		OS:         "linux",
		BlockWebGL: true,
		Fingerprint: map[string]any{
			"navigator.userAgent": "gomoufox-override",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeNodeDirectPayloadForTest(t, spec.StdinBase64)
	config := decodeCamouConfigForTest(t, payload["env"].(map[string]any))
	if config["navigator.userAgent"] != "gomoufox-override" {
		t.Fatalf("navigator.userAgent = %#v", config["navigator.userAgent"])
	}
	for _, key := range []string{"navigator.platform", "screen.width", "fonts", "canvas:aaOffset"} {
		if _, ok := config[key]; !ok {
			t.Fatalf("partial override dropped generated key %s: %#v", key, config)
		}
	}
}

func TestBuildNodeDirectSpecGoAppliesExactFingerprintWithoutGeneratedKeys(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	cache := t.TempDir()
	replaceUserCacheDir(t, cache, nil)
	fakeCachedBrowser(t, filepath.Join(cache, "camoufox"))

	spec, err := buildNodeDirectSpecGo(Config{
		VenvDir:          venv,
		Fingerprint:      map[string]any{"navigator.userAgent": "exact"},
		FingerprintExact: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeNodeDirectPayloadForTest(t, spec.StdinBase64)
	config := decodeCamouConfigForTest(t, payload["env"].(map[string]any))
	if !reflect.DeepEqual(config, map[string]any{"navigator.userAgent": "exact"}) {
		t.Fatalf("exact fingerprint config = %#v", config)
	}

	_, err = buildNodeDirectSpecGo(Config{VenvDir: venv, FingerprintExact: true})
	if err == nil || !strings.Contains(err.Error(), "exact fingerprint config is empty") {
		t.Fatalf("empty exact fingerprint error = %v", err)
	}
}

func TestBuildNodeDirectSpecGoAppliesCustomFontOptions(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	cache := t.TempDir()
	replaceUserCacheDir(t, cache, nil)
	fakeCachedBrowser(t, filepath.Join(cache, "camoufox"))

	spec, err := buildNodeDirectSpecGo(Config{
		VenvDir:         venv,
		BlockWebGL:      true,
		CustomFontsOnly: true,
		Fonts:           []string{"Gomoufox Test"},
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeNodeDirectPayloadForTest(t, spec.StdinBase64)
	config := decodeCamouConfigForTest(t, payload["env"].(map[string]any))
	fonts, ok := config["fonts"].([]any)
	if !ok || len(fonts) != 1 || fonts[0] != "Gomoufox Test" {
		t.Fatalf("fonts = %#v", config["fonts"])
	}
	prefs := payload["firefoxUserPrefs"].(map[string]any)
	if prefs["gfx.bundled-fonts.activate"] != float64(0) && prefs["gfx.bundled-fonts.activate"] != 0 {
		t.Fatalf("firefox prefs = %#v", prefs)
	}

	_, err = buildNodeDirectSpecGo(Config{
		VenvDir:         venv,
		BlockWebGL:      true,
		CustomFontsOnly: true,
	})
	if err == nil {
		t.Fatal("expected custom fonts only without font families to fail")
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
	} {
		if _, ok := camouConfig[key]; !ok {
			t.Fatalf("generated config missing %s: %#v", key, camouConfig)
		}
	}
	if _, ok := camouConfig["addons"]; ok {
		t.Fatalf("generated default config includes addons: %#v", camouConfig)
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
	executablePath, err := ResolveManagedCamoufoxExecutable("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := Config{
		ExecutablePath: executablePath,
		OS:             "linux",
		BlockWebGL:     true,
		BlockWebRTC:    true,
		BlockImages:    true,
		Window:         &Size{Width: 1200, Height: 800},
		Screen:         &Size{Width: 1440, Height: 900},
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
	for name, config := range map[string]map[string]any{"go": goConfig, "python": pythonConfig} {
		userAgent, _ := config["navigator.userAgent"].(string)
		if !strings.Contains(userAgent, "rv:135.0") || !strings.Contains(userAgent, "Firefox/135.0") {
			t.Fatalf("%s navigator.userAgent = %q, want managed Firefox 135", name, userAgent)
		}
	}
	if !reflect.DeepEqual(goPayload["firefoxUserPrefs"], pythonPayload["firefoxUserPrefs"]) {
		t.Fatalf("firefox prefs differ: go=%#v python=%#v", goPayload["firefoxUserPrefs"], pythonPayload["firefoxUserPrefs"])
	}
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
	} {
		if _, ok := goConfig[key]; !ok {
			t.Fatalf("go config missing %s: %#v", key, goConfig)
		}
		if _, ok := pythonConfig[key]; !ok {
			t.Fatalf("python config missing %s: %#v", key, pythonConfig)
		}
	}
	if _, ok := goConfig["addons"]; ok {
		t.Fatalf("go default config includes addons: %#v", goConfig)
	}
	if _, ok := pythonConfig["addons"]; ok {
		t.Fatalf("python default config includes addons: %#v", pythonConfig)
	}
	if !reflect.DeepEqual(goConfig["fonts"], pythonConfig["fonts"]) {
		t.Fatalf("font lists differ: go=%#v python=%#v", goConfig["fonts"], pythonConfig["fonts"])
	}
	for name, config := range map[string]map[string]any{"go": goConfig, "python": pythonConfig} {
		if config["window.screenY"] != float64(50) && config["window.screenY"] != 50 {
			t.Fatalf("%s window.screenY = %#v, want centered value 50", name, config["window.screenY"])
		}
	}
}

func TestBuildNodeDirectSpecGoAppliesSharedPythonPersonaExactlyLive(t *testing.T) {
	if os.Getenv("GOMOUFOX_LIVE") != "1" {
		t.Skip("set GOMOUFOX_LIVE=1 to compare an exact shared Python persona with the Go launch payload")
	}
	python, err := VenvPython("")
	if err != nil {
		t.Fatal(err)
	}
	executablePath, err := ResolveManagedCamoufoxExecutable("")
	if err != nil {
		t.Fatal(err)
	}
	base := Config{
		ExecutablePath: executablePath,
		OS:             "linux",
	}
	pythonPayload, err := BuildPythonLaunchPayload(context.Background(), python, base)
	if err != nil {
		t.Fatal(err)
	}
	pythonConfig := decodeCamouConfigForTest(t, pythonPayload["env"].(map[string]any))
	pythonPrefs := pythonPayload["firefoxUserPrefs"].(map[string]any)

	shared := base
	shared.Fingerprint = pythonConfig
	shared.FirefoxPrefs = pythonPrefs
	shared.FingerprintExact = true
	goSpec, err := buildNodeDirectSpecGo(shared)
	if err != nil {
		t.Fatal(err)
	}
	goPayload := decodeNodeDirectPayloadForTest(t, goSpec.StdinBase64)
	goConfig := decodeCamouConfigForTest(t, goPayload["env"].(map[string]any))
	if !reflect.DeepEqual(goConfig, pythonConfig) {
		t.Fatalf("shared persona config differs: go=%#v python=%#v", goConfig, pythonConfig)
	}
	if !reflect.DeepEqual(goPayload["firefoxUserPrefs"], pythonPrefs) {
		t.Fatalf("shared persona Firefox prefs differ: go=%#v python=%#v", goPayload["firefoxUserPrefs"], pythonPrefs)
	}
}

func TestBuildNodeDirectSpecGoFailsClosedForUnsupportedOptions(t *testing.T) {
	_, err := buildNodeDirectSpecGo(Config{GeoIP: true, BlockWebGL: true, Fingerprint: explicitCompleteFingerprintForTest()})
	if !errors.Is(err, errGoLaunchPlanUnsupported) {
		t.Fatalf("geoip err = %v", err)
	}
	delay := 250.0
	_, err = buildNodeDirectSpecGo(Config{Humanize: &delay, BlockWebGL: true, Fingerprint: explicitCompleteFingerprintForTest()})
	if !errors.Is(err, errGoLaunchPlanUnsupported) {
		t.Fatalf("humanize err = %v", err)
	}
}

func TestBuildNodeDirectSpecGoSupportsPersistentProfile(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	cache := t.TempDir()
	replaceUserCacheDir(t, cache, nil)
	fakeCachedBrowser(t, filepath.Join(cache, "camoufox"))
	profile := t.TempDir()

	spec, err := buildNodeDirectSpecGo(Config{
		VenvDir:     venv,
		Persistent:  true,
		UserDataDir: profile,
		BlockWebGL:  true,
		Fingerprint: explicitCompleteFingerprintForTest(),
	})
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeNodeDirectPayloadForTest(t, spec.StdinBase64)
	if payload["_userDataDir"] != profile || payload["_sharedBrowser"] != true {
		t.Fatalf("persistent payload = %#v", payload)
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

func TestBuildNodeDirectSpecGoErrorAndOptionEdges(t *testing.T) {
	wantErr := errors.New("injected")
	if _, err := buildNodeDirectSpecGo(Config{VenvDir: t.TempDir()}); err == nil {
		t.Fatal("missing runtime resolved")
	}

	venv := fakeNodeDirectRuntime(t)
	oldInstalled := nodeDirectInstalledBrowser
	oldLoad := nodeDirectLoadPersonaDataset
	oldApply := nodeDirectApplyPersonaFonts
	oldValidate := nodeDirectValidateSpec
	t.Cleanup(func() {
		nodeDirectInstalledBrowser = oldInstalled
		nodeDirectLoadPersonaDataset = oldLoad
		nodeDirectApplyPersonaFonts = oldApply
		nodeDirectValidateSpec = oldValidate
	})
	nodeDirectInstalledBrowser = func(RuntimeRoot) (string, error) { return "", wantErr }
	if _, err := buildNodeDirectSpecGo(Config{VenvDir: venv}); !errors.Is(err, ErrNotInstalled) {
		t.Fatalf("browser lookup error = %v", err)
	}
	nodeDirectInstalledBrowser = oldInstalled

	if _, err := buildNodeDirectSpecGo(Config{
		VenvDir: venv,
		WebGL:   &WebGLConfig{Vendor: "missing", Renderer: "missing"},
	}); err == nil || !strings.Contains(err.Error(), "no Camoufox WebGL sample") {
		t.Fatalf("WebGL sample error = %v", err)
	}
	nodeDirectLoadPersonaDataset = func() (personaDataset, error) { return personaDataset{}, wantErr }
	if _, err := buildNodeDirectSpecGo(Config{VenvDir: venv, BlockWebGL: true}); err == nil || !strings.Contains(err.Error(), wantErr.Error()) {
		t.Fatalf("persona reload error = %v", err)
	}
	nodeDirectLoadPersonaDataset = oldLoad
	nodeDirectApplyPersonaFonts = func(map[string]any, Config, []string) error { return wantErr }
	if _, err := buildNodeDirectSpecGo(Config{VenvDir: venv, BlockWebGL: true}); !errors.Is(err, wantErr) {
		t.Fatalf("font application error = %v", err)
	}
	nodeDirectApplyPersonaFonts = oldApply

	if _, err := buildNodeDirectSpecGo(Config{
		VenvDir:          venv,
		FingerprintExact: true,
		Fingerprint:      map[string]any{"bad": make(chan struct{})},
	}); err == nil || !strings.Contains(err.Error(), "encode Camoufox config") {
		t.Fatalf("config encoding error = %v", err)
	}
	if _, err := buildNodeDirectSpecGo(Config{
		VenvDir:          venv,
		FingerprintExact: true,
		Fingerprint:      map[string]any{"navigator.userAgent": "exact"},
		FirefoxPrefs:     map[string]any{"bad": make(chan struct{})},
	}); err == nil || !strings.Contains(err.Error(), "encode Go node-direct payload") {
		t.Fatalf("payload encoding error = %v", err)
	}
	if _, err := buildNodeDirectSpecGo(Config{
		VenvDir:          venv,
		FingerprintExact: true,
		Fingerprint:      map[string]any{"navigator.userAgent": "exact"},
		Persistent:       true,
	}); err == nil || !strings.Contains(err.Error(), "requires user data dir") {
		t.Fatalf("persistent profile error = %v", err)
	}

	nodeDirectValidateSpec = func(nodeDirectSpec) error { return wantErr }
	if _, err := buildNodeDirectSpecGo(Config{
		VenvDir:          venv,
		FingerprintExact: true,
		Fingerprint:      map[string]any{"navigator.userAgent": "exact"},
	}); !errors.Is(err, wantErr) {
		t.Fatalf("spec validation error = %v", err)
	}
}

func TestBuildNodeDirectSpecGoPreferenceAndPlatformEdges(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	spec, err := buildNodeDirectSpecGo(Config{
		VenvDir:          venv,
		FingerprintExact: true,
		Fingerprint:      map[string]any{"navigator.userAgent": "exact"},
		BlockImages:      true,
		DisableCOOP:      true,
		EnableCache:      true,
	})
	if err != nil {
		t.Fatal(err)
	}
	prefs := decodeNodeDirectPayloadForTest(t, spec.StdinBase64)["firefoxUserPrefs"].(map[string]any)
	if prefs["permissions.default.image"] != float64(2) || prefs["browser.tabs.remote.useCrossOriginOpenerPolicy"] != false {
		t.Fatalf("preferences = %#v", prefs)
	}
	for key := range cachePrefs {
		if _, ok := prefs[key]; !ok {
			t.Fatalf("cache preference %q missing: %#v", key, prefs)
		}
	}

	restore := overrideSidecarPlatform(t, "windows", "amd64")
	defer restore()
	large := strings.Repeat("x", 3000)
	env, err := nodeDirectGoEnv(map[string]any{"value": large}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := env["CAMOU_CONFIG_2"]; !ok {
		t.Fatalf("Windows config was not chunked: %#v", env)
	}
	if nodeExecutableName() != "node.exe" {
		t.Fatalf("Windows node executable = %q", nodeExecutableName())
	}
	if _, err := installedRuntimeBrowserExecutable(RuntimeRoot{BrowserResourcesDir: t.TempDir()}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing installed browser = %v", err)
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
	if err := ensureRuntimePlaywrightCoreModule(root, sidecarGOOS); err != nil {
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
