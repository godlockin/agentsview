package db

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBackupArchiveToProducesOpenableSnapshot verifies the VACUUM
// INTO read-only backup path end to end: the snapshot must exist,
// open, and contain the seeded rows.
func TestBackupArchiveToProducesOpenableSnapshot(t *testing.T) {
	d := testDB(t)
	ctx := context.Background()

	insertSession(t, d, "bk-1", "proj", func(s *Session) {
		s.Agent = "claude"
		s.StartedAt = Ptr("2026-06-25T10:00:00Z")
	})
	insertMessages(t, d, Message{
		SessionID: "bk-1",
		Ordinal:   0,
		Role:      "user",
		Content:   "backup me",
		Timestamp: "2026-06-25T10:30:00Z",
	})

	snapshot := filepath.Join(t.TempDir(), "nested", "sessions.db")
	require.NoError(t, BackupArchiveTo(ctx, d.Path(), snapshot))

	info, err := os.Stat(snapshot)
	require.NoError(t, err)
	assert.Greater(t, info.Size(), int64(0))

	restored, err := OpenReadOnly(snapshot)
	require.NoError(t, err)
	defer restored.Close()

	got, err := restored.GetSession(ctx, "bk-1")
	require.NoError(t, err)
	require.NotNil(t, got, "seeded session must be in the snapshot")
}

func TestBackupArchiveToBusyMessageIsRetried(t *testing.T) {
	assert.False(t, isSQLiteBusy(nil))
	assert.True(t, isSQLiteBusy(
		assert.AnError))
	// A busy-looking error is detected by text so both drivers work.
	assert.True(t, isSQLiteBusy(
		wrapForTest("database is locked")))
	assert.False(t, isSQLiteBusy(wrapForTest("no such table")))
}

type wrappedError struct{ msg string }

func (w wrappedError) Error() string { return w.msg }

func wrapForTest(msg string) error { return wrappedError{msg: msg} }
