package policy

import (
	"fmt"
	"time"
)

const (
	DefaultMaxResponseBytes = 512 * 1024
	HardMaxResponseBytes    = 512 * 1024
	DefaultMaxInputBytes    = 256 * 1024
	HardMaxInputBytes       = 1024 * 1024
	ScriptInputBytes        = 64 * 1024
	TypedTextInputBytes     = 64 * 1024
	HeaderInputBytes        = 32 * 1024
	FetchBodyInputBytes     = 1024 * 1024
	DefaultScreenshotBytes  = 2 * 1024 * 1024
	FullPageScreenshotBytes = 10 * 1024 * 1024
	InlineSessionStateBytes = 100 * 1024
	InlineSessionLoadBytes  = 256 * 1024
	DefaultMaxSessions      = 5
	HardMaxSessions         = 20
	DefaultSessionTTL       = 30 * time.Minute
	MaxSessionTTL           = 24 * time.Hour
)

type Config struct {
	AllowedSchemes      []string
	AllowPrivateIPs     bool
	EnableEval          bool
	MaxResponseBytes    int
	MaxInputBytes       int
	MaxSessions         int
	SessionTTL          time.Duration
	AllowBrowserFetch   bool
	AllowCookieValues   bool
	AllowCookieMutation bool
	AllowSnapshotValues bool
	AllowSessionExport  bool
	AllowSessionImport  bool
	AllowSessionProxy   bool
	AllowFileUpload     bool
	ContentWarning      bool
	AllowedOrigins      []string
	AllowedHosts        []string
}

func DefaultConfig() Config {
	return Config{
		AllowedSchemes:   []string{"http", "https"},
		MaxResponseBytes: DefaultMaxResponseBytes,
		MaxInputBytes:    DefaultMaxInputBytes,
		MaxSessions:      DefaultMaxSessions,
		SessionTTL:       DefaultSessionTTL,
		ContentWarning:   true,
	}
}

func HasExplicitTargetScope(cfg Config) bool {
	return len(cfg.AllowedOrigins) > 0 || len(cfg.AllowedHosts) > 0
}

func ValidateConfig(cfg Config) (Config, error) {
	if len(cfg.AllowedSchemes) == 0 {
		cfg.AllowedSchemes = []string{"http", "https"}
	}
	responseCap, err := ClampResponseCap(cfg.MaxResponseBytes)
	if err != nil {
		return Config{}, err
	}
	inputCap, err := ClampInputCap(cfg.MaxInputBytes)
	if err != nil {
		return Config{}, err
	}
	if cfg.MaxSessions == 0 {
		cfg.MaxSessions = DefaultMaxSessions
	}
	if cfg.MaxSessions < 0 || cfg.MaxSessions > HardMaxSessions {
		return Config{}, fmt.Errorf("max sessions must be between 1 and %d", HardMaxSessions)
	}
	if cfg.SessionTTL == 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	if cfg.SessionTTL < 0 || cfg.SessionTTL > MaxSessionTTL {
		return Config{}, fmt.Errorf("session ttl must be greater than 0 and at most %s", MaxSessionTTL)
	}
	cfg.MaxResponseBytes = responseCap
	cfg.MaxInputBytes = inputCap
	return cfg, nil
}
