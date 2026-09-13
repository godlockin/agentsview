package config

import (
	"fmt"
	"strconv"
	"strings"
)

// RetentionConfig configures the daemon's scheduled archive
// pruning. It is read from the [retention] table of config.toml.
type RetentionConfig struct {
	// Enabled turns the scheduler on. Off by default.
	Enabled bool `toml:"enabled" json:"enabled"`
	// OlderThan selects sessions that ended more than this long ago
	// ("30d", "2w", "1y"). Required when Enabled.
	OlderThan string `toml:"older_than" json:"older_than,omitempty"`
	// Schedule fixes the cadence; only "daily" is supported today.
	Schedule string `toml:"schedule" json:"schedule,omitempty"`
	// DryRun defaults to true: the scheduler only reports what it
	// would prune until explicitly set to false.
	DryRun *bool `toml:"dry_run" json:"dry_run,omitempty"`
}

// Validate rejects configurations the scheduler cannot honor.
func (r RetentionConfig) Validate() error {
	if !r.Enabled {
		return nil
	}
	if strings.TrimSpace(r.OlderThan) == "" {
		return fmt.Errorf(
			"[retention] older_than is required when enabled (e.g. \"30d\")")
	}
	if _, err := parseRetentionAge(r.OlderThan); err != nil {
		return fmt.Errorf("[retention] older_than: %w", err)
	}
	switch strings.ToLower(strings.TrimSpace(r.Schedule)) {
	case "", "daily":
		return nil
	default:
		return fmt.Errorf(
			"[retention] schedule %q unsupported; use \"daily\"", r.Schedule)
	}
}

// EffectiveDryRun reports whether the next run deletes anything:
// the zero value is dry, so an absent dry_run key never deletes.
func (r RetentionConfig) EffectiveDryRun() bool {
	return r.DryRun == nil || *r.DryRun
}

// parseRetentionAge converts "30d" / "2w" / "1y" into whole days.
// It mirrors the CLI's --age parser (cmd/agentsview/age.go); the two
// live apart because internal packages cannot import package main.
func parseRetentionAge(s string) (int, error) {
	trimmed := strings.TrimSpace(strings.ToLower(s))
	if trimmed == "" {
		return 0, fmt.Errorf("age is empty; use forms like 7d, 30d, 2w, or 1y")
	}
	unit := trimmed[len(trimmed)-1]
	value, err := strconv.Atoi(trimmed[:len(trimmed)-1])
	if err != nil || value <= 0 {
		return 0, fmt.Errorf(
			"invalid age %q; use forms like 7d, 30d, 2w, or 1y", s)
	}
	switch unit {
	case 'd':
		return value, nil
	case 'w':
		return value * 7, nil
	case 'y':
		return value * 365, nil
	default:
		return 0, fmt.Errorf(
			"invalid age %q; use forms like 7d, 30d, 2w, or 1y", s)
	}
}
