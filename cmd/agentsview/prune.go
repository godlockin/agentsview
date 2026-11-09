package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"go.kenn.io/agentsview/internal/config"
	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/parser"
	"go.kenn.io/agentsview/internal/sourcelayout"
	"go.kenn.io/agentsview/internal/trash"
)

// PruneConfig holds parsed CLI options for the prune command.
type PruneConfig struct {
	Filter db.PruneFilter
	DryRun bool
	Yes    bool
	// Age accepts "30d"-style durations and resolves to Filter.Before
	// when Before is empty. Empty means no age filter.
	Age        string
	SourceOnly bool
}

// resolvePruneBefore converts --age into Filter.Before so every flag
// path (the cobra command and parsePruneFlags alike) shares one set
// of filter semantics. It rejects combining --age with --before.
func resolvePruneBefore(cfg PruneConfig) (PruneConfig, error) {
	if cfg.Age != "" && cfg.Filter.Before != "" {
		return PruneConfig{}, fmt.Errorf("--age and --before are mutually exclusive")
	}
	if cfg.Age != "" {
		days, err := parseAgeDuration(cfg.Age)
		if err != nil {
			return PruneConfig{}, err
		}
		cfg.Filter.Before = time.Now().
			AddDate(0, 0, -days).
			Format("2006-01-02")
	}
	return cfg, nil
}

// Pruner executes the prune workflow against a database.
type Pruner struct {
	DB    *db.DB
	Out   io.Writer
	In    io.Reader
	Trash *trash.Store
}

// Prune finds matching sessions and deletes them.
func (p *Pruner) Prune(ctx context.Context, cfg PruneConfig) error {
	if !cfg.Filter.HasFilters() {
		return errors.New("at least one filter is required " +
			"(refusing to prune all sessions)",
		)
	}

	candidates, err := p.DB.FindPruneCandidates(ctx, cfg.Filter)
	if err != nil {
		return fmt.Errorf("finding candidates: %w", err)
	}

	if len(candidates) == 0 {
		fmt.Fprintln(p.Out,
			"No sessions match the given filters.")
		return nil
	}

	filesKept := writeSummary(p.Out, candidates)

	if cfg.DryRun {
		fmt.Fprintln(p.Out, "\nDry run: no changes made.")
		return nil
	}

	if !cfg.Yes {
		msg := fmt.Sprintf(
			"\nDelete %d sessions?", len(candidates),
		)
		if !confirm(p.In, p.Out, msg) {
			fmt.Fprintln(p.Out, "Aborted.")
			return nil
		}
	}

	ids := make([]string, len(candidates))
	for i, s := range candidates {
		ids[i] = s.ID
	}

	if p.Trash == nil {
		return fmt.Errorf("prune: trash store not configured")
	}

	deleted := 0
	if !cfg.SourceOnly {
		var err error
		deleted, err = p.DB.DeleteSessions(ctx, ids)
		if err != nil {
			return fmt.Errorf("deleting sessions: %w", err)
		}
	}

	trashed, skipped, unsupported, bytesReclaimed := p.trashSources(candidates)

	if cfg.SourceOnly {
		fmt.Fprintf(p.Out,
			"\nTrashed %d source files (%d skipped, %d report-only;"+
				" %s reclaimed); archive rows kept\n",
			trashed, skipped, unsupported, formatBytes(bytesReclaimed),
		)
		fmt.Fprintln(p.Out,
			"Run \"agentsview prune restore\" to undo; archived"+
				" rows will re-import on the next sync.")
		return nil
	}

	fmt.Fprintf(p.Out,
		"\nDeleted %d sessions, trashed %d source files"+
			" (%d skipped, %d report-only; %s reclaimed)\n",
		deleted, trashed, skipped, unsupported, formatBytes(bytesReclaimed),
	)
	if filesKept > 0 {
		writeKeptFiles(p.Out, "left", filesKept)
	}
	fmt.Fprintln(p.Out)
	fmt.Fprintln(p.Out,
		"Source files moved to the trash;"+
			" run \"agentsview prune restore\" to undo.")
	return nil
}

// trashSources moves every candidate's source files to the trash via
// the per-agent layout policy. Sessions whose layout is app-owned are
// counted as unsupported and left alone; missing files are skipped.
// Failures are logged so one bad path cannot abort the batch.
func (p *Pruner) trashSources(
	sessions []db.Session,
) (trashed, skipped, unsupported int, reclaimed int64) {
	var paths []string
	var metas []trash.Meta
	for i := range sessions {
		s := sessions[i]
		decision := sourcelayout.For(s.Agent).Decide(s)
		if !decision.Deletable {
			if decision.Reason == sourcelayout.ReportOnlyReason {
				unsupported++
			} else {
				skipped++
			}
			continue
		}
		for j, path := range decision.Paths {
			paths = append(paths, path)
			meta := trash.Meta{}
			if j == 0 {
				meta = trash.Meta{SessionID: s.ID, Agent: s.Agent}
			}
			metas = append(metas, meta)
		}
	}
	if len(paths) == 0 {
		return 0, skipped, unsupported, 0
	}
	items, err := p.Trash.Trash(paths, metas)
	if err != nil {
		log.Printf("warning: some source files were not trashed: %v", err)
	}
	trashed = len(items)
	skipped += len(paths) - trashed
	for _, item := range items {
		reclaimed += item.Size
	}
	return trashed, skipped, unsupported, reclaimed
}

func confirm(r io.Reader, w io.Writer, msg string) bool {
	return confirmScanned(bufio.NewScanner(r), w, msg)
}

func confirmScanned(scanner *bufio.Scanner, w io.Writer, msg string) bool {
	fmt.Fprintf(w, "%s [y/N] ", msg)
	scanner.Scan()
	ans := strings.ToLower(strings.TrimSpace(scanner.Text()))
	return ans == "y" || ans == "yes"
}

func writeSummary(w io.Writer, sessions []db.Session) int {
	var totalSize int64
	trashed := 0
	byProject := map[string]int{}
	var projects []string
	for _, s := range sessions {
		if byProject[s.Project] == 0 {
			projects = append(projects, s.Project)
		}
		byProject[s.Project]++
		if s.DeletedAt != nil {
			trashed++
		}
		if s.FilePath != nil && parser.OwnsAppStore(parser.AgentType(s.Agent), *s.FilePath) {
			continue
		}
		if s.FileSize != nil {
			totalSize += *s.FileSize
		}
	}

	sort.Strings(projects)

	fmt.Fprintf(w,
		"Found %d sessions (%s on disk)",
		len(sessions), formatBytes(totalSize),
	)
	if trashed > 0 {
		fmt.Fprintf(w, ", %d in trash", trashed)
	}
	kept := keptSourceFiles(sessions)
	if kept > 0 {
		writeKeptFiles(w, "will leave", kept)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "\nBy project:")
	for _, proj := range projects {
		count := byProject[proj]
		fmt.Fprintf(w, "  %-40s %d\n", proj, count)
	}
	return kept
}

func writeKeptFiles(w io.Writer, verb string, count int) {
	fmt.Fprintf(w, ", %s %d source file%s in place", verb, count, pluralSuffix(count))
}

func keptSourceFiles(sessions []db.Session) int {
	kept := map[string]struct{}{}
	for _, s := range sessions {
		if s.FilePath == nil || !parser.OwnsAppStore(parser.AgentType(s.Agent), *s.FilePath) {
			continue
		}
		path := parser.ResolveSourceFilePath(*s.FilePath)
		kept[path] = struct{}{}
	}
	count := 0
	for path := range kept {
		if _, err := os.Stat(path); err == nil {
			count++
		}
	}
	return count
}

func deleteFiles(sessions []db.Session) (int, int64) {
	removed := 0
	var reclaimed int64

	for _, s := range sessions {
		if s.FilePath == nil || parser.OwnsAppStore(parser.AgentType(s.Agent), *s.FilePath) {
			continue
		}
		path := *s.FilePath

		info, err := os.Stat(path)
		size := int64(0)
		if err == nil {
			size = info.Size()
		}

		if err := os.Remove(path); err != nil {
			if !os.IsNotExist(err) {
				log.Printf(
					"warning: removing %s: %v", path, err,
				)
			}
			continue
		}
		removed++
		reclaimed += size

		// Remove parent directory if empty (session subdirs).
		dir := filepath.Dir(path)
		entries, err := os.ReadDir(dir)
		if err == nil && len(entries) == 0 {
			_ = os.Remove(dir)
		}
	}
	return removed, reclaimed
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func runPrune(ctx context.Context, cfg PruneConfig) {
	if cfg.Filter.MaxMessages != nil && *cfg.Filter.MaxMessages < 0 {
		fatal("max-messages must be >= 0")
	}
	if !cfg.Filter.HasFilters() {
		fatal("at least one filter is required\nuse --agent, --project, --max-messages, --before, or --first-message")
	}

	appCfg, err := config.LoadMinimalFor(config.ScopeLocal)
	if err != nil {
		fatal("loading config: %v", err)
	}

	database, writeLock, err := openWriteDB(ctx, appCfg)
	if err != nil {
		fatal("opening database: %v", err)
	}

	pruner := &Pruner{
		DB:    database,
		Out:   os.Stdout,
		In:    os.Stdin,
		Trash: trash.New(appCfg.DataDir),
	}
	err = pruner.Prune(ctx, cfg)
	closeWriteDB(database, writeLock)
	if err != nil {
		fatal("prune: %v", err)
	}
}
