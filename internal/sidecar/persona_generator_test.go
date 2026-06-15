package sidecar

import (
	"math/rand"
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
