package db

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"go.kenn.io/agentsview/internal/export"
)

// DefaultRollupRecomputeDays is how many trailing days
// RecomputeDailyUsageRollup rebuilds when the caller does not pin
// a window. Sync runs every ~15 min, so a 7-day trailing window
// covers a full rotation of "today plus recent history" while
// still keeping the recompute time bounded regardless of how big
// the archive is. Older days are considered frozen and are not
// re-scanned; if they need to be rebuilt (pricing change, parser
// backfill), call RecomputeDailyUsageRollupSince directly.
const DefaultRollupRecomputeDays = 7

// RecomputeDailyUsageRollup rebuilds the daily_usage_rollup
// table for the trailing `days` days ending today (UTC).
// The function runs the same UNION ALL aggregation that
// GetDailyUsage uses, aggregates per (day, agent, project,
// model), then DELETEs and re-INSERTs the affected day range
// inside one transaction.
//
// The rollup stores raw token counts only. Cost is intentionally
// omitted so pricing catalog changes do not require rebuilding
// the rollup — cost is a pure function of tokens × current rates,
// which is cheap at read time over an O(days × models) table.
func (db *DB) RecomputeDailyUsageRollup(
	ctx context.Context, days int,
) error {
	if days <= 0 {
		days = DefaultRollupRecomputeDays
	}
	since := time.Now().UTC().AddDate(0, 0, -days).
		Format("2006-01-02")
	return db.RecomputeDailyUsageRollupSince(ctx, since)
}

// rollupBucket accumulates token counts for one
// (day, agent, project, model) key.
type rollupBucket struct {
	inputTok     int
	outputTok    int
	cacheCr      int
	cacheRd      int
	messageCount int
}

// rollupKey is the composite grouping key. Kept file-local so
// it does not conflict with the accumKey inside GetDailyUsage.
type rollupKey struct {
	day     string
	agent   string
	project string
	model   string
}

// RecomputeDailyUsageRollupSince recomputes rollup rows for
// every day on or after `since` (YYYY-MM-DD, UTC). Days before
// `since` are left as-is.
//
// The aggregation intentionally mirrors GetDailyUsage's dedup
// rules by piggy-backing on UsageFilter{From: since}. That is
// the only source of truth for what counts as a distinct
// message across message / usage_event / cursor branches.
func (db *DB) RecomputeDailyUsageRollupSince(
	ctx context.Context, since string,
) error {
	f := UsageFilter{
		From: since,
		// Empty To means "no upper bound".
		To: "",
		// SkipSessionCounts avoids building the seenSessions map;
		// the rollup does not need it.
		SkipSessionCounts: true,
		// Breakdowns forces the (date, project, agent, model)
		// accumulator, which is exactly the rollup grain. Without
		// it we would only get (date, model) and lose project/agent.
		Breakdowns: true,
		// Timezone empty => UTC via localDate() default is Local;
		// for rollup we want deterministic UTC day boundaries so
		// bucket keys are stable across machines. See loc below.
		Timezone: "UTC",
	}

	buckets, err := db.aggregateRollupBuckets(ctx, f)
	if err != nil {
		return fmt.Errorf("aggregating rollup buckets: %w", err)
	}

	return db.writeRollupWindow(ctx, since, buckets)
}

// aggregateRollupBuckets runs the same UNION ALL scan as
// GetDailyUsage, but writes into a rollupKey-indexed map with
// no cost math. Reusing the SQL means the dedup and per-branch
// token-source rules stay in exactly one place.
func (db *DB) aggregateRollupBuckets(
	ctx context.Context, f UsageFilter,
) (map[rollupKey]*rollupBucket, error) {
	loc := f.location()

	query, args := dailyUsageRowsSQLForBounds(
		f, usageBoundsForFilter(f), db.hasCursorUsageTable(),
	)
	query = dailyUsageRowSelectFromRows(query)
	query += ` ORDER BY u.ts ASC, u.session_id ASC,
		COALESCE(u.message_ordinal, -1) ASC`

	rows, err := db.getReader().QueryContext(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("querying rollup source: %w", err)
	}
	defer rows.Close()

	// No pricing needed: the rollup stores tokens only.
	rateResolver := export.NewPricingResolver(nil)

	buckets := make(map[rollupKey]*rollupBucket)
	seen := make(map[usageDedupToken]struct{})

	for rows.Next() {
		r, scanErr := scanDailyUsageRow(rows)
		if scanErr != nil {
			return nil, fmt.Errorf(
				"scanning rollup row: %w", scanErr,
			)
		}

		date := localDate(r.ts, loc)
		if f.From != "" && date < f.From {
			continue
		}

		if key, ok := usageDedupTokenForRow(
			r.usageSource, r.agent, r.claudeMessageID,
			r.claudeRequestID, r.sourceUUID, r.usageDedupKey,
		); ok {
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
		}

		inputTok, outputTok, cacheCrTok, cacheRdTok, _, _, priceErr :=
			dailyUsageAmounts(r, rateResolver)
		if priceErr != nil {
			return nil, fmt.Errorf("pricing rollup row: %w", priceErr)
		}

		k := rollupKey{
			day:     date,
			agent:   r.agent,
			project: r.project,
			model:   r.model,
		}
		b, ok := buckets[k]
		if !ok {
			b = &rollupBucket{}
			buckets[k] = b
		}
		b.inputTok += inputTok
		b.outputTok += outputTok
		b.cacheCr += cacheCrTok
		b.cacheRd += cacheRdTok
		b.messageCount++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterating rollup rows: %w", err,
		)
	}
	return buckets, nil
}

// writeRollupWindow replaces the rollup rows for days on or
// after `since` with the freshly aggregated buckets.
//
// The DELETE + INSERT pattern (rather than per-key UPSERT)
// guarantees that a (day, agent, project, model) tuple that
// used to have rows but no longer does — for example a session
// that was excluded via excluded_sessions after the previous
// rollup ran — is dropped from the table.
func (db *DB) writeRollupWindow(
	ctx context.Context, since string,
	buckets map[rollupKey]*rollupBucket,
) error {
	db.mu.Lock()
	defer db.mu.Unlock()
	w := db.getWriter()

	tx, err := w.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin rollup tx: %w", err)
	}
	defer tx.Rollback()

	if _, err := tx.ExecContext(
		ctx,
		`DELETE FROM daily_usage_rollup WHERE day >= ?`,
		since,
	); err != nil {
		return fmt.Errorf("clearing rollup window: %w", err)
	}

	if len(buckets) > 0 {
		stmt, err := tx.PrepareContext(
			ctx,
			`INSERT INTO daily_usage_rollup
			 (day, agent, project, model,
			  input_tokens, output_tokens,
			  cache_creation_tokens, cache_read_tokens,
			  reasoning_tokens, message_count, updated_at)
			 VALUES
			 (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		)
		if err != nil {
			return fmt.Errorf("prepare rollup insert: %w", err)
		}
		defer stmt.Close()

		now := time.Now().UTC().Format(time.RFC3339)
		for k, b := range buckets {
			if _, err := stmt.ExecContext(
				ctx,
				k.day, k.agent, k.project, k.model,
				b.inputTok, b.outputTok,
				b.cacheCr, b.cacheRd,
				0, b.messageCount, now,
			); err != nil {
				return fmt.Errorf(
					"insert rollup row (%s/%s/%s/%s): %w",
					k.day, k.agent, k.project, k.model, err,
				)
			}
		}
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit rollup tx: %w", err)
	}
	return nil
}

// DailyUsageRollupRow is one persisted rollup entry, exposed
// for tests and callers that want to inspect the table directly.
type DailyUsageRollupRow struct {
	Day                 string
	Agent               string
	Project             string
	Model               string
	InputTokens         int
	OutputTokens        int
	CacheCreationTokens int
	CacheReadTokens     int
	ReasoningTokens     int
	MessageCount        int
	UpdatedAt           string
}

// ReadDailyUsageRollup returns every rollup row on or after
// `since` (YYYY-MM-DD). Used by tests and by the future read
// path in GetDailyUsage.
func (db *DB) ReadDailyUsageRollup(
	ctx context.Context, since string,
) ([]DailyUsageRollupRow, error) {
	rows, err := db.getReader().QueryContext(
		ctx,
		`SELECT day, agent, project, model,
		        input_tokens, output_tokens,
		        cache_creation_tokens, cache_read_tokens,
		        reasoning_tokens, message_count, updated_at
		 FROM daily_usage_rollup
		 WHERE day >= ?
		 ORDER BY day ASC, agent ASC, project ASC, model ASC`,
		since,
	)
	if err != nil {
		return nil, fmt.Errorf("querying rollup: %w", err)
	}
	defer rows.Close()

	var out []DailyUsageRollupRow
	for rows.Next() {
		var r DailyUsageRollupRow
		if err := rows.Scan(
			&r.Day, &r.Agent, &r.Project, &r.Model,
			&r.InputTokens, &r.OutputTokens,
			&r.CacheCreationTokens, &r.CacheReadTokens,
			&r.ReasoningTokens, &r.MessageCount, &r.UpdatedAt,
		); err != nil {
			return nil, fmt.Errorf(
				"scan rollup row: %w", err,
			)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf(
			"iterating rollup: %w", err,
		)
	}
	return out, nil
}

// Enforce that sql.ErrNoRows stays reachable through this file;
// keeps the import from being flagged when the package's other
// files are the only current users of sql.
var _ = sql.ErrNoRows
