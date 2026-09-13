package parser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppendCodexThreadNameRoundTrip(t *testing.T) {
	root := t.TempDir()
	sessionsDir := filepath.Join(root, "sessions", "2026", "09", "13")
	require.NoError(t, os.MkdirAll(sessionsDir, 0o755))
	sessionPath := filepath.Join(sessionsDir, "rollout-1.jsonl")
	require.NoError(t, os.WriteFile(sessionPath, []byte("{}\n"), 0o644))
	indexPath := filepath.Join(root, "session_index.jsonl")
	require.NoError(t, os.WriteFile(indexPath,
		[]byte(`{"id":"sess-1","thread_name":"Old title"}`+"\n"+
			`{"id":"other","thread_name":"Other"}`+"\n"), 0o644))

	require.NoError(t, AppendCodexThreadName(sessionPath, "sess-1", "Renamed"))

	payload, err := os.ReadFile(indexPath)
	require.NoError(t, err)
	titles, err := ParseCodexSessionIndexTitles(strings.NewReader(string(payload)))
	require.NoError(t, err)
	assert.Equal(t, "Renamed", titles["sess-1"], "later entry wins")
	assert.Equal(t, "Other", titles["other"], "other entries preserved")
}

func TestAppendCodexThreadNameMissingIndexIsLoud(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "x", "rollout.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionPath), 0o755))
	require.NoError(t, os.WriteFile(sessionPath, []byte("{}\n"), 0o644))

	err := AppendCodexThreadName(sessionPath, "sess-1", "Renamed")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "no longer maintains session_index.jsonl")
}

func TestAppendCodexThreadNameRejectsBlankTitle(t *testing.T) {
	root := t.TempDir()
	sessionPath := filepath.Join(root, "sessions", "x", "rollout.jsonl")
	require.NoError(t, os.MkdirAll(filepath.Dir(sessionPath), 0o755))
	require.NoError(t, os.WriteFile(sessionPath, []byte("{}\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(root, "session_index.jsonl"), []byte(""), 0o644))

	err := AppendCodexThreadName(sessionPath, "sess-1", "   ")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "empty thread title")
}
