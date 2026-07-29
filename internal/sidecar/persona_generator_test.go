package sidecar

import (
	"math/rand"
	"strings"
	"testing"
)

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
