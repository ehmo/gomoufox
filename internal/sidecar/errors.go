package sidecar

import "errors"

var (
	ErrNotInstalled    = errors.New("sidecar not installed")
	ErrVersionMismatch = errors.New("sidecar version mismatch")
	ErrSidecarStart    = errors.New("sidecar failed to start")
	ErrTimeout         = errors.New("sidecar operation timed out")
	ErrProfileInUse    = errors.New("profile in use")
)
