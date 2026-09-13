package db

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// backupBusyRetries bounds the wait when the daemon holds the writer
// for a maintenance pass while a backup is being taken.
const backupBusyRetries = 3

// BackupArchiveTo produces a single-file snapshot of the archive at
// destPath using SQLite's VACUUM INTO. The source is opened through a
// dedicated read-only connection, so the snapshot is consistent under
// WAL mode and never blocks (or requires) the daemon's writer.
func BackupArchiveTo(ctx context.Context, dbPath, destPath string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return fmt.Errorf("creating backup directory: %w", err)
	}

	params := url.Values{}
	params.Set("mode", "ro")
	escaped := (&url.URL{Path: dbPath}).EscapedPath()
	dsn := "file:" + escaped + "?" + params.Encode()

	src, err := sql.Open("sqlite3", dsn)
	if err != nil {
		return fmt.Errorf("opening archive read-only: %w", err)
	}
	defer src.Close()

	var lastErr error
	for attempt := 0; attempt < backupBusyRetries; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(time.Duration(attempt) * 500 * time.Millisecond):
			}
		}
		_, lastErr = src.ExecContext(ctx, "VACUUM INTO ?", destPath)
		if lastErr == nil {
			return nil
		}
		if !isSQLiteBusy(lastErr) {
			return fmt.Errorf("vacuum into %s: %w", destPath, lastErr)
		}
	}
	return fmt.Errorf(
		"vacuum into %s: archive busy after %d attempts: %w",
		destPath, backupBusyRetries, lastErr)
}

func isSQLiteBusy(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(strings.ToLower(err.Error()), "busy") ||
		strings.Contains(strings.ToLower(err.Error()), "locked")
}
