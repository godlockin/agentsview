package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Regression for the cobra flag path: newPruneCommand's Run assembles
// PruneConfig from pflag values, and --age must go through the same
// Age→Before conversion parsePruneFlags applies. Before the fix the
// raw config reached runPrune and `--age 30d` alone failed with
// "at least one filter is required".
func TestPruneCobraPathAgeAloneSatisfiesFilters(t *testing.T) {
	cfg, err := buildPruneConfigFromFlags(
		"", -1, "", "", "30d", false, true, false)
	require.NoError(t, err)
	assert.True(t, cfg.Filter.HasFilters(),
		"--age alone must satisfy the filter requirement")
	assert.NotEmpty(t, cfg.Filter.Before, "age resolves to a before date")
	assert.True(t, cfg.DryRun)
}

func TestPruneCobraPathAgeAndBeforeMutuallyExclusive(t *testing.T) {
	_, err := buildPruneConfigFromFlags(
		"", -1, "2026-01-01", "", "30d", false, true, false)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "mutually exclusive")
}

func TestPruneCobraPathExplicitFiltersStillWork(t *testing.T) {
	cfg, err := buildPruneConfigFromFlags(
		"proj-a", -1, "", "", "", false, false, false)
	require.NoError(t, err)
	assert.True(t, cfg.Filter.HasFilters())
	assert.Equal(t, "proj-a", cfg.Filter.Project)
}
