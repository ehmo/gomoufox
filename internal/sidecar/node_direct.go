package sidecar

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type nodeDirectSpec struct {
	NodeJS       string `json:"nodejs"`
	LaunchScript string `json:"launch_script"`
	CWD          string `json:"cwd"`
	StdinBase64  string `json:"stdin_base64"`
}

func buildNodeDirectSpec(ctx context.Context, python string, cfg Config) (nodeDirectSpec, error) {
	_ = ctx
	_ = python
	return buildNodeDirectSpecGo(cfg)
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
