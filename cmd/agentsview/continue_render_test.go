package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"go.kenn.io/agentsview/internal/db"
)

func TestTerminationSummary(t *testing.T) {
	tests := []struct {
		status   string
		contains string
	}{
		{"awaiting_user", "waiting for user input"},
		{"clean", "ended normally"},
		{"tool_call_pending", "never returned a result"},
		{"truncated", "mid-write"},
		{"", "unknown"},
		{"exotic", "Termination status: exotic"},
	}
	for _, tt := range tests {
		t.Run(tt.status, func(t *testing.T) {
			assert.Contains(t, terminationSummary(tt.status), tt.contains)
		})
	}
}

func TestTrimmedOrEmpty(t *testing.T) {
	assert.Equal(t, "ok", trimmedOrEmpty("  ok "))
	assert.Equal(t, "(empty)", trimmedOrEmpty("   \n"))
}
func TestPendingToolNamesFromLastAssistant(t *testing.T) {
	msgs := []db.Message{
		{Role: "user", Content: "go"},
		{Role: "assistant", ToolCalls: []db.ToolCall{
			{ToolName: "Read"}, {ToolName: "Read"}, {ToolName: "Bash"},
		}},
		{Role: "user", Content: "earlier"}, // ignored: not the last assistant
	}
	names := pendingToolNames(msgs)
	assert.Equal(t, []string{"Read", "Bash"}, names)

	assert.Nil(t, pendingToolNames([]db.Message{
		{Role: "user", Content: "only user"},
	}))
}

func TestRenderHandoffSections(t *testing.T) {
	health := 72
	session := &db.Session{
		ID:                "abc-123",
		Agent:             "claude",
		Project:           "agentsview",
		Cwd:               "/repo/agentsview",
		GitBranch:         "main",
		MessageCount:      42,
		HealthScore:       &health,
		TerminationStatus: new("awaiting_user"),
	}
	started := "2026-09-13T10:00:00Z"
	session.StartedAt = &started
	ended := "2026-09-13T12:30:00Z"
	session.EndedAt = &ended
	session.Outcome = "success"

	msgs := []db.Message{
		{Role: "user", Content: "fix the flaky test", Timestamp: "2026-09-13T11:00:00Z"},
		{Role: "assistant", Content: strings.Repeat("long answer ", 80)},
	}

	document := renderHandoff(session, msgs, "- Keep the fixture small\n")

	assert.Contains(t, document, "# Session handoff")
	assert.Contains(t, document, "`abc-123`")
	assert.Contains(t, document, "- Agent: claude")
	assert.Contains(t, document, "- Project: agentsview")
	assert.Contains(t, document, "/repo/agentsview")
	assert.Contains(t, document, "- Git branch: main")
	assert.Contains(t, document, "- Health: 72/100")
	assert.Contains(t, document, "- Outcome: success")
	assert.Contains(t, document, "waiting for user input")
	assert.Contains(t, document, "## Last messages")
	assert.Contains(t, document, "### User")
	assert.Contains(t, document, "fix the flaky test")
	assert.Contains(t, document, "### Assistant")
	assert.Contains(t, document, "[truncated]", "over-cap message is cut")
	assert.Contains(t, document, "## Recall context")
	assert.Contains(t, document, "Keep the fixture small")
	assert.Contains(t, document, "does not relaunch agents")
	assert.Contains(t, document, "Open session `abc-123` in claude")
}

func TestRenderHandoffEmptyWindowAndPendingTools(t *testing.T) {
	session := &db.Session{ID: "x1", TerminationStatus: new("tool_call_pending")}
	msgs := []db.Message{
		{Role: "assistant", ToolCalls: []db.ToolCall{{ToolName: "Edit"}}},
	}

	document := renderHandoff(session, msgs, "")
	assert.Contains(t, document, "### Assistant")
	assert.Contains(t, document, "- Edit")
	assert.NotContains(t, document, "## Recall context",
		"empty recall context must be omitted")

	empty := renderHandoff(session, nil, "")
	assert.Contains(t, empty,
		"(No user or assistant messages in this window.)")
}

func TestRenderHandoffSessionIDRoundTrip(t *testing.T) {
	session := &db.Session{ID: "id-with-backtick-free", Agent: "codex"}
	document := renderHandoff(session, nil, "")
	// The id appears in both the header and the closing instruction.
	require.Equal(t, 2, strings.Count(document, "id-with-backtick-free"))
}
