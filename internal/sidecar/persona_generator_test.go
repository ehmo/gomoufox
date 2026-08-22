package sidecar

import (
	"errors"
	"io/fs"
	"math"
	"math/rand"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
)

func preservePersonaDataForTest(t *testing.T) {
	t.Helper()
	oldFS := personaDataFS
	oldData, err := loadPersonaDataset()
	if err != nil {
		t.Fatalf("load original persona dataset: %v", err)
	}
	t.Cleanup(func() {
		personaDataFS = oldFS
		personaData = struct {
			once sync.Once
			data personaDataset
		}{}
		personaData.once.Do(func() {})
		personaData.data = oldData
	})
}

func resetPersonaDataForTest(data personaDataset, loaded bool) {
	personaData = struct {
		once sync.Once
		data personaDataset
	}{}
	if loaded {
		personaData.once.Do(func() {})
		personaData.data = data
	}
}

// Regression test: the bundled apify fingerprint dataset contains screen
// samples with innerWidth=0/innerHeight=0. Upstream camoufox-python skips
// falsy values when casting samples to Camoufox properties; emitting them
// makes Camoufox spoof window.innerWidth/innerHeight to 0 in every JS world,
// which breaks all Playwright pointer actions ("element is outside of the
// viewport").
func TestMergeScreenSampleSkipsFalsyValues(t *testing.T) {
	config := map[string]any{}
	sample := map[string]any{
		"screen": map[string]any{
			"innerWidth":  float64(0),
			"innerHeight": float64(0),
			"outerWidth":  float64(1720),
			"outerHeight": float64(1329),
			"width":       float64(1920),
			"height":      float64(1080),
			"availWidth":  float64(0),
			"colorDepth":  float64(24),
		},
	}
	mergeScreenSample(config, sample, Config{})

	for _, key := range []string{"window.innerWidth", "window.innerHeight", "screen.availWidth"} {
		if value, ok := config[key]; ok {
			t.Errorf("expected falsy sample value for %s to be skipped, got %v", key, value)
		}
	}
	if got := config["window.outerWidth"]; got != float64(1720) {
		t.Errorf("window.outerWidth = %v, want 1720", got)
	}
	if got := config["screen.width"]; got != float64(1920) {
		t.Errorf("screen.width = %v, want 1920", got)
	}
	if got := config["screen.colorDepth"]; got != float64(24) {
		t.Errorf("screen.colorDepth = %v, want 24", got)
	}
	// Explicit defaults for absent screenX/screenY must still apply.
	if got := config["window.screenX"]; got != 0 {
		t.Errorf("window.screenX = %v, want 0", got)
	}
	if got := config["window.screenY"]; got != 0 {
		t.Errorf("window.screenY = %v, want 0", got)
	}
}

func TestMergeScreenSampleCentersCustomWindow(t *testing.T) {
	config := map[string]any{}
	sample := map[string]any{
		"screen": map[string]any{
			"width":       float64(1440),
			"height":      float64(900),
			"outerWidth":  float64(1280),
			"outerHeight": float64(720),
			"innerWidth":  float64(1260),
			"innerHeight": float64(680),
			"screenX":     float64(0),
		},
	}
	mergeScreenSample(config, sample, Config{Window: &Size{Width: 1200, Height: 800}})

	for key, want := range map[string]int{
		"window.outerWidth":  1200,
		"window.outerHeight": 800,
		"window.innerWidth":  1180,
		"window.innerHeight": 760,
		"window.screenX":     120,
		"window.screenY":     50,
	} {
		if got := config[key]; got != want {
			t.Errorf("%s = %v, want %d", key, got, want)
		}
	}
}

func TestPersonaMappingsSkipFalsyValues(t *testing.T) {
	config := map[string]any{}
	sample := map[string]any{
		"maxTouchPoints": float64(0),
		"extraProperties": map[string]any{
			"globalPrivacyControl": false,
		},
		"battery": map[string]any{
			"charging":        false,
			"chargingTime":    float64(0),
			"dischargingTime": float64(42),
		},
	}
	mergeNavigatorSample(config, sample)
	mergeExtraPropertiesSample(config, sample)
	mergeBatterySample(config, sample)
	if _, ok := config["navigator.maxTouchPoints"]; ok {
		t.Fatalf("falsy navigator value emitted: %#v", config)
	}
	if _, ok := config["navigator.globalPrivacyControl"]; ok {
		t.Fatalf("falsy extra property emitted: %#v", config)
	}
	if _, ok := config["battery:charging"]; ok {
		t.Fatalf("falsy battery value emitted: %#v", config)
	}
	if got := config["battery:dischargingTime"]; got != float64(42) {
		t.Fatalf("battery:dischargingTime = %#v, want 42", got)
	}
}

func TestGeneratedPersonaConfigNeverSpoofsZeroInnerViewport(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	for i := 0; i < 25; i++ {
		config, err := generatePersonaConfig(Config{OS: "macos"}, rng)
		if err != nil {
			t.Fatalf("generatePersonaConfig: %v", err)
		}
		for _, key := range []string{"window.innerWidth", "window.innerHeight"} {
			if value, ok := config[key]; ok {
				if number, isNumber := value.(float64); isNumber && number == 0 {
					t.Fatalf("persona %d: %s spoofed to 0; this breaks Playwright pointer actions", i, key)
				}
			}
		}
	}
}

func TestGeneratedPersonaConfigUsesManagedFirefoxVersion(t *testing.T) {
	for _, tc := range []struct {
		name    string
		config  Config
		version string
	}{
		{name: "managed default", config: Config{OS: "linux"}, version: "135.0"},
		{name: "explicit override", config: Config{OS: "linux", FFVersion: 149}, version: "149.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			config, err := generatePersonaConfig(tc.config, rand.New(rand.NewSource(1)))
			if err != nil {
				t.Fatal(err)
			}
			userAgent, _ := config["navigator.userAgent"].(string)
			if !strings.Contains(userAgent, "rv:"+tc.version) || !strings.Contains(userAgent, "Firefox/"+tc.version) {
				t.Fatalf("navigator.userAgent = %q, want Firefox %s", userAgent, tc.version)
			}
		})
	}
}

func TestManagedPersonaFirefoxMajorMatchesBrowserPin(t *testing.T) {
	if personaFirefoxVersion(Config{}) != camoufoxFirefoxMajor || personaFirefoxVersion(Config{FFVersion: 149}) != 149 {
		t.Fatalf("persona Firefox major does not honor the managed pin or override")
	}
	if !strings.HasPrefix(CamoufoxBinaryVersion, "v135.") {
		t.Fatalf("Camoufox pin %q changed; update camoufoxFirefoxMajor with the browser pin", CamoufoxBinaryVersion)
	}
}

func TestRewritePersonaFirefoxVersionMatchesCamoufoxBoundaries(t *testing.T) {
	input := "99.0 100.0 135.0 135.01 1135.0 150.0x"
	want := "99.0 127.0 127.0 135.01 1135.0 127.0x"
	if got := rewritePersonaFirefoxVersionString(input, 127); got != want {
		t.Fatalf("rewrite = %q, want %q", got, want)
	}
}

func TestApplyPersonaFontsMatchesCamoufoxMerge(t *testing.T) {
	config := map[string]any{}
	err := applyPersonaFonts(config, Config{Fonts: []string{"Inter", "Arial"}}, []string{"Arial", "Noto Sans", "Arial"})
	if err != nil {
		t.Fatal(err)
	}
	got := config["fonts"].([]string)
	want := []string{"Arial", "Inter", "Noto Sans"}
	if len(got) != len(want) {
		t.Fatalf("fonts = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("fonts = %#v, want %#v", got, want)
		}
	}
}

func TestApplyPersonaFontsRejectsEmptyCustomSet(t *testing.T) {
	err := applyPersonaFonts(map[string]any{}, Config{CustomFontsOnly: true}, nil)
	if err == nil {
		t.Fatal("expected custom fonts only without font families to fail")
	}
}

func TestIsFalsyPersonaValue(t *testing.T) {
	cases := []struct {
		value any
		want  bool
	}{
		{float64(0), true},
		{float64(1080), false},
		{float64(-5), false},
		{0, true},
		{7, false},
		{"", true},
		{"x", false},
		{false, true},
		{true, false},
		{map[string]any{}, false},
	}
	for _, tc := range cases {
		if got := isFalsyPersonaValue(tc.value); got != tc.want {
			t.Errorf("isFalsyPersonaValue(%#v) = %v, want %v", tc.value, got, tc.want)
		}
	}
}

func TestPersonaDatasetLoaderRejectsMissingAndMalformedAssets(t *testing.T) {
	validNetwork := []byte(`{"nodes":[]}`)
	validFonts := []byte(`{"lin":["Arial"]}`)
	validWebGL := []byte(`[]`)
	tests := []struct {
		name string
		fs   fs.ReadFileFS
		want string
	}{
		{name: "network missing", fs: fstest.MapFS{}, want: "fingerprint-network-definition.json"},
		{name: "network malformed", fs: fstest.MapFS{"personadata/apify/fingerprint-network-definition.json": {Data: []byte("{")}}, want: "decode personadata/apify/fingerprint-network-definition.json"},
		{name: "fonts missing", fs: fstest.MapFS{"personadata/apify/fingerprint-network-definition.json": {Data: validNetwork}}, want: "fonts.json"},
		{name: "fonts malformed", fs: fstest.MapFS{
			"personadata/apify/fingerprint-network-definition.json": {Data: validNetwork},
			"personadata/camoufox/fonts.json":                       {Data: []byte("{")},
		}, want: "decode Camoufox fonts"},
		{name: "webgl missing", fs: fstest.MapFS{
			"personadata/apify/fingerprint-network-definition.json": {Data: validNetwork},
			"personadata/camoufox/fonts.json":                       {Data: validFonts},
		}, want: "webgl-data.json"},
		{name: "webgl malformed", fs: fstest.MapFS{
			"personadata/apify/fingerprint-network-definition.json": {Data: validNetwork},
			"personadata/camoufox/fonts.json":                       {Data: validFonts},
			"personadata/camoufox/webgl-data.json":                  {Data: []byte("{")},
		}, want: "decode Camoufox WebGL data"},
		{name: "valid", fs: fstest.MapFS{
			"personadata/apify/fingerprint-network-definition.json": {Data: validNetwork},
			"personadata/camoufox/fonts.json":                       {Data: validFonts},
			"personadata/camoufox/webgl-data.json":                  {Data: validWebGL},
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			preservePersonaDataForTest(t)
			personaDataFS = tc.fs
			resetPersonaDataForTest(personaDataset{}, false)

			_, err := loadPersonaDataset()
			if tc.want == "" {
				if err != nil {
					t.Fatalf("loadPersonaDataset: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("loadPersonaDataset error = %v, want %q", err, tc.want)
			}
		})
	}
}

func TestPersonaGenerationReportsDatasetAndSamplingFailures(t *testing.T) {
	preservePersonaDataForTest(t)
	resetPersonaDataForTest(personaDataset{onceErr: errors.New("dataset unavailable")}, true)
	if _, err := generatePersonaConfig(Config{}, rand.New(rand.NewSource(1))); err == nil || !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("generatePersonaConfig dataset error = %v", err)
	}
	if _, _, err := sampleWebGLConfig(Config{}, rand.New(rand.NewSource(1))); err == nil || !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("sampleWebGLConfig dataset error = %v", err)
	}

	resetPersonaDataForTest(personaDataset{network: personaNetwork{Nodes: []personaNode{{Name: "userAgent"}}}}, true)
	if _, err := generatePersonaConfig(Config{}, rand.New(rand.NewSource(1))); err == nil || !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("generatePersonaConfig sampling error = %v", err)
	}
}

func TestPersonaGenerationMapsHeadersAndRejectsEmptyCustomFonts(t *testing.T) {
	preservePersonaDataForTest(t)
	resetPersonaDataForTest(personaDataset{
		network: personaNetwork{Nodes: []personaNode{
			{Name: "userAgent", PossibleValues: []string{"Firefox/135.0 (X11; Linux)"}, ConditionalProbabilities: map[string]any{"Firefox/135.0 (X11; Linux)": 1.0}},
			{Name: "headers", PossibleValues: []string{stringifiedPrefix + `{"Accept-Encoding":"gzip"}`}, ConditionalProbabilities: map[string]any{stringifiedPrefix + `{"Accept-Encoding":"gzip"}`: 1.0}},
		}},
		fonts: map[string][]string{"lin": {"Arial"}},
	}, true)
	got, err := generatePersonaConfig(Config{}, rand.New(rand.NewSource(1)))
	if err != nil || got["headers.Accept-Encoding"] != "gzip" {
		t.Fatalf("generated headers = %#v, %v", got, err)
	}
	if _, err := generatePersonaConfig(Config{CustomFontsOnly: true}, rand.New(rand.NewSource(1))); err == nil || !errors.Is(err, ErrSidecarStart) {
		t.Fatalf("empty custom fonts error = %v", err)
	}
}

func TestPersonaWebGLSamplingFiltersAndWeightsByOS(t *testing.T) {
	preservePersonaDataForTest(t)
	resetPersonaDataForTest(personaDataset{webgl: []personaWebGLRow{
		{Vendor: "zero", Renderer: "zero", Lin: 0, Mac: 0, Win: 0},
		{Vendor: "linux", Renderer: "gpu", Lin: 1, Data: map[string]any{"id": "linux", "webGl2Enabled": true}},
		{Vendor: "mac", Renderer: "gpu", Mac: 1, Data: map[string]any{"id": "mac"}},
		{Vendor: "win", Renderer: "gpu", Win: 1, Data: map[string]any{"id": "win"}},
	}}, true)
	for _, tc := range []struct {
		os, want string
		enabled  bool
	}{
		{os: "linux", want: "linux", enabled: true},
		{os: "macos", want: "mac"},
		{os: "windows", want: "win"},
	} {
		got, enabled, err := sampleWebGLConfig(Config{OS: tc.os}, rand.New(rand.NewSource(1)))
		if err != nil || got["id"] != tc.want || enabled != tc.enabled {
			t.Fatalf("sampleWebGLConfig(%s) = %#v, %v, %v", tc.os, got, enabled, err)
		}
		if _, found := got["webGl2Enabled"]; found {
			t.Fatalf("internal webGl2Enabled leaked: %#v", got)
		}
	}
	if _, _, err := sampleWebGLConfig(Config{WebGL: &WebGLConfig{Vendor: "missing", Renderer: "missing"}}, rand.New(rand.NewSource(1))); err == nil {
		t.Fatal("missing explicit WebGL sample accepted")
	}
}

func TestPersonaNetworkSamplingAndValueDecoding(t *testing.T) {
	rng := rand.New(rand.NewSource(1))
	network := personaNetwork{Nodes: []personaNode{
		{Name: "userAgent", PossibleValues: []string{"Chrome/Linux", "Firefox/135.0 (X11; Linux)"}, ConditionalProbabilities: map[string]any{"Chrome/Linux": 1.0, "Firefox/135.0 (X11; Linux)": 1.0}},
		{Name: "screen", PossibleValues: []string{stringifiedPrefix + `{"width":1280}`}, ConditionalProbabilities: map[string]any{stringifiedPrefix + `{"width":1280}`: 1.0}},
		{Name: "missing", PossibleValues: []string{"*MISSING_VALUE*"}, ConditionalProbabilities: map[string]any{"*MISSING_VALUE*": 1.0}},
	}}
	got, err := network.generatePersonaSample("linux", rng)
	if err != nil || !strings.Contains(got["userAgent"].(string), "Firefox/") || got["missing"] != nil {
		t.Fatalf("generatePersonaSample = %#v, %v", got, err)
	}
	if got["screen"].(map[string]any)["width"] != float64(1280) {
		t.Fatalf("decoded screen = %#v", got["screen"])
	}

	failing := personaNetwork{Nodes: []personaNode{{Name: "userAgent", PossibleValues: []string{"Chrome/Linux"}, ConditionalProbabilities: map[string]any{"Chrome/Linux": 1.0}}}}
	if _, err := failing.generatePersonaSample("linux", rng); err == nil {
		t.Fatal("network without an allowed Firefox user agent succeeded")
	}
	if got := decodePersonaValue(stringifiedPrefix + "{"); got != stringifiedPrefix+"{" {
		t.Fatalf("malformed stringified value = %#v", got)
	}
}

func TestPersonaNodeProbabilityFallbacks(t *testing.T) {
	rng := rand.New(rand.NewSource(2))
	node := personaNode{
		Name:        "child",
		ParentNames: []string{"parent"},
		ConditionalProbabilities: map[string]any{
			"deeper": map[string]any{"direct": map[string]any{"a": 1}},
			"skip":   map[string]any{"b": 2.0, "ignored": "x", "deeper": map[string]any{}, "skip": map[string]any{}},
		},
	}
	if got := node.probabilities(map[string]any{"parent": "direct"}); got["a"] != 1 {
		t.Fatalf("direct probabilities = %#v", got)
	}
	if got := node.probabilities(map[string]any{"parent": "other"}); got["b"] != 2 {
		t.Fatalf("skip probabilities = %#v", got)
	}
	if got, ok := node.sample(map[string]any{"parent": "other"}, nil, rng); !ok || got != "b" {
		t.Fatalf("sample fallback = %q, %v", got, ok)
	}
	node.ConditionalProbabilities = map[string]any{"deeper": map[string]any{}}
	if got := node.probabilities(map[string]any{"parent": "missing"}); got != nil {
		t.Fatalf("missing probability branch = %#v", got)
	}
	if _, ok := node.sample(map[string]any{"parent": "missing"}, nil, rng); ok {
		t.Fatal("empty probabilities produced a sample")
	}
	node = personaNode{PossibleValues: []string{"zero", "missing"}, ConditionalProbabilities: map[string]any{"zero": 0.0}}
	if _, ok := node.sample(nil, nil, rng); ok {
		t.Fatal("zero and missing probabilities produced a sample")
	}
	node = personaNode{PossibleValues: []string{"nan"}, ConditionalProbabilities: map[string]any{"nan": math.NaN()}}
	if got, ok := node.sample(nil, nil, rng); !ok || got != "nan" {
		t.Fatalf("non-finite fallback = %q, %v", got, ok)
	}
}

func TestPersonaMappingsCoverNestedAndTypedValues(t *testing.T) {
	rewritten := rewritePersonaFirefoxVersion(map[string]any{
		"array": []any{"Firefox/135.0", 7},
		"plain": "unchanged",
	}, 140).(map[string]any)
	if rewritten["array"].([]any)[0] != "Firefox/140.0" || rewritten["array"].([]any)[1] != 7 || rewritten["plain"] != "unchanged" {
		t.Fatalf("rewritten persona = %#v", rewritten)
	}
	if got := rewritePersonaFirefoxVersionString("1135.0", 140); got != "1135.0" {
		t.Fatalf("digit-bounded version changed: %q", got)
	}

	config := map[string]any{"fonts": []any{"Zed", 1, "Arial"}}
	if err := applyPersonaFonts(config, Config{}, []string{"Arial"}); err != nil {
		t.Fatal(err)
	}
	if got := config["fonts"].([]string); len(got) != 2 || got[0] != "Arial" || got[1] != "Zed" {
		t.Fatalf("fonts = %#v", got)
	}
	config = map[string]any{"fonts": []string{"Zed"}}
	if err := applyPersonaFonts(config, Config{}, nil); err != nil || config["fonts"].([]string)[0] != "Zed" {
		t.Fatalf("string fonts = %#v, %v", config, err)
	}
	config = map[string]any{}
	if err := applyPersonaFonts(config, Config{CustomFontsOnly: true, Fonts: []string{"Solo"}}, nil); err != nil || config["fonts"].([]string)[0] != "Solo" {
		t.Fatalf("custom fonts = %#v, %v", config, err)
	}
	if got := dedupeSortedStrings([]string{"one"}); len(got) != 1 {
		t.Fatalf("single dedupe = %#v", got)
	}
}

func TestPersonaScreenAndOSHelpers(t *testing.T) {
	config := map[string]any{}
	mergeScreenSample(config, map[string]any{"screen": map[string]any{
		"width": float64(-1), "height": float64(900), "outerWidth": 1200, "outerHeight": 800,
		"innerWidth": 1100, "innerHeight": 700, "screenX": 20,
	}}, Config{Screen: &Size{Width: 1600, Height: 1000}, Window: &Size{Width: 1000, Height: 600}})
	if config["screen.width"] != 1600 || config["screen.availWidth"] != 1600 || config["screen.height"] != 1000 || config["screen.availHeight"] != 1000 {
		t.Fatalf("screen override = %#v", config)
	}
	if config["window.innerWidth"] != 900 || config["window.innerHeight"] != 500 || config["window.screenX"] != 320 || config["window.screenY"] != 200 {
		t.Fatalf("window adjustment = %#v", config)
	}
	if got, ok := personaInt("x"); got != 0 || ok {
		t.Fatalf("personaInt string = %d, %v", got, ok)
	}
	if got, ok := personaInt(float64(3)); got != 3 || !ok {
		t.Fatalf("personaInt float = %d, %v", got, ok)
	}
	if got := nonNegativePersonaNumber("window.outerWidth", float64(-1)); got != float64(-1) {
		t.Fatalf("non-screen number changed: %#v", got)
	}
	if got := nonNegativePersonaNumber("screen.width", float64(-1)); got != float64(0) {
		t.Fatalf("negative screen number = %#v", got)
	}
	if got := nonNegativePersonaNumber("screen.width", 3); got != 3 {
		t.Fatalf("integer screen number = %#v", got)
	}
	for _, tc := range []struct{ raw, normalized, target, ua string }{
		{raw: "mac", normalized: "macos", target: "mac", ua: "Macintosh Firefox/135.0"},
		{raw: "win", normalized: "windows", target: "win", ua: "Windows Firefox/135.0"},
		{raw: "lin", normalized: "linux", target: "lin", ua: "X11 Firefox/135.0"},
		{raw: "unknown", normalized: "linux", target: "lin", ua: "Linux Firefox/135.0"},
	} {
		if normalizePersonaOS(tc.raw) != tc.normalized || targetCamoufoxOS(tc.raw) != tc.target || !userAgentMatchesPersonaOS(tc.ua, tc.raw) {
			t.Fatalf("OS mapping for %q failed", tc.raw)
		}
	}
	if userAgentMatchesPersonaOS("Macintosh", "windows") || userAgentMatchesPersonaOS("Windows", "linux") {
		t.Fatal("cross-platform user agent accepted")
	}
}
