package sidecar

import (
	"os/exec"
	"testing"
)

func TestLaunchWindowDimensionsAreExplicit(t *testing.T) {
	venv := fakeNodeDirectRuntime(t)
	for _, test := range []struct {
		name string
		cfg  Config
		want bool
	}{
		{"native", Config{}, false},
		{"window", Config{Window: &Size{Width: 390, Height: 844}}, true},
		{"override", Config{Fingerprint: map[string]any{"window.innerWidth": 390}}, true},
		{"exact", Config{FingerprintExact: true, Fingerprint: map[string]any{"window.innerWidth": 390}}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			test.cfg.VenvDir = venv
			spec, err := buildNodeDirectSpecGo(test.cfg)
			if err != nil {
				t.Fatal(err)
			}
			payload := decodeNodeDirectPayloadForTest(t, spec.StdinBase64)
			config := decodeCamouConfigForTest(t, payload["env"].(map[string]any))
			key := "window.innerWidth"
			if test.cfg.Window != nil {
				key = "window.outerWidth"
			}
			_, exists := config[key]
			if exists != test.want {
				t.Fatalf("%s present=%v, want %v", key, exists, test.want)
			}
			args, err := LaunchArgsMap(test.cfg)
			if err != nil {
				t.Fatal(err)
			}
			wantNative := test.cfg.Window == nil && !test.cfg.FingerprintExact
			if (args["_native_window"] == true) != wantNative {
				t.Fatalf("Python native window=%v, want %v", args["_native_window"], wantNative)
			}
		})
	}
}

func TestPythonNativeWindowPreservesExplicitDimensions(t *testing.T) {
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 is required to check the compatibility launch transform")
	}
	script := `import json
class orjson:
    loads = staticmethod(json.loads)
    dumps = staticmethod(lambda value: json.dumps(value).encode())
` + pythonNativeWindowScript + `
kwargs = {"_native_window": True, "config": {"window.innerWidth": 390}}
keys = native_window_keys(kwargs)
assert "_native_window" not in kwargs
assert "window.innerWidth" not in keys
blob = json.dumps({"window.innerWidth": 390, "window.innerHeight": 700, "window.outerWidth": 1200, "screen.width": 1920, "navigator.language": "ja-JP"})
config = {"env": {"CAMOU_CONFIG_1": blob[:40], "CAMOU_CONFIG_2": blob[40:], "DISPLAY": ":1"}}
apply_native_window(config, keys)
env = config["env"]
result = json.loads("".join(env[k] for k in sorted(env) if k.startswith("CAMOU_CONFIG_")))
assert result == {"window.innerWidth": 390, "screen.width": 1920, "navigator.language": "ja-JP"}, result
assert env["DISPLAY"] == ":1"
assert native_window_keys({}) == ()
apply_native_window({}, keys)
apply_native_window({}, ())
`
	if out, err := exec.Command(python, "-c", script).CombinedOutput(); err != nil {
		t.Fatalf("Python launch transform: %v\n%s", err, out)
	}
}
