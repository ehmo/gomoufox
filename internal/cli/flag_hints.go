package cli

import (
	"fmt"
	"strings"
)

func unknownFlagError(name string, specs map[string]flagSpec, global bool) error {
	label := "unknown flag"
	if global {
		label = "unknown global flag"
	}
	hint := ""
	replacements := map[string]string{
		"output":            "out",
		"wait-for-selector": "wait-selector",
		"wait-until":        "wait-load-state",
		"wait_until":        "wait-load-state",
		"load-state":        "wait-load-state",
		"load_state":        "wait-load-state",
	}
	replacement := replacements[name]
	if replacement == "" && strings.Contains(name, "_") {
		replacement = strings.ReplaceAll(name, "_", "-")
	}
	if _, ok := specs[replacement]; replacement != "" && ok {
		hint = "; use --" + replacement
	}
	if name == "format" {
		if _, ok := specs["markdown"]; ok {
			hint = "; use one of --html, --text or --markdown"
		}
		if _, ok := specs["quality"]; ok {
			hint = "; select PNG or JPEG with the --out filename extension (.png, .jpg or .jpeg)"
		}
	}
	if name == "url" {
		_, screenshot := specs["quality"]
		_, reader := specs["markdown"]
		_, eval := specs["script"]
		_, fetch := specs["method"]
		_, interactive := specs["humanize"]
		if screenshot || reader || eval || fetch || interactive {
			hint = "; the URL is positional: gomoufox <command> <url> [flags]"
		}
	}
	return fmt.Errorf("%s: --%s%s", label, name, hint)
}
