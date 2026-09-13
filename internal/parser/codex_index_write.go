package parser

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// AppendCodexThreadName records a thread title for a Codex session in
// the home's session_index.jsonl. Codex treats later entries as the
// truth (ParseCodexSessionIndexTitles keeps the last write per id), so
// appending one line updates the title without rewriting the file.
// The call is best-effort by design: modern Codex releases stopped
// maintaining the index, in which case the function reports the index
// as missing and callers surface that to the user.
func AppendCodexThreadName(sessionPath, sessionID, title string) error {
	title = strings.TrimSpace(title)
	if title == "" {
		return fmt.Errorf("codex: refusing to write an empty thread title")
	}
	indexPath := CodexSessionIndexPath(sessionPath)
	if _, err := os.Stat(indexPath); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf(
				"codex no longer maintains session_index.jsonl for this session; write-back unavailable")
		}
		return fmt.Errorf("codex: locating session index: %w", err)
	}

	line, err := json.Marshal(struct {
		ID         string `json:"id"`
		ThreadName string `json:"thread_name"`
	}{ID: sessionID, ThreadName: title})
	if err != nil {
		return err
	}
	f, err := os.OpenFile(indexPath, os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("codex: opening session index: %w", err)
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		f.Close()
		return fmt.Errorf("codex: appending thread name: %w", err)
	}
	if err := f.Close(); err != nil {
		return err
	}
	EvictCodexSessionIndexForSession(sessionPath)
	return nil
}
