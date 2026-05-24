package main

import (
	"fmt"
	"sort"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
)

func renderHubSessionStatus(detail hubSessionDetail, tasks []agent.Task, auth appwire.AuthStatusResponse, taskErr, authErr error, width int) string {
	var b strings.Builder
	b.WriteString("status\n")
	if detail.SessionID != "" {
		fmt.Fprintf(&b, "Session:  %s\n", detail.SessionID)
	}
	if detail.SourceLabel != "" {
		fmt.Fprintf(&b, "Source:   %s\n", detail.SourceLabel)
	}
	writeModelOrProviderLine(&b, detail.Model, detail.Profile)
	if detail.WorkingDir != "" {
		fmt.Fprintf(&b, "Dir:      %s\n", detail.WorkingDir)
	}
	fmt.Fprintf(&b, "Turns:    %d\n", detail.TurnCount)
	if detail.ContextPressure > 0 {
		fmt.Fprintf(&b, "Context:  %.0f%% used\n", detail.ContextPressure*100)
	}
	if taskErr != nil {
		fmt.Fprintf(&b, "Tasks:    unavailable: %s\n", taskErr)
	} else {
		fmt.Fprintf(&b, "Tasks:    %s\n", taskSummary(tasks))
	}
	if authErr != nil {
		fmt.Fprintf(&b, "Auth:     unavailable: %s\n", authErr)
	} else {
		fmt.Fprintf(&b, "Auth:     %s\n", authSummary(auth))
	}
	if len(detail.RecentErrors) > 0 {
		b.WriteString("Recent errors:\n")
		for _, err := range detail.RecentErrors {
			fmt.Fprintf(&b, "  %s\n", err)
		}
	}
	if detail.Diagnostics != nil {
		appendDiagnosticsSections(&b, detail.Diagnostics, width)
	}
	return strings.TrimSpace(b.String())
}

// appendDiagnosticsSections writes the tool/MCP/skill/plugin/hook/subagent/agent
// breakdown that the legacy standalone TUI showed under /status. The data is
// already on the wire (appwire.SerfDiagnostics) — this just renders it.
func appendDiagnosticsSections(b *strings.Builder, ds *appwire.SerfDiagnostics, width int) {
	if width <= 0 {
		width = 80
	}

	// Tools grouped by source: core, mcp:<server>, custom.
	var core []string
	mcp := map[string][]string{}
	var custom []string
	for _, t := range ds.Tools {
		switch {
		case t.Source == "core":
			core = append(core, t.Name)
		case strings.HasPrefix(t.Source, "mcp:"):
			srv := t.Source[len("mcp:"):]
			mcp[srv] = append(mcp[srv], t.Name)
		default:
			custom = append(custom, t.Name)
		}
	}
	fmt.Fprintf(b, "\n\nTools (%d):", len(ds.Tools))
	if len(core) > 0 {
		writeWrappedStatusList(b, "Core:", core, width)
	}
	mcpKeys := make([]string, 0, len(mcp))
	for k := range mcp {
		mcpKeys = append(mcpKeys, k)
	}
	sort.Strings(mcpKeys)
	for _, srv := range mcpKeys {
		writeWrappedStatusList(b, fmt.Sprintf("MCP [%s]:", srv), mcp[srv], width)
	}
	if len(custom) > 0 {
		writeWrappedStatusList(b, "Custom:", custom, width)
	}

	fmt.Fprintf(b, "\n\nMCP Servers (%d):", len(ds.MCP))
	for _, srv := range ds.MCP {
		fmt.Fprintf(b, "\n  %s (%d tools)", srv.Name, len(srv.Tools))
	}

	fmt.Fprintf(b, "\n\nSkills (%d):", len(ds.Skills))
	for _, skill := range ds.Skills {
		fmt.Fprintf(b, "\n  %s", skill.Name)
	}

	fmt.Fprintf(b, "\n\nPlugins (%d):", len(ds.Plugins))
	for _, p := range ds.Plugins {
		version := p.Version
		if version == "" {
			version = "?"
		}
		fmt.Fprintf(b, "\n  %s v%s (%d skills, %d agents, %d hooks)",
			p.Name, version, p.SkillCount, p.AgentCount, p.HookCount)
	}

	fmt.Fprintf(b, "\n\nHooks (%d):", len(ds.Hooks))
	if len(ds.Hooks) > 0 {
		parts := make([]string, 0, len(ds.Hooks))
		for event, count := range ds.Hooks {
			parts = append(parts, fmt.Sprintf("%s: %d", event, count))
		}
		sort.Strings(parts)
		b.WriteString("\n  " + strings.Join(parts, "  "))
	}

	fmt.Fprintf(b, "\n\nSubagents (%d):", len(ds.Subagents))
	for _, sub := range ds.Subagents {
		fmt.Fprintf(b, "\n  %s (%s, %d turns)", sub.ID, sub.Status, sub.TurnsUsed)
	}

	fmt.Fprintf(b, "\n\nAgents (%d):", len(ds.Agents))
	for _, name := range ds.Agents {
		fmt.Fprintf(b, "\n  %s", name)
	}
}

// writeWrappedStatusList writes a labeled comma-separated list that wraps at
// width, matching the legacy standalone TUI's status layout. Continuation
// lines are indented to align with the first item after the label.
func writeWrappedStatusList(b *strings.Builder, label string, items []string, width int) {
	prefix := "  " + label + " "
	indent := strings.Repeat(" ", len(prefix))

	b.WriteString("\n" + prefix)
	col := len(prefix)
	for i, item := range items {
		entry := item
		if i < len(items)-1 {
			entry += ","
		}
		needed := len(entry)
		if i > 0 {
			needed++ // space before item
		}
		if col+needed > width && col > len(prefix) {
			b.WriteString("\n" + indent)
			col = len(indent)
		} else if i > 0 {
			b.WriteString(" ")
			col++
		}
		b.WriteString(entry)
		col += len(entry)
	}
}

func taskSummary(tasks []agent.Task) string {
	if len(tasks) == 0 {
		return "0/0 done"
	}
	done := 0
	active := 0
	for _, task := range tasks {
		switch task.Status {
		case agent.TaskDone, agent.TaskCancelled:
			done++
		case agent.TaskInProgress:
			active++
		}
	}
	summary := fmt.Sprintf("%d/%d done", done, len(tasks))
	if active > 0 {
		summary += fmt.Sprintf(", %d active", active)
	}
	return summary
}

func authSummary(auth appwire.AuthStatusResponse) string {
	provider := strings.TrimSpace(auth.Provider)
	if provider == "" {
		provider = "auth"
	}
	if !auth.Supported {
		return provider + " not supported"
	}
	if !auth.SignedIn {
		return provider + " signed out"
	}
	source := strings.TrimSpace(auth.ActiveSource)
	if source == "" {
		source = "signed in"
	}
	account := strings.TrimSpace(auth.Email)
	if account == "" {
		account = strings.TrimSpace(auth.StoredEmail)
	}
	if account == "" {
		return provider + " " + source
	}
	return provider + " " + source + " " + account
}

func authProviderForStatus(detail hubSessionDetail) string {
	if provider := strings.TrimSpace(detail.Profile); provider != "" {
		return provider
	}
	return "openai"
}

func hubErrorReason(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if _, reason, ok := strings.Cut(text, ": "); ok && strings.HasPrefix(text, "appwire ") {
		return reason
	}
	return text
}
