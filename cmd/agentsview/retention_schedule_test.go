package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/dbtest"
	"go.kenn.io/agentsview/internal/trash"
	"time"
)

func TestRunRetentionTickDryRunDeletesNothing(t *testing.T) {
	d := dbtest.OpenTestDB(t)
	dataDir := t.TempDir()
	src := filepath.Join(dataDir, "old.jsonl")
	require.NoError(t, os.WriteFile(src, []byte("old"), 0o644))
	dbtest.SeedSession(t, d, "ret-1", "proj", func(s *db.Session) {
		s.EndedAt = new("2024-01-01T00:00:00Z")
		s.FilePath = new(src)
	})

	cfg := config.Config{
		DataDir:   dataDir,
		Retention: config.RetentionConfig{Enabled: true, OlderThan: "30d"},
	}
	var out bytes.Buffer
	result, err := runRetentionTick(
		context.Background(), cfg, d, nowFix(), &out)
	require.NoError(t, err)

	assert.True(t, result.DryRun)
	assert.Equal(t, 1, result.WouldPrune)
	assert.Equal(t, 1, result.WouldTrash)
	assert.Equal(t, 0, result.Pruned)
	assert.Equal(t, 0, result.Trashed)
	assert.FileExists(t, src, "dry run keeps sources")
	assert.Contains(t, out.String(), "retention dry-run")
	assert.Contains(t, out.String(), "dry_run = false")
}

func TestRunRetentionTickWetRunPrunesAndTrashes(t *testing.T) {
	d := dbtest.OpenTestDB(t)
	dataDir := t.TempDir()
	src := filepath.Join(dataDir, "old.jsonl")
	require.NoError(t, os.WriteFile(src, []byte("old"), 0o644))
	dbtest.SeedSession(t, d, "ret-2", "proj", func(s *db.Session) {
		s.EndedAt = new("2024-01-01T00:00:00Z")
		s.FilePath = new(src)
	})

	dry := false
	cfg := config.Config{
		DataDir: dataDir,
		Retention: config.RetentionConfig{
			Enabled: true, OlderThan: "30d", DryRun: &dry,
		},
	}
	var out bytes.Buffer
	result, err := runRetentionTick(
		context.Background(), cfg, d, nowFix(), &out)
	require.NoError(t, err)

	assert.False(t, result.DryRun)
	assert.Equal(t, 1, result.Pruned)
	assert.Equal(t, 1, result.Trashed)
	assert.NoFileExists(t, src)
	assert.True(t, d.IsSessionExcluded("ret-2"))

	// The state file records the run for the restart-throttle check.
	state, err := os.ReadFile(filepath.Join(dataDir, "retention-state.json"))
	require.NoError(t, err)
	assert.Contains(t, string(state), "last_run")
}

func TestRunRetentionTickRecentSessionsUntouched(t *testing.T) {
	d := dbtest.OpenTestDB(t)
	dbtest.SeedSession(t, d, "ret-3", "proj", func(s *db.Session) {
		s.EndedAt = new("2099-01-01T00:00:00Z")
	})

	cfg := config.Config{
		DataDir:   t.TempDir(),
		Retention: config.RetentionConfig{Enabled: true, OlderThan: "30d"},
	}
	result, err := runRetentionTick(
		context.Background(), cfg, d, nowFix(), &bytes.Buffer{})
	require.NoError(t, err)
	assert.Equal(t, 0, result.WouldPrune, "recent session not selected")
}

func TestReadRetentionStateThrottles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retention-state.json")
	_, ok := readRetentionState(path)
	assert.False(t, ok, "missing state file")

	require.NoError(t, os.WriteFile(path,
		[]byte(`{"last_run":"2099-01-01T00:00:00Z"}`), 0o644))
	last, ok := readRetentionState(path)
	require.True(t, ok)
	assert.False(t, last.IsZero())
}

func nowFix() time.Time {
	return time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
}
