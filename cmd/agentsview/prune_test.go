package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/dbtest"
)

func TestPrunerKeepsClaudeParentTranscript(t *testing.T) {
	d := dbtest.OpenTestDB(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("shared transcript"), 0o600))
	dbtest.SeedSession(t, d, "parent", "test", func(s *db.Session) {
		s.Agent, s.FilePath = "claude", new(path)
	})
	dbtest.SeedSession(t, d, "leaf", "test", func(s *db.Session) {
		s.Agent, s.FilePath = "claude", new(path)
		s.ParentSessionID, s.RelationshipType = new("parent"), "fork"
		s.FileSize = new(int64(17))
		s.FirstMessage = new("prune this fork")
	})
	pruner, out := newTestPruner(t, d, "")
	require.NoError(t, pruner.Prune(t.Context(), PruneConfig{
		Filter: db.PruneFilter{FirstMessage: "prune this fork"}, Yes: true,
	}))
	parent, err := d.GetSession(t.Context(), "parent")
	require.NoError(t, err)
	assert.NotNil(t, parent)
	leaf, err := d.GetSession(t.Context(), "leaf")
	require.NoError(t, err)
	assert.Nil(t, leaf)
	assert.FileExists(t, path)
	assert.Contains(t, out.String(), "Found 1 sessions (0 B on disk), left 1 source file in place")
	assert.Contains(t, out.String(), "removed 0 files (0 B reclaimed), left 1 source file in place")
}

func TestPrunerClaudeContinuationSources(t *testing.T) {
	d := dbtest.OpenTestDB(t)
	first := filepath.Join(t.TempDir(), "first.jsonl")
	second := filepath.Join(t.TempDir(), "second.jsonl")
	for _, path := range []string{first, second} {
		require.NoError(t, os.WriteFile(path, []byte("transcript"), 0o600))
	}
	// Same layout as TestClaudeSubagentContinuationPreservesForks:
	// the resumed session uses both files, and the fork uses the second.
	result, err := d.WriteSessionBatchContext(t.Context(), []db.SessionBatchWrite{{
		Session:               db.Session{ID: "main", Agent: "claude", Project: "test", Machine: "local", FilePath: new(first)},
		ClaudeSubagentSources: []string{first, second}, ReplaceMessages: true,
	}})
	require.NoError(t, err)
	require.Equal(t, 1, result.WrittenSessions)
	dbtest.SeedSession(t, d, "fork", "test", func(s *db.Session) {
		s.Agent, s.FilePath, s.FirstMessage = "claude", new(second), new("fork question")
		s.ParentSessionID, s.RelationshipType = new("main"), "fork"
	})
	assert.Equal(t, first, d.GetSessionFilePath(t.Context(), "main"))
	for path, want := range map[string]int{first: 1, second: 2} {
		count, err := d.CountSessionsByFilePath(t.Context(), path)
		require.NoError(t, err)
		assert.Equal(t, want, count)
	}
	pruner, out := newTestPruner(t, d, "")
	require.NoError(t, pruner.Prune(t.Context(), PruneConfig{Filter: db.PruneFilter{FirstMessage: "fork question"}, Yes: true}))
	fork, err := d.GetSession(t.Context(), "fork")
	require.NoError(t, err)
	assert.Nil(t, fork)
	main, err := d.GetSession(t.Context(), "main")
	require.NoError(t, err)
	assert.NotNil(t, main)
	assert.FileExists(t, first)
	assert.FileExists(t, second)
	assert.Contains(t, out.String(), "removed 0 files (0 B reclaimed), left 1 source file in place")
	// The joined session's primary path also appears in its provenance.
	// Once it is the only candidate, that duplicate mustn't keep its file.
	require.NoError(t, pruner.Prune(t.Context(), PruneConfig{Filter: db.PruneFilter{Project: "test"}, Yes: true}))
	assert.NoFileExists(t, first)
}

func TestPrunerKeepsSharedSourceFiles(t *testing.T) {
	for _, mode := range []string{"delete", "dry run", "abort"} {
		t.Run(mode, func(t *testing.T) {
			d := dbtest.OpenTestDB(t)
			hermes := filepath.Join(t.TempDir(), "state.db")
			augure := filepath.Join(t.TempDir(), "state.db")
			for _, path := range []string{hermes, augure} {
				require.NoError(t, os.WriteFile(path, []byte("app state"), 0o600))
			}
			// Only bare state.db paths can reach file deletion; synced rows
			// hold state.db#id, which isn't a physical file path.
			for _, row := range []struct{ id, agent, path string }{
				{"hermes", "hermes", hermes},
				{"augure", "augure-desktop", augure},
				{"member", "hermes", hermes + "#member"},
			} {
				dbtest.SeedSession(t, d, row.id, "test", func(s *db.Session) {
					s.Agent, s.FilePath = row.agent, new(row.path)
				})
			}
			pruner, out := newTestPruner(t, d, "n\n")
			require.NoError(t, pruner.Prune(t.Context(), PruneConfig{
				Filter: db.PruneFilter{Project: "test"},
				Yes:    mode == "delete", DryRun: mode == "dry run",
			}))
			for _, path := range []string{hermes, augure} {
				content, err := os.ReadFile(path)
				require.NoError(t, err)
				assert.Equal(t, "app state", string(content))
			}
			assert.Contains(t, out.String(), "left 2 source files in place")
			if mode == "delete" {
				assert.Contains(t, out.String(), "Deleted 3 sessions, removed 0 files (0 B reclaimed), left 2 source files in place")
			}
			for _, id := range []string{"hermes", "augure", "member"} {
				session, err := d.GetSession(t.Context(), id)
				require.NoError(t, err)
				if mode == "delete" {
					assert.Nil(t, session)
				} else {
					assert.NotNil(t, session)
				}
			}
		})
	}
}

type pruneInputFunc func([]byte) (int, error)

func (f pruneInputFunc) Read(b []byte) (int, error) { return f(b) }

func TestPrunerRechecksSourcesAfterConfirmation(t *testing.T) {
	d := dbtest.OpenTestDB(t)
	path := filepath.Join(t.TempDir(), "session.jsonl")
	require.NoError(t, os.WriteFile(path, []byte("transcript"), 0o600))
	dbtest.SeedSession(t, d, "fork", "test", func(s *db.Session) {
		s.Agent, s.FilePath, s.FirstMessage = "claude", new(path), new("fork question")
	})
	pruner, out := newTestPruner(t, d, "")
	pruner.In = pruneInputFunc(func(b []byte) (int, error) {
		dbtest.SeedSession(t, d, "survivor", "test", func(s *db.Session) {
			s.Agent, s.FilePath = "claude", new(path)
		})
		return copy(b, "yes\n"), nil
	})
	require.NoError(t, pruner.Prune(t.Context(), PruneConfig{Filter: db.PruneFilter{FirstMessage: "fork question"}}))
	assert.FileExists(t, path)
	survivor, err := d.GetSession(t.Context(), "survivor")
	require.NoError(t, err)
	assert.NotNil(t, survivor)
	fork, err := d.GetSession(t.Context(), "fork")
	require.NoError(t, err)
	assert.Nil(t, fork)
	assert.Contains(t, out.String(), "removed 0 files (0 B reclaimed), left 1 source file in place")
}

func TestPrunerRemovesClaudeAndKeepsHermes(t *testing.T) {
	d := dbtest.OpenTestDB(t)
	claude := filepath.Join(t.TempDir(), "session.jsonl")
	hermes := filepath.Join(t.TempDir(), "state.db")
	for _, row := range []struct{ agent, path string }{{"claude", claude}, {"hermes", hermes}} {
		require.NoError(t, os.WriteFile(row.path, []byte("source data"), 0o600))
		dbtest.SeedSession(t, d, row.agent, "test", func(s *db.Session) {
			s.Agent, s.FilePath, s.FileSize = row.agent, new(row.path), new(int64(11))
		})
	}
	pruner, out := newTestPruner(t, d, "")
	require.NoError(t, pruner.Prune(t.Context(), PruneConfig{
		Filter: db.PruneFilter{Project: "test"}, Yes: true,
	}))
	assert.NoFileExists(t, claude)
	content, err := os.ReadFile(hermes)
	require.NoError(t, err)
	assert.Equal(t, "source data", string(content))
	for _, id := range []string{"claude", "hermes"} {
		session, err := d.GetSession(t.Context(), id)
		require.NoError(t, err)
		assert.Nil(t, session)
	}
	assert.Contains(t, out.String(), "Found 2 sessions (11 B on disk), left 1 source file in place")
	assert.Contains(t, out.String(), "Deleted 2 sessions, removed 1 files (11 B reclaimed), left 1 source file in place")
}

func TestPrunerKeepsUnownedAgentFile(t *testing.T) {
	for _, agent := range []string{"nope", "gemini"} {
		t.Run(agent, func(t *testing.T) {
			d := dbtest.OpenTestDB(t)
			path := filepath.Join(t.TempDir(), "session.jsonl")
			require.NoError(t, os.WriteFile(path, []byte("transcript"), 0o600))
			dbtest.SeedSession(t, d, "unknown", "test", func(s *db.Session) {
				s.Agent, s.FilePath = agent, new(path)
			})
			pruner, out := newTestPruner(t, d, "")
			require.NoError(t, pruner.Prune(t.Context(), PruneConfig{
				Filter: db.PruneFilter{Project: "test"}, Yes: true,
			}))
			assert.FileExists(t, path)
			session, err := d.GetSession(t.Context(), "unknown")
			require.NoError(t, err)
			assert.Nil(t, session)
			assert.Contains(t, out.String(), "left 1 source file in place")
		})
	}
}

func TestDeleteFilesCountsExistingContainers(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile#one", "state.db")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte("app state"), 0o600))
	removed, reclaimed, kept := deleteFiles([]db.Session{
		{Agent: "hermes", FilePath: new(path)},
		{Agent: "hermes", FilePath: new(path + "#one")},
		{Agent: "hermes", FilePath: new(path + "#two")},
		{Agent: "hermes", FilePath: new(filepath.Join(t.TempDir(), "state.db") + "#three")},
	}, nil)
	assert.Equal(t, 0, removed)
	assert.Zero(t, reclaimed)
	assert.Equal(t, 1, kept)
	assert.FileExists(t, path)
}

func TestParsePruneFlags(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		wantErr string
		check   func(t *testing.T, cfg PruneConfig)
	}{
		{
			name:    "no filters",
			args:    []string{},
			wantErr: "at least one filter",
		},
		{
			name: "project filter",
			args: []string{"--project", "myapp"},
			check: func(t *testing.T, cfg PruneConfig) {
				t.Helper()

				assert.Equal(t, "myapp", cfg.Filter.Project)
				assert.False(t, cfg.DryRun, "DryRun default")
				assert.False(t, cfg.Yes, "Yes default")
			},
		},
		{
			name: "all flags",
			args: []string{
				"--project", "p",
				"--max-messages", "5",
				"--before", "2024-01-01",
				"--first-message", "hello",
				"--dry-run",
				"--yes",
			},
			check: func(t *testing.T, cfg PruneConfig) {
				t.Helper()

				assert.Equal(t, "p", cfg.Filter.Project)
				require.NotNil(t, cfg.Filter.MaxMessages)
				assert.Equal(t, 5, *cfg.Filter.MaxMessages)
				assert.Equal(t, "2024-01-01", cfg.Filter.Before)
				assert.Equal(t, "hello", cfg.Filter.FirstMessage)
				assert.True(t, cfg.DryRun, "DryRun should be true")
				assert.True(t, cfg.Yes, "Yes should be true")
			},
		},
		{
			name:    "unknown flag",
			args:    []string{"--bogus"},
			wantErr: "flag provided but not defined",
		},
		{
			name:    "negative max-messages",
			args:    []string{"--max-messages", "-2"},
			wantErr: "max-messages must be >= 0",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := parsePruneFlags(tt.args)
			if tt.wantErr != "" {
				require.Error(t, err,
					"expected error containing %q", tt.wantErr)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}
			require.NoError(t, err)
			if tt.check != nil {
				tt.check(t, cfg)
			}
		})
	}
}

func TestParsePruneFlagsHelp(t *testing.T) {
	_, err := parsePruneFlags([]string{"--help"})
	require.ErrorIs(t, err, flag.ErrHelp)
}

func TestPrunerEmptyFilterReturnsError(t *testing.T) {
	d := dbtest.OpenTestDB(t)

	pruner, _ := newTestPruner(t, d, "")
	cfg := PruneConfig{
		Filter: db.PruneFilter{},
	}

	err := pruner.Prune(t.Context(), cfg)
	require.Error(t, err, "expected error for empty filter")
	assert.Contains(t, err.Error(), "at least one filter",
		"error should mention filter requirement")
}

func TestConfirm(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"yes lowercase", "y\n", true},
		{"yes full", "yes\n", true},
		{"YES uppercase", "YES\n", true},
		{"no", "n\n", false},
		{"empty", "\n", false},
		{"other text", "maybe\n", false},
		{"y with spaces", "  y  \n", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			in := strings.NewReader(tt.input)
			out := &bytes.Buffer{}
			got := confirm(in, out, "Delete?")
			assert.Equal(t, tt.want, got)
			assert.Contains(t, out.String(), "[y/N]",
				"prompt missing [y/N]")
		})
	}
}

func TestWriteSummary(t *testing.T) {
	sessions := []db.Session{
		{ID: "s1", Agent: "claude", FilePath: new("session.jsonl"), Project: "projA", FileSize: new(int64(1024))},
		{ID: "s2", Agent: "hermes", Project: "projA", FileSize: new(int64(2048))},
		{ID: "s3", Project: "projB"},
	}

	var buf bytes.Buffer
	writeSummary(&buf, sessions, nil)
	out := buf.String()

	want := `Found 3 sessions (1.0 KB on disk)

By project:
  projA                                    2
  projB                                    1
`
	assert.Equal(t, want, out, "writeSummary() mismatch")
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		input int64
		want  string
	}{
		{0, "0 B"},
		{512, "512 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1048576, "1.0 MB"},
		{1073741824, "1.0 GB"},
	}

	for _, tt := range tests {
		name := fmt.Sprintf("%d_bytes", tt.input)
		t.Run(name, func(t *testing.T) {
			assert.Equal(t, tt.want, formatBytes(tt.input),
				"formatBytes(%d)", tt.input)
		})
	}
}

func TestPrunerMaxMessagesCountsUserOnly(t *testing.T) {
	d := dbtest.OpenTestDB(t)

	// Session with 1 user message + 49 assistant messages.
	// max-messages=1 should match because only user messages
	// are counted.
	dbtest.SeedSession(t, d, "oneshot", "proj", func(s *db.Session) {
		s.MessageCount = 50
	})
	msgs := []db.Message{dbtest.UserMsg("oneshot", 0, "do it")}
	for i := 1; i < 50; i++ {
		msgs = append(msgs,
			dbtest.AsstMsg("oneshot", i, "working..."))
	}
	dbtest.SeedMessages(t, d, msgs...)

	// Session with 5 user messages + 5 assistant messages.
	// max-messages=1 should NOT match.
	dbtest.SeedSession(t, d, "multi", "proj", func(s *db.Session) {
		s.MessageCount = 10
	})
	dbtest.SeedMessages(t, d,
		dbtest.UserMsg("multi", 0, "step 1"),
		dbtest.AsstMsg("multi", 1, "done 1"),
		dbtest.UserMsg("multi", 2, "step 2"),
		dbtest.AsstMsg("multi", 3, "done 2"),
		dbtest.UserMsg("multi", 4, "step 3"),
		dbtest.AsstMsg("multi", 5, "done 3"),
		dbtest.UserMsg("multi", 6, "step 4"),
		dbtest.AsstMsg("multi", 7, "done 4"),
		dbtest.UserMsg("multi", 8, "step 5"),
		dbtest.AsstMsg("multi", 9, "done 5"),
	)

	pruner, buf := newTestPruner(t, d, "")
	cfg := PruneConfig{
		Filter: db.PruneFilter{MaxMessages: new(1)},
		DryRun: true,
	}

	require.NoError(t, pruner.Prune(t.Context(), cfg), "Prune")

	out := buf.String()
	assert.Contains(t, out, "Found 1 sessions",
		"expected 1 match (oneshot only)")
}

func TestPruner_PruneScenarios(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		cfg        PruneConfig
		wantOutput []string
		wantKept   bool
	}{
		{
			name:       "dry run",
			cfg:        PruneConfig{Filter: db.PruneFilter{Project: "test"}, DryRun: true},
			wantOutput: []string{"Dry run", "Found 1 sessions"},
			wantKept:   true,
		},
		{
			name:       "no matches",
			cfg:        PruneConfig{Filter: db.PruneFilter{Project: "nonexistent"}},
			wantOutput: []string{"No sessions match"},
			wantKept:   true,
		},
		{
			name:       "abort",
			input:      "n\n",
			cfg:        PruneConfig{Filter: db.PruneFilter{Project: "test"}},
			wantOutput: []string{"Aborted"},
			wantKept:   true,
		},
		{
			name:       "confirm delete",
			input:      "y\n",
			cfg:        PruneConfig{Filter: db.PruneFilter{Project: "test"}},
			wantOutput: []string{"Deleted 1 sessions"},
			wantKept:   false,
		},
		{
			name:       "yes flag skips prompt",
			cfg:        PruneConfig{Filter: db.PruneFilter{Project: "test"}, Yes: true},
			wantOutput: []string{"Deleted 1 sessions"},
			wantKept:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			d := dbtest.OpenTestDB(t)
			dbtest.SeedSession(t, d, "s1", "test", func(s *db.Session) {
				s.EndedAt = new("2024-01-01T00:00:00Z")
				s.MessageCount = 0
			})

			pruner, buf := newTestPruner(t, d, tt.input)
			require.NoError(t, pruner.Prune(t.Context(), tt.cfg), "Prune")

			out := buf.String()
			for _, want := range tt.wantOutput {
				assert.Contains(t, out, want,
					"expected output containing %q", want)
			}
			if tt.cfg.Yes {
				assert.NotContains(t, out, "[y/N]",
					"should not prompt when --yes is set")
			}

			s, _ := d.GetSession(t.Context(), "s1")
			if tt.wantKept {
				assert.NotNil(t, s, "session was deleted unexpectedly")
			} else {
				assert.Nil(t, s, "session still exists")
			}
		})
	}
}

func TestDeleteFilesRemovesFiles(t *testing.T) {
	d := dbtest.OpenTestDB(t)
	var paths []string
	for _, agent := range []string{"claude", "codex", "traex", "augure-code"} {
		path := filepath.Join(t.TempDir(), agent, "session.jsonl")
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
		require.NoError(t, os.WriteFile(path, []byte("transcript"), 0o600))
		paths = append(paths, path)
		dbtest.SeedSession(t, d, agent, "test", func(s *db.Session) {
			s.Agent, s.FilePath = agent, new(path)
		})
	}
	pruner, out := newTestPruner(t, d, "")
	require.NoError(t, pruner.Prune(t.Context(), PruneConfig{
		Filter: db.PruneFilter{Project: "test"}, Yes: true,
	}))
	for _, path := range paths {
		assert.NoFileExists(t, path)
		assert.NoDirExists(t, filepath.Dir(path))
	}
	assert.Contains(t, out.String(), "Deleted 4 sessions, removed 4 files (40 B reclaimed)")
	assert.NotContains(t, out.String(), "source files in place")
}

func TestDeleteFilesMissingFile(t *testing.T) {
	sessions := []db.Session{
		{ID: "s1", Agent: "claude", FilePath: new(filepath.Join(t.TempDir(), "missing.jsonl"))},
	}

	removed, reclaimed, kept := deleteFiles(sessions, nil)
	assert.Equal(t, 0, removed)
	assert.Equal(t, int64(0), reclaimed)
	assert.Zero(t, kept)
}

func TestDeleteFilesNilPath(t *testing.T) {
	sessions := []db.Session{
		{ID: "s1", FilePath: nil},
	}

	removed, reclaimed, kept := deleteFiles(sessions, nil)
	assert.Equal(t, 0, removed)
	assert.Equal(t, int64(0), reclaimed)
	assert.Zero(t, kept)
}

func newTestPruner(t *testing.T, d *db.DB, input string) (*Pruner, *bytes.Buffer) {
	t.Helper()
	var buf bytes.Buffer
	p := &Pruner{
		DB:  d,
		Out: &buf,
		In:  strings.NewReader(input),
	}
	return p, &buf
}
