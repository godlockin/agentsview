package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"go.kenn.io/agentsview/internal/db"
	"go.kenn.io/agentsview/internal/service"
)

// continueConfig holds parsed options for the continue command.
type continueConfig struct {
	SessionID     string
	Last          int
	MaxChars      int
	IncludeRecall bool
	Out           string
}

// defaultContinue returns the config defaults.
func defaultContinue() continueConfig {
	return continueConfig{
		Last:          10,
		MaxChars:      400,
		IncludeRecall: true,
	}
}

func newContinueCommand() *cobra.Command {
	cfg := defaultContinue()
	cmd := &cobra.Command{
		Use:          "continue [session-id]",
		Short:        "Write a handoff briefing for the last (or a chosen) session",
		GroupID:      groupCore,
		SilenceUsage: true,
		Args:         cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 {
				cfg.SessionID = args[0]
			}
			return runContinue(cmd, cfg)
		},
	}
	cmd.Flags().String("server", "", "Remote daemon URL")
	cmd.Flags().String("server-token-file", "",
		"File containing bearer token for explicit --server requests")
	cmd.Flags().Bool("pg", false, "Read session data from configured PostgreSQL")
	cmd.Flags().IntVar(&cfg.Last, "last", cfg.Last,
		"How many trailing user/assistant messages to include")
	cmd.Flags().IntVar(&cfg.MaxChars, "max-chars", cfg.MaxChars,
		"Per-message character cap before truncation")
	cmd.Flags().BoolVar(&cfg.IncludeRecall, "recall", cfg.IncludeRecall,
		"Embed same-project recall entries in the briefing")
	cmd.Flags().StringVar(&cfg.Out, "out", cfg.Out,
		"Write the briefing to this file instead of stdout")
	return cmd
}

// runContinue assembles the handoff document and prints or writes it.
func runContinue(cmd *cobra.Command, cfg continueConfig) error {
	svc, cleanup, err := resolveService(cmd)
	if err != nil {
		return err
	}
	defer cleanup()

	ctx := context.Background()
	session, err := pickContinueSession(ctx, svc, cfg.SessionID)
	if err != nil {
		return err
	}

	msgs, recallContext, err := gatherContinueData(ctx, svc, session, cfg)
	if err != nil {
		return err
	}

	document := renderHandoff(session, msgs, recallContext)
	if cfg.Out == "" {
		fmt.Print(document)
		return nil
	}
	return os.WriteFile(cfg.Out, []byte(document), 0o644)
}

// pickContinueSession resolves an explicit session id, or selects the
// most recent session overall (preferring one active in the last 15
// minutes).
func pickContinueSession(
	ctx context.Context, svc service.SessionService, sessionID string,
) (*db.Session, error) {
	if sessionID != "" {
		detail, err := svc.Get(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("loading session %s: %w", sessionID, err)
		}
		if detail == nil {
			return nil, fmt.Errorf("session %s not found", sessionID)
		}
		session := detail.Session
		return &session, nil
	}

	activeSince := time.Now().Add(-resumeActiveWindow).
		UTC().Format(time.RFC3339)
	list, err := svc.List(ctx, service.ListFilter{
		ActiveSince: activeSince,
		Limit:       1,
	})
	if err != nil {
		return nil, fmt.Errorf("listing recent sessions: %w", err)
	}
	if len(list.Sessions) > 0 {
		session := list.Sessions[0]
		return &session, nil
	}

	// Nothing active in the resume window; fall back to the most
	// recent session overall and say so.
	list, err = svc.List(ctx, service.ListFilter{Limit: 1})
	if err != nil {
		return nil, fmt.Errorf("listing sessions: %w", err)
	}
	if len(list.Sessions) == 0 {
		return nil, fmt.Errorf("no sessions found; run \"agentsview sync\" first")
	}
	session := list.Sessions[0]
	fmt.Fprintf(os.Stderr,
		"note: no session active in the last %s; using the most"+
			" recent session (%s)\n",
		resumeActiveWindow, session.ID)
	return &session, nil
}

// gatherContinueData loads the trailing message window and, when
// enabled, the same-project recall context. Recall failures are
// downgraded to a stderr note: the briefing is still useful without it.
func gatherContinueData(
	ctx context.Context,
	svc service.SessionService,
	session *db.Session,
	cfg continueConfig,
) ([]db.Message, string, error) {
	msgs, err := gatherContinueMessages(ctx, svc, session, cfg.Last)
	if err != nil {
		return nil, "", err
	}
	if !cfg.IncludeRecall {
		return msgs, "", nil
	}
	recallContext, err := gatherRecallContext(ctx, svc, session)
	if err != nil {
		fmt.Fprintf(os.Stderr,
			"note: recall context unavailable (%v); continuing without it\n",
			err)
	}
	return msgs, recallContext, nil
}

func gatherContinueMessages(
	ctx context.Context,
	svc service.SessionService,
	session *db.Session,
	last int,
) ([]db.Message, error) {
	list, err := svc.Messages(ctx, session.ID, service.MessageFilter{
		Direction: "desc",
		Limit:     last,
		Roles:     []string{"user", "assistant"},
	})
	if err != nil {
		return nil, fmt.Errorf("loading messages: %w", err)
	}
	// Reverse the descending window so the briefing reads forward.
	for i, j := 0, len(list.Messages)-1; i < j; i, j = i+1, j-1 {
		list.Messages[i], list.Messages[j] = list.Messages[j], list.Messages[i]
	}
	return list.Messages, nil
}

// gatherRecallContext reuses the recall brief pipeline scoped to the
// session's project so the briefing carries prior learned context.
func gatherRecallContext(
	ctx context.Context,
	svc service.SessionService,
	session *db.Session,
) (string, error) {
	result, err := svc.QueryRecallEntries(ctx, service.RecallQuery{
		Query:          session.Project,
		Surface:        "brief",
		Project:        session.Project,
		CWD:            session.Cwd,
		GitBranch:      session.GitBranch,
		IncludeContext: true,
		SkipRecording:  true,
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(result.Context), nil
}
