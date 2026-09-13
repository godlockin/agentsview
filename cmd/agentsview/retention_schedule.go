package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"

	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/trash"
)

// retentionStateInterval is the minimum spacing between two wet or
// dry retention runs; a daemon restart inside the window will not
// re-run the schedule.
const retentionStateInterval = 20 * time.Hour

// retentionResult summarizes one scheduler tick for the state file.
type retentionResult struct {
	DryRun      bool      `json:"dry_run"`
	WouldPrune  int       `json:"would_prune"`
	WouldTrash  int       `json:"would_trash"`
	Pruned      int       `json:"pruned"`
	Trashed     int       `json:"trashed"`
	Unsupported int       `json:"unsupported"`
	LastRun     time.Time `json:"last_run"`
}

// startRetention launches the daily prune scheduler when the
// [retention] config enables it. Invalid or disabled configurations
// only log: retention must never block server startup.
func startRetention(
	ctx context.Context,
	cfg config.Config,
	database *db.DB,
	runner pricingRefreshExclusiveRunner,
) {
	rc := cfg.Retention
	if !rc.Enabled {
		return
	}
	if err := rc.Validate(); err != nil {
		log.Printf("retention disabled: %v", err)
		return
	}
	go func() {
		retentionRunOnce(ctx, cfg, database, runner)
		ticker := time.NewTicker(24 * time.Hour)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				retentionRunOnce(ctx, cfg, database, runner)
			}
		}
	}()
}

func retentionRunOnce(
	ctx context.Context,
	cfg config.Config,
	database *db.DB,
	runner pricingRefreshExclusiveRunner,
) {
	statePath := filepath.Join(cfg.DataDir, "retention-state.json")
	if last, ok := readRetentionState(statePath); ok {
		if since := time.Since(last); since < retentionStateInterval {
			log.Printf("retention: skipping run; last run %.0f min ago",
				since.Minutes())
			return
		}
	}

	_, err := runRetentionTick(ctx, cfg, database, runner, time.Now(), os.Stdout)
	if err != nil {
		log.Printf("retention: %v", err)
	}
}

// runRetentionTick prunes (or, in the default dry-run mode, only
// counts) sessions older than the configured age. When a sync runner
// is available the write happens under the engine's exclusive lock so
// background syncs never interleave with the prune transaction.
func runRetentionTick(
	ctx context.Context,
	cfg config.Config,
	database *db.DB,
	runner pricingRefreshExclusiveRunner,
	now time.Time,
	out io.Writer,
) (retentionResult, error) {
	rc := cfg.Retention
	days, err := parseAgeDuration(rc.OlderThan)
	if err != nil {
		return retentionResult{}, err
	}
	before := now.AddDate(0, 0, -days).Format("2006-01-02")

	candidates, err := database.FindPruneCandidates(db.PruneFilter{
		Before: before,
	})
	if err != nil {
		return retentionResult{}, fmt.Errorf("finding retention candidates: %w", err)
	}

	dry := rc.EffectiveDryRun()
	result := retentionResult{DryRun: dry, LastRun: now.UTC()}

	if dry {
		for _, s := range candidates {
			if s.FilePath != nil && *s.FilePath != "" {
				result.WouldTrash++
			}
		}
		result.WouldPrune = len(candidates)
		fmt.Fprintf(out,
			"retention dry-run: %d sessions and %d source files are"+
				" older than %s; set [retention] dry_run = false to enable\n",
			result.WouldPrune, result.WouldTrash, before)
		return result, nil
	}

	apply := func() error {
		ids := make([]string, len(candidates))
		for i, s := range candidates {
			ids[i] = s.ID
		}
		if len(ids) > 0 {
			pruned, err := database.DeleteSessions(ids)
			if err != nil {
				return fmt.Errorf("deleting sessions: %w", err)
			}
			result.Pruned = pruned
		}
		pruner := &Pruner{
			DB:    database,
			Out:   out,
			Trash: trash.New(cfg.DataDir),
		}
		result.Trashed, _, result.Unsupported, _ =
			pruner.trashSources(candidates)
		return nil
	}
	if runner != nil {
		err = runner.RunExclusive(apply)
	} else {
		err = apply()
	}
	if err != nil {
		return result, err
	}
	fmt.Fprintf(out,
		"retention: pruned %d sessions and trashed %d source files"+
			" (%d report-only) older than %s\n",
		result.Pruned, result.Trashed, result.Unsupported, before)
	writeRetentionState(filepath.Join(cfg.DataDir, "retention-state.json"), result)
	return result, nil
}

func readRetentionState(path string) (time.Time, bool) {
	payload, err := os.ReadFile(path)
	if err != nil {
		return time.Time{}, false
	}
	var state struct {
		LastRun time.Time `json:"last_run"`
	}
	if json.Unmarshal(payload, &state) != nil || state.LastRun.IsZero() {
		return time.Time{}, false
	}
	return state.LastRun, true
}

func writeRetentionState(path string, result retentionResult) {
	payload, err := json.MarshalIndent(struct {
		LastRun time.Time       `json:"last_run"`
		Result  retentionResult `json:"result"`
	}{LastRun: result.LastRun, Result: result}, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(path, payload, 0o644)
}
