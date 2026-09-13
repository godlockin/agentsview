package main

import (
	"fmt"
	"strings"

	"go.kenn.io/agentsview/internal/db"
)

// defaultHandoffMaxChars mirrors the CLI default; kept here so the
// renderer and the flag stay in sync through one constant.
const defaultHandoffMaxChars = 400

// renderHandoff assembles the Markdown handoff document. It is a pure
// function over the session, its trailing messages, and the optional
// recall context so it can be table-tested.
func renderHandoff(
	session *db.Session,
	msgs []db.Message,
	recallContext string,
) string {
	var b strings.Builder

	b.WriteString("# Session handoff\n\n")
	b.WriteString(fmt.Sprintf("- Session: `%s`\n", session.ID))
	b.WriteString(fmt.Sprintf("- Agent: %s\n", agentLabel(session.Agent)))
	b.WriteString(fmt.Sprintf("- Project: %s\n", projectLabel(session.Project)))
	if session.Cwd != "" {
		b.WriteString(fmt.Sprintf("- Working directory: %s\n", session.Cwd))
	}
	if session.GitBranch != "" {
		b.WriteString(fmt.Sprintf("- Git branch: %s\n", session.GitBranch))
	}
	if session.StartedAt != nil && *session.StartedAt != "" {
		b.WriteString(fmt.Sprintf("- Started: %s\n", *session.StartedAt))
	}
	if session.EndedAt != nil && *session.EndedAt != "" {
		b.WriteString(fmt.Sprintf("- Last activity: %s\n", *session.EndedAt))
	}
	b.WriteString(fmt.Sprintf("- Messages: %d\n", session.MessageCount))
	if session.Outcome != "" {
		b.WriteString(fmt.Sprintf("- Outcome: %s\n", session.Outcome))
	}
	if session.HealthScore != nil {
		b.WriteString(fmt.Sprintf("- Health: %d/100\n", *session.HealthScore))
	}

	b.WriteString("\n## Where things stand\n\n")
	b.WriteString(terminationSummary(derefString(session.TerminationStatus)))
	b.WriteString("\n")

	if pending := pendingToolNames(msgs); len(pending) > 0 {
		b.WriteString("\nTool calls that may not have returned:\n")
		for _, name := range pending {
			b.WriteString(fmt.Sprintf("- %s\n", name))
		}
	}

	b.WriteString("\n## Last messages\n\n")
	b.WriteString(renderConversation(msgs, defaultHandoffMaxChars))

	if recallContext != "" {
		b.WriteString("\n## Recall context\n\n")
		b.WriteString(recallContext)
		b.WriteString("\n")
	}

	b.WriteString("\n## Continuing\n\n")
	b.WriteString(fmt.Sprintf(
		"This tool does not relaunch agents. Open session `%s` in %s"+
			" and paste this briefing as your first message.\n",
		session.ID, agentLabel(session.Agent)))
	return b.String()
}

// terminationSummary translates the parser's termination status into
// plain language for the briefing header.
func terminationSummary(status string) string {
	switch status {
	case "awaiting_user":
		return "The agent stopped while waiting for user input. " +
			"Answer its last question to move forward."
	case "clean":
		return "The session ended normally (no pending work detected)."
	case "tool_call_pending":
		return "A tool call never returned a result. The agent may " +
			"still be running, or it was interrupted mid-task."
	case "truncated":
		return "The transcript ends mid-write. The agent likely " +
			"crashed or the session file was cut short."
	case "":
		return "Termination status unknown for this session."
	default:
		return fmt.Sprintf("Termination status: %s.", status)
	}
}

// pendingToolNames collects the tool names from the trailing window's
// last assistant message that carries tool calls.
func pendingToolNames(msgs []db.Message) []string {
	for i := len(msgs) - 1; i >= 0; i-- {
		msg := msgs[i]
		if msg.Role != "assistant" || len(msg.ToolCalls) == 0 {
			continue
		}
		seen := make(map[string]struct{}, len(msg.ToolCalls))
		var names []string
		for _, call := range msg.ToolCalls {
			if call.ToolName == "" {
				continue
			}
			if _, dup := seen[call.ToolName]; dup {
				continue
			}
			seen[call.ToolName] = struct{}{}
			names = append(names, call.ToolName)
		}
		return names
	}
	return nil
}

// renderConversation formats the trailing window, truncating long
// messages at rune boundaries so multibyte text is never split.
func renderConversation(msgs []db.Message, maxChars int) string {
	if len(msgs) == 0 {
		return "(No user or assistant messages in this window.)\n"
	}
	var b strings.Builder
	for _, msg := range msgs {
		role := strings.ToUpper(msg.Role[:1]) + msg.Role[1:]
		if msg.Role == "user" {
			role = "User"
		} else if msg.Role == "assistant" {
			role = "Assistant"
		}
		b.WriteString(fmt.Sprintf("### %s", role))
		if msg.Timestamp != "" {
			b.WriteString(fmt.Sprintf(" (%s)", msg.Timestamp))
		}
		b.WriteString("\n\n")
		if cut, truncated := truncateRunes(msg.Content, maxChars); truncated {
			b.WriteString(strings.TrimSpace(cut))
			b.WriteString("\n\n[truncated]\n\n")
		} else {
			b.WriteString(strings.TrimSpace(cut))
			b.WriteString("\n\n")
		}
	}
	return b.String()
}

// trimmedOrEmpty marks blank content in briefings.
func trimmedOrEmpty(text string) string {
	text = strings.TrimSpace(text)
	if text == "" {
		return "(empty)"
	}
	return text
}

// derefString reads an optional string column safely.
func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// agentLabel prefers a friendly name over the raw agent key.
func agentLabel(agent string) string {
	if agent == "" {
		return "the original agent"
	}
	return agent
}

func projectLabel(project string) string {
	if project == "" {
		return "(unknown)"
	}
	return project
}
