package config

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRetentionValidate(t *testing.T) {
	disabled := RetentionConfig{}
	require.NoError(t, disabled.Validate(), "disabled config always valid")

	missingAge := RetentionConfig{Enabled: true}
	assert.Error(t, missingAge.Validate())

	valid := RetentionConfig{Enabled: true, OlderThan: "30d"}
	require.NoError(t, valid.Validate())

	weekly := RetentionConfig{Enabled: true, OlderThan: "2w", Schedule: "daily"}
	require.NoError(t, weekly.Validate())

	badAge := RetentionConfig{Enabled: true, OlderThan: "30x"}
	assert.Error(t, badAge.Validate())

	badSchedule := RetentionConfig{Enabled: true, OlderThan: "30d", Schedule: "hourly"}
	assert.Error(t, badSchedule.Validate())
}

func TestRetentionEffectiveDryRun(t *testing.T) {
	absent := RetentionConfig{}
	assert.True(t, absent.EffectiveDryRun(), "absent dry_run stays dry")

	explicitFalse := false
	assert.False(t, RetentionConfig{DryRun: &explicitFalse}.EffectiveDryRun())

	explicitTrue := true
	assert.True(t, RetentionConfig{DryRun: &explicitTrue}.EffectiveDryRun())
}

func TestParseRetentionAge(t *testing.T) {
	days, err := parseRetentionAge("30d")
	require.NoError(t, err)
	assert.Equal(t, 30, days)

	days, err = parseRetentionAge("1y")
	require.NoError(t, err)
	assert.Equal(t, 365, days)

	_, err = parseRetentionAge("0d")
	assert.Error(t, err)
}
