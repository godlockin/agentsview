package config

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const telemetryScreensFilename = "telemetry-screen-views"

// ClaimScreenView serializes enqueueing and persists only accepted events.
// When the send fails after the lock is held, the caller may retry with the
// same claimer: the failed screen is not treated as already recorded, so the
// retry sends again instead of being deduped.
func (c *Config) ClaimScreenView(screen string, send func() error) (day string, claimed bool, err error) {
	sendAttempted := false
	err = c.withConfigLock(func() error {
		day = time.Now().UTC().Format(time.DateOnly)
		data, err := os.ReadFile(filepath.Join(c.DataDir, telemetryScreensFilename))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		fields := strings.Fields(string(data))
		if len(fields) < 2 || fields[0] != c.InstallationID || fields[1] != day {
			fields = []string{c.InstallationID, day}
		}
		if slices.Contains(fields[2:], screen) {
			return nil
		}
		sendAttempted = true
		if err := send(); err != nil {
			return err
		}
		claimed = true
		if err := c.writeInstallationFile(telemetryScreensFilename, strings.Join(append(fields, screen), " ")); err != nil {
			return err
		}
		return nil
	})
	if err != nil && !sendAttempted {
		// The lock itself could not be taken: send without it so a
		// transient lock failure never drops an event.
		day = time.Now().UTC().Format(time.DateOnly)
		if sendErr := send(); sendErr != nil {
			return day, false, sendErr
		}
		return day, true, err
	}
	return day, claimed, err
}
