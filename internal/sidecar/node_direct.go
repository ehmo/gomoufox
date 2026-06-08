package sidecar

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/ehmo/gomoufox/internal/policy"
)

type nodeDirectSpec struct {
	NodeJS       string `json:"nodejs"`
	LaunchScript string `json:"launch_script"`
	CWD          string `json:"cwd"`
	StdinBase64  string `json:"stdin_base64"`
}

const nodeDirectPayloadScript = `
import base64
import contextlib
import json
from pathlib import Path
import sys

import orjson
from browserforge.fingerprints import Screen
from camoufox.server import LAUNCH_SCRIPT, get_nodejs, launch_options, to_camel_case_dict

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
nodejs = get_nodejs()
payload = to_camel_case_dict(config)
if persistent_user_data_dir:
    payload["_userDataDir"] = persistent_user_data_dir
    payload["_sharedBrowser"] = True
data = orjson.dumps(payload)
print(json.dumps({
    "nodejs": nodejs,
    "launch_script": str(LAUNCH_SCRIPT),
    "cwd": str(Path(nodejs).parent / "package"),
    "stdin_base64": base64.b64encode(data).decode(),
}, separators=(",", ":")))
`

func buildNodeDirectSpec(ctx context.Context, python string, cfg Config) (nodeDirectSpec, error) {
	launchJSON, err := launchArgsJSON(cfg)
	if err != nil {
		return nodeDirectSpec{}, err
	}
	cmd := exec.CommandContext(ctx, python, "-c", nodeDirectPayloadScript)
	cmd.Env = append(os.Environ(), cfg.ExtraEnv...)
	cmd.Stdin = bytes.NewReader(launchJSON)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nodeDirectSpec{}, fmt.Errorf("%w: build node-direct launch payload: %v: %s", ErrSidecarStart, err, policy.Redact(stderr.String()))
	}
	var spec nodeDirectSpec
	if err := json.Unmarshal(stdout.Bytes(), &spec); err != nil {
		return nodeDirectSpec{}, fmt.Errorf("%w: decode node-direct launch payload: %v", ErrSidecarStart, err)
	}
	if err := validateNodeDirectSpec(spec); err != nil {
		return nodeDirectSpec{}, err
	}
	return spec, nil
}

func validateNodeDirectSpec(spec nodeDirectSpec) error {
	if strings.TrimSpace(spec.NodeJS) == "" {
		return fmt.Errorf("%w: node-direct launch payload missing nodejs", ErrSidecarStart)
	}
	if strings.TrimSpace(spec.LaunchScript) == "" {
		return fmt.Errorf("%w: node-direct launch payload missing launch_script", ErrSidecarStart)
	}
	if strings.TrimSpace(spec.CWD) == "" {
		return fmt.Errorf("%w: node-direct launch payload missing cwd", ErrSidecarStart)
	}
	if strings.TrimSpace(spec.StdinBase64) == "" {
		return fmt.Errorf("%w: node-direct launch payload missing stdin_base64", ErrSidecarStart)
	}
	decoded, err := base64.StdEncoding.DecodeString(spec.StdinBase64)
	if err != nil {
		return fmt.Errorf("%w: node-direct launch payload has invalid stdin_base64", ErrSidecarStart)
	}
	var payload map[string]any
	if err := json.Unmarshal(decoded, &payload); err != nil {
		return fmt.Errorf("%w: node-direct launch payload is not JSON", ErrSidecarStart)
	}
	if payload == nil {
		return fmt.Errorf("%w: node-direct launch payload is not a JSON object", ErrSidecarStart)
	}
	if info, err := os.Stat(spec.NodeJS); err != nil {
		return fmt.Errorf("%w: node-direct nodejs path unusable: %v", ErrSidecarStart, err)
	} else if info.IsDir() {
		return fmt.Errorf("%w: node-direct nodejs path is a directory", ErrSidecarStart)
	}
	if info, err := os.Stat(spec.LaunchScript); err != nil {
		return fmt.Errorf("%w: node-direct launch script unusable: %v", ErrSidecarStart, err)
	} else if info.IsDir() {
		return fmt.Errorf("%w: node-direct launch script is a directory", ErrSidecarStart)
	}
	if info, err := os.Stat(spec.CWD); err != nil {
		return fmt.Errorf("%w: node-direct cwd unusable: %v", ErrSidecarStart, err)
	} else if !info.IsDir() {
		return fmt.Errorf("%w: node-direct cwd is not a directory", ErrSidecarStart)
	}
	return nil
}
