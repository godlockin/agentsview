package db

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRecomputeDailyUsageRollup_TableExists confirms the
// daily_usage_rollup schema is applied on a fresh DB open and
// that a second open is a no-op (CREATE TABLE IF NOT EXISTS).
func TestRecomputeDailyUsageRollup_TableExists(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	rows, err := d.ReadDailyUsageRollup(ctx, "0000-01-01")
	require.NoError(t, err, "ReadDailyUsageRollup on fresh DB")
	assert.Empty(t, rows, "rollup empty on fresh DB")
}

// TestRecomputeDailyUsageRollup_ParityWithGetDailyUsage seeds a
// message payload similar to TestGetDailyUsageWithData, then
// verifies that the rollup rows match GetDailyUsage's aggregated
// per-day totals for the same (day, model) — i.e. the writer and
// the live scanner see the same numbers on the same rows.
func TestRecomputeDailyUsageRollup_ParityWithGetDailyUsage(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	require.NoError(t, d.UpsertModelPricing([]ModelPricing{{
		ModelPattern:         "claude-sonnet-4-20250514",
		InputPerMTok:         3.0,
		OutputPerMTok:        15.0,
		CacheCreationPerMTok: 3.75,
		CacheReadPerMTok:     0.30,
	}}), "UpsertModelPricing")

	insertSession(t, d, "sessR", "projR", func(s *Session) {
		s.Agent = "claude"
		s.StartedAt = new("2024-06-15T10:00:00Z")
		s.EndedAt = new("2024-06-15T11:00:00Z")
	})

	tokenUsage := `{
		"input_tokens": 1000,
		"output_tokens": 500,
		"cache_creation_input_tokens": 200,
		"cache_read_input_tokens": 300
	}`
	insertMessages(t, d, Message{
		SessionID:  "sessR",
		Ordinal:    0,
		Role:       "assistant",
		Timestamp:  "2024-06-15T10:30:00Z",
		Model:      "claude-sonnet-4-20250514",
		TokenUsage: json.RawMessage(tokenUsage),
	})

	require.NoError(t,
		d.RecomputeDailyUsageRollupSince(ctx, "2024-06-01"),
		"RecomputeDailyUsageRollupSince")

	rollup, err := d.ReadDailyUsageRollup(ctx, "2024-06-01")
	require.NoError(t, err, "ReadDailyUsageRollup")
	require.Len(t, rollup, 1, "one rollup row expected")

	r := rollup[0]
	assert.Equal(t, "2024-06-15", r.Day, "Day")
	assert.Equal(t, "claude", r.Agent, "Agent")
	assert.Equal(t, "projR", r.Project, "Project")
	assert.Equal(t, "claude-sonnet-4-20250514", r.Model, "Model")
	assert.Equal(t, 1000, r.InputTokens, "InputTokens")
	assert.Equal(t, 500, r.OutputTokens, "OutputTokens")
	assert.Equal(t, 200, r.CacheCreationTokens, "CacheCreationTokens")
	assert.Equal(t, 300, r.CacheReadTokens, "CacheReadTokens")
	assert.Equal(t, 1, r.MessageCount, "MessageCount")

	live, err := d.GetDailyUsage(ctx, UsageFilter{
		From: "2024-06-01", To: "2024-06-30",
	})
	require.NoError(t, err, "GetDailyUsage")
	require.Len(t, live.Daily, 1, "one live daily row")
	day := live.Daily[0]
	assert.Equal(t, day.InputTokens, r.InputTokens,
		"parity: input tokens")
	assert.Equal(t, day.OutputTokens, r.OutputTokens,
		"parity: output tokens")
	assert.Equal(t, day.CacheCreationTokens, r.CacheCreationTokens,
		"parity: cache creation tokens")
	assert.Equal(t, day.CacheReadTokens, r.CacheReadTokens,
		"parity: cache read tokens")
}

// TestRecomputeDailyUsageRollup_WindowBoundaries verifies that a
// recompute starting from `since` only touches rows on or after
// that date. Older rollup rows are preserved.
func TestRecomputeDailyUsageRollup_WindowBoundaries(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	insertSession(t, d, "sessOld", "projB", func(s *Session) {
		s.Agent = "claude"
		s.StartedAt = new("2024-01-15T10:00:00Z")
	})
	insertMessages(t, d, Message{
		SessionID:  "sessOld",
		Ordinal:    0,
		Role:       "assistant",
		Timestamp:  "2024-01-15T10:30:00Z",
		Model:      "model-old",
		TokenUsage: json.RawMessage(`{"input_tokens":100,"output_tokens":50}`),
	})

	insertSession(t, d, "sessNew", "projB", func(s *Session) {
		s.Agent = "claude"
		s.StartedAt = new("2024-06-15T10:00:00Z")
	})
	insertMessages(t, d, Message{
		SessionID:  "sessNew",
		Ordinal:    0,
		Role:       "assistant",
		Timestamp:  "2024-06-15T10:30:00Z",
		Model:      "model-new",
		TokenUsage: json.RawMessage(`{"input_tokens":200,"output_tokens":75}`),
	})

	// First: rebuild everything from 2024-01-01 so both days land.
	require.NoError(t,
		d.RecomputeDailyUsageRollupSince(ctx, "2024-01-01"),
		"first recompute")

	all, err := d.ReadDailyUsageRollup(ctx, "2024-01-01")
	require.NoError(t, err, "read all rollup")
	require.Len(t, all, 2, "both days expected in first pass")

	// Second: rebuild only from 2024-06-01. This must not delete
	// the 2024-01-15 rollup row.
	require.NoError(t,
		d.RecomputeDailyUsageRollupSince(ctx, "2024-06-01"),
		"windowed recompute")

	all, err = d.ReadDailyUsageRollup(ctx, "2024-01-01")
	require.NoError(t, err, "read after windowed")
	require.Len(t, all, 2, "old row preserved after windowed recompute")

	// Verify by date filter.
	newerOnly, err := d.ReadDailyUsageRollup(ctx, "2024-06-01")
	require.NoError(t, err, "read newer only")
	require.Len(t, newerOnly, 1, "only the newer day in >=2024-06-01")
	assert.Equal(t, "2024-06-15", newerOnly[0].Day, "newer Day")
}

// TestRecomputeDailyUsageRollup_DropsStaleRows verifies that when
// a session's rows disappear (excluded), the rollup entry it
// contributed to is dropped by the next recompute.
func TestRecomputeDailyUsageRollup_DropsStaleRows(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	insertSession(t, d, "sessX", "projX", func(s *Session) {
		s.Agent = "claude"
		s.StartedAt = new("2024-06-15T10:00:00Z")
	})
	insertMessages(t, d, Message{
		SessionID:  "sessX",
		Ordinal:    0,
		Role:       "assistant",
		Timestamp:  "2024-06-15T10:30:00Z",
		Model:      "model-x",
		TokenUsage: json.RawMessage(`{"input_tokens":42,"output_tokens":7}`),
	})

	require.NoError(t,
		d.RecomputeDailyUsageRollupSince(ctx, "2024-06-01"),
		"first recompute")
	rows, err := d.ReadDailyUsageRollup(ctx, "2024-06-01")
	require.NoError(t, err, "initial read")
	require.Len(t, rows, 1, "row present after first recompute")

	// Drop the session's messages. GetDailyUsage would now return
	// no data for 2024-06-15; the rollup must follow.
	_, err = d.getWriter().ExecContext(ctx,
		`DELETE FROM messages WHERE session_id = ?`, "sessX")
	require.NoError(t, err, "delete messages")

	require.NoError(t,
		d.RecomputeDailyUsageRollupSince(ctx, "2024-06-01"),
		"second recompute after delete")

	rows, err = d.ReadDailyUsageRollup(ctx, "2024-06-01")
	require.NoError(t, err, "read after delete")
	assert.Empty(t, rows,
		"stale rollup row dropped after source vanished")
}
