package sidecar

import (
	"encoding/json"
	"fmt"
	"math/rand"
	"strings"
	"sync"
)

const stringifiedPrefix = "*STRINGIFIED*"

type personaNetwork struct {
	Nodes []personaNode `json:"nodes"`
}

type personaNode struct {
	Name                     string         `json:"name"`
	ParentNames              []string       `json:"parentNames"`
	PossibleValues           []string       `json:"possibleValues"`
	ConditionalProbabilities map[string]any `json:"conditionalProbabilities"`
}

type personaDataset struct {
	network personaNetwork
	fonts   map[string][]string
	webgl   []personaWebGLRow
	onceErr error
}

type personaWebGLRow struct {
	Vendor   string         `json:"vendor"`
	Renderer string         `json:"renderer"`
	Win      float64        `json:"win"`
	Mac      float64        `json:"mac"`
	Lin      float64        `json:"lin"`
	Data     map[string]any `json:"data"`
}

var personaData struct {
	once sync.Once
	data personaDataset
}

func loadPersonaDataset() (personaDataset, error) {
	personaData.once.Do(func() {
		network, err := readPersonaNetwork("personadata/apify/fingerprint-network-definition.json")
		if err != nil {
			personaData.data.onceErr = err
			return
		}
		fontsRaw, err := personaDataFS.ReadFile("personadata/camoufox/fonts.json")
		if err != nil {
			personaData.data.onceErr = err
			return
		}
		var fonts map[string][]string
		if err := json.Unmarshal(fontsRaw, &fonts); err != nil {
			personaData.data.onceErr = fmt.Errorf("decode Camoufox fonts: %w", err)
			return
		}
		webglRaw, err := personaDataFS.ReadFile("personadata/camoufox/webgl-data.json")
		if err != nil {
			personaData.data.onceErr = err
			return
		}
		var webgl []personaWebGLRow
		if err := json.Unmarshal(webglRaw, &webgl); err != nil {
			personaData.data.onceErr = fmt.Errorf("decode Camoufox WebGL data: %w", err)
			return
		}
		personaData.data.network = network
		personaData.data.fonts = fonts
		personaData.data.webgl = webgl
	})
	return personaData.data, personaData.data.onceErr
}

func readPersonaNetwork(path string) (personaNetwork, error) {
	raw, err := personaDataFS.ReadFile(path)
	if err != nil {
		return personaNetwork{}, err
	}
	var network personaNetwork
	if err := json.Unmarshal(raw, &network); err != nil {
		return personaNetwork{}, fmt.Errorf("decode %s: %w", path, err)
	}
	return network, nil
}

func generatePersonaConfig(cfg Config, rng *rand.Rand) (map[string]any, error) {
	data, err := loadPersonaDataset()
	if err != nil {
		return nil, fmt.Errorf("%w: load persona data: %v", ErrSidecarStart, err)
	}
	osName := normalizePersonaOS(cfg.OS)
	sample, err := data.network.generatePersonaSample(osName, rng)
	if err != nil {
		return nil, err
	}
	config := map[string]any{}
	mergeNavigatorSample(config, sample)
	mergeScreenSample(config, sample, cfg)
	if headers, ok := sample["headers"].(map[string]any); ok {
		if value, ok := headers["Accept-Encoding"]; ok && value != "" {
			config["headers.Accept-Encoding"] = value
		}
	}
	if extra, ok := sample["extraProperties"].(map[string]any); ok {
		if value, ok := extra["globalPrivacyControl"]; ok {
			config["navigator.globalPrivacyControl"] = value
		}
	}
	if _, ok := config["navigator.globalPrivacyControl"]; !ok {
		config["navigator.globalPrivacyControl"] = false
	}
	if fonts := cfg.Fonts; len(fonts) > 0 {
		config["fonts"] = append([]string(nil), fonts...)
	} else if !cfg.CustomFontsOnly {
		config["fonts"] = append([]string(nil), data.fonts[targetCamoufoxOS(osName)]...)
	}
	config["window.history.length"] = rng.Intn(5) + 1
	config["fonts:spacing_seed"] = rng.Intn(1_073_741_824)
	config["canvas:aaOffset"] = rng.Intn(101) - 50
	config["canvas:aaCapOffset"] = true
	return config, nil
}

func sampleWebGLConfig(cfg Config, rng *rand.Rand) (map[string]any, bool, error) {
	data, err := loadPersonaDataset()
	if err != nil {
		return nil, false, fmt.Errorf("%w: load persona data: %v", ErrSidecarStart, err)
	}
	osKey := targetCamoufoxOS(cfg.OS)
	var candidates []personaWebGLRow
	var total float64
	for _, row := range data.webgl {
		if cfg.WebGL != nil && (row.Vendor != cfg.WebGL.Vendor || row.Renderer != cfg.WebGL.Renderer) {
			continue
		}
		weight := row.weight(osKey)
		if weight <= 0 {
			continue
		}
		candidates = append(candidates, row)
		total += weight
	}
	if len(candidates) == 0 {
		return nil, false, fmt.Errorf("%w: no Camoufox WebGL sample for %s", ErrSidecarStart, osKey)
	}
	anchor := rng.Float64() * total
	var cumulative float64
	chosen := candidates[0]
	for _, row := range candidates {
		cumulative += row.weight(osKey)
		if cumulative > anchor {
			chosen = row
			break
		}
	}
	out := cloneStringAnyMap(chosen.Data)
	enabled, _ := out["webGl2Enabled"].(bool)
	delete(out, "webGl2Enabled")
	return out, enabled, nil
}

func (row personaWebGLRow) weight(osKey string) float64 {
	switch osKey {
	case "mac":
		return row.Mac
	case "win":
		return row.Win
	default:
		return row.Lin
	}
}

func (network personaNetwork) generatePersonaSample(osName string, rng *rand.Rand) (map[string]any, error) {
	restrictions := map[string]func(string) bool{
		"userAgent": func(value string) bool {
			return strings.Contains(value, "Firefox/") && userAgentMatchesPersonaOS(value, osName)
		},
	}
	for attempt := 0; attempt < 200; attempt++ {
		sample := map[string]any{}
		ok := true
		for _, node := range network.Nodes {
			value, found := node.sample(sample, restrictions[node.Name], rng)
			if !found {
				ok = false
				break
			}
			sample[node.Name] = decodePersonaValue(value)
		}
		if ok {
			return sample, nil
		}
	}
	return nil, fmt.Errorf("%w: unable to generate BrowserForge-compatible persona for %s", ErrSidecarStart, osName)
}

func (node personaNode) sample(parentValues map[string]any, allow func(string) bool, rng *rand.Rand) (string, bool) {
	probs := node.probabilities(parentValues)
	if len(probs) == 0 {
		return "", false
	}
	values := node.PossibleValues
	if len(values) == 0 {
		for value := range probs {
			values = append(values, value)
		}
	}
	var filtered []string
	var total float64
	for _, value := range values {
		prob, ok := probs[value]
		if !ok || prob <= 0 {
			continue
		}
		if allow != nil && !allow(value) {
			continue
		}
		filtered = append(filtered, value)
		total += prob
	}
	if len(filtered) == 0 {
		return "", false
	}
	anchor := rng.Float64() * total
	var cumulative float64
	for _, value := range filtered {
		cumulative += probs[value]
		if cumulative > anchor {
			return value, true
		}
	}
	return filtered[0], true
}

func (node personaNode) probabilities(parentValues map[string]any) map[string]float64 {
	current := node.ConditionalProbabilities
	for _, parent := range node.ParentNames {
		parentValue, _ := parentValues[parent].(string)
		if deeper, ok := current["deeper"].(map[string]any); ok {
			if next, ok := deeper[parentValue].(map[string]any); ok {
				current = next
				continue
			}
		}
		if skip, ok := current["skip"].(map[string]any); ok {
			current = skip
		} else {
			return nil
		}
	}
	out := map[string]float64{}
	for key, value := range current {
		if key == "deeper" || key == "skip" {
			continue
		}
		switch v := value.(type) {
		case float64:
			out[key] = v
		case int:
			out[key] = float64(v)
		}
	}
	return out
}

func decodePersonaValue(value string) any {
	if strings.HasPrefix(value, stringifiedPrefix) {
		var out any
		if err := json.Unmarshal([]byte(value[len(stringifiedPrefix):]), &out); err == nil {
			return out
		}
	}
	if value == "*MISSING_VALUE*" {
		return nil
	}
	return value
}

func mergeNavigatorSample(config map[string]any, sample map[string]any) {
	for source, target := range map[string]string{
		"userAgent":           "navigator.userAgent",
		"doNotTrack":          "navigator.doNotTrack",
		"appCodeName":         "navigator.appCodeName",
		"appName":             "navigator.appName",
		"appVersion":          "navigator.appVersion",
		"oscpu":               "navigator.oscpu",
		"platform":            "navigator.platform",
		"hardwareConcurrency": "navigator.hardwareConcurrency",
		"product":             "navigator.product",
		"maxTouchPoints":      "navigator.maxTouchPoints",
	} {
		if value, ok := sample[source]; ok && value != nil {
			config[target] = value
		}
	}
}

func mergeScreenSample(config map[string]any, sample map[string]any, cfg Config) {
	screen, _ := sample["screen"].(map[string]any)
	for source, target := range map[string]string{
		"availLeft":   "screen.availLeft",
		"availTop":    "screen.availTop",
		"availWidth":  "screen.availWidth",
		"availHeight": "screen.availHeight",
		"height":      "screen.height",
		"width":       "screen.width",
		"colorDepth":  "screen.colorDepth",
		"pixelDepth":  "screen.pixelDepth",
		"pageXOffset": "screen.pageXOffset",
		"pageYOffset": "screen.pageYOffset",
		"outerHeight": "window.outerHeight",
		"outerWidth":  "window.outerWidth",
		"innerHeight": "window.innerHeight",
		"innerWidth":  "window.innerWidth",
		"screenX":     "window.screenX",
		"screenY":     "window.screenY",
	} {
		// Skip falsy values just like upstream camoufox-python's
		// _cast_to_properties ("if not data: continue"). The bundled apify
		// fingerprint dataset contains screen samples with innerWidth=0 and
		// innerHeight=0; emitting them would make Camoufox spoof
		// window.innerWidth/innerHeight to 0 in every JS world, breaking all
		// Playwright pointer actions ("element is outside of the viewport").
		if value, ok := screen[source]; ok && value != nil && !isFalsyPersonaValue(value) {
			config[target] = nonNegativePersonaNumber(target, value)
		}
	}
	if cfg.Screen != nil {
		config["screen.width"] = cfg.Screen.Width
		config["screen.availWidth"] = cfg.Screen.Width
		config["screen.height"] = cfg.Screen.Height
		config["screen.availHeight"] = cfg.Screen.Height
	}
	if cfg.Window != nil {
		config["window.outerWidth"] = cfg.Window.Width
		config["window.outerHeight"] = cfg.Window.Height
	}
	if _, ok := config["window.screenY"]; !ok {
		config["window.screenY"] = 0
	}
	if _, ok := config["window.screenX"]; !ok {
		config["window.screenX"] = 0
	}
}

// isFalsyPersonaValue mirrors Python's truthiness check used by upstream
// camoufox-python when casting fingerprint samples to Camoufox properties:
// zero numbers, empty strings and false are skipped, never emitted.
func isFalsyPersonaValue(value any) bool {
	switch v := value.(type) {
	case float64:
		return v == 0
	case int:
		return v == 0
	case string:
		return v == ""
	case bool:
		return !v
	}
	return false
}

func nonNegativePersonaNumber(key string, value any) any {
	if !strings.HasPrefix(key, "screen.") {
		return value
	}
	if number, ok := value.(float64); ok && number < 0 {
		return float64(0)
	}
	return value
}

func normalizePersonaOS(value string) string {
	switch value {
	case "mac", "macos":
		return "macos"
	case "win", "windows":
		return "windows"
	case "lin", "linux", "":
		return "linux"
	default:
		return "linux"
	}
}

func targetCamoufoxOS(value string) string {
	switch normalizePersonaOS(value) {
	case "macos":
		return "mac"
	case "windows":
		return "win"
	default:
		return "lin"
	}
}

func userAgentMatchesPersonaOS(userAgent, osName string) bool {
	switch normalizePersonaOS(osName) {
	case "macos":
		return strings.Contains(userAgent, "Macintosh")
	case "windows":
		return strings.Contains(userAgent, "Windows")
	default:
		return strings.Contains(userAgent, "Linux") || strings.Contains(userAgent, "X11")
	}
}
