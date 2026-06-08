package sidecar

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"

	"github.com/ehmo/gomoufox/internal/policy"
)

const pythonLaunchPayloadScript = `
import contextlib
import json
import sys

import orjson
from browserforge.fingerprints import Screen
from camoufox.server import launch_options, to_camel_case_dict

launch_kwargs = orjson.loads(sys.stdin.buffer.read())
persistent_user_data_dir = None
if launch_kwargs.pop("persistent_context", False):
    persistent_user_data_dir = launch_kwargs.pop("user_data_dir", None)
screen_value = launch_kwargs.get("screen")
if isinstance(screen_value, dict):
    width = screen_value.get("width")
    height = screen_value.get("height")
    launch_kwargs["screen"] = Screen(min_width=width, max_width=width, min_height=height, max_height=height)
window_value = launch_kwargs.get("window")
if isinstance(window_value, dict):
    launch_kwargs["window"] = (window_value.get("width"), window_value.get("height"))
webgl_value = launch_kwargs.get("webgl_config")
if isinstance(webgl_value, dict):
    launch_kwargs["webgl_config"] = (webgl_value.get("vendor"), webgl_value.get("renderer"))
with contextlib.redirect_stdout(sys.stderr):
    config = launch_options(**launch_kwargs)
if config.get("proxy") is None:
    config.pop("proxy", None)
payload = to_camel_case_dict(config)
if persistent_user_data_dir:
    payload["_userDataDir"] = persistent_user_data_dir
    payload["_sharedBrowser"] = True
print(json.dumps(payload, separators=(",", ":")))
`

func LaunchArgsMap(cfg Config) (map[string]any, error) {
	raw, err := launchArgsJSON(cfg)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	_ = json.Unmarshal(raw, &out)
	return out, nil
}

func BuildPythonLaunchPayload(ctx context.Context, python string, cfg Config) (map[string]any, error) {
	launchJSON, err := launchArgsJSON(cfg)
	if err != nil {
		return nil, err
	}
	cmd := exec.CommandContext(ctx, python, "-c", pythonLaunchPayloadScript)
	cmd.Env = append(os.Environ(), cfg.ExtraEnv...)
	cmd.Stdin = bytes.NewReader(launchJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("%w: build Python launch payload: %v: %s", ErrSidecarStart, err, policy.Redact(stderr.String()))
	}
	var out map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		return nil, fmt.Errorf("%w: decode Python launch payload: %v", ErrSidecarStart, err)
	}
	if out == nil {
		return nil, fmt.Errorf("%w: Python launch payload is not a JSON object", ErrSidecarStart)
	}
	return out, nil
}
