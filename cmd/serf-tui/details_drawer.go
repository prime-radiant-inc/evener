package main

import (
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuiprim"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

type detailsDrawer struct {
	Detail hubSessionDetail
	HubURL string
}

func sectionLabel(text string) string {
	th := tuitheme.ActiveTheme()
	return lipgloss.NewStyle().Foreground(th.TextDim).Bold(true).Render(strings.ToUpper(text))
}

func ghostText(text string) string {
	th := tuitheme.ActiveTheme()
	return lipgloss.NewStyle().Foreground(th.TextGhost).Render(text)
}

func (d detailsDrawer) View() string {
	var b strings.Builder
	detail := d.Detail

	// Header: section label + state badge.
	b.WriteString(sectionLabel("details"))
	if state := strings.TrimSpace(detail.State); state != "" {
		b.WriteString("  " + tuiprim.StatusBadge(stateColor(state), state))
	}
	b.WriteString("\n")

	if detail.SessionID != "" {
		fmt.Fprintf(&b, "Session:  %s\n", detail.SessionID)
	}
	if detail.Ref != "" {
		fmt.Fprintf(&b, "Hub ref:  %s\n", detail.Ref)
	}
	if detail.SourceLabel != "" {
		fmt.Fprintf(&b, "Source:   %s\n", detail.SourceLabel)
	}
	writeModelOrProviderLine(&b, detail.Model, detail.Profile)
	if detail.WorkingDir != "" {
		fmt.Fprintf(&b, "Dir:      %s\n", detail.WorkingDir)
	}
	if detail.Branch != "" {
		fmt.Fprintf(&b, "Branch:   %s\n", detail.Branch)
	}
	if detail.TurnCount > 0 {
		fmt.Fprintf(&b, "Turns:    %s\n", ghostText(strconv.Itoa(detail.TurnCount)))
	}
	if detail.ContextPressure > 0 {
		fmt.Fprintf(&b, "Context:  %s\n", ghostText(fmt.Sprintf("%.0f%% used", detail.ContextPressure*100)))
	}
	if d.HubURL != "" && detail.Ref != "" {
		base := strings.TrimRight(d.HubURL, "/")
		escaped := url.PathEscape(detail.Ref)
		fmt.Fprintf(&b, "Web:      %s/s/%s\n", base, escaped)
	}
	if caps := capabilityList(detail.Capabilities); caps != "" {
		fmt.Fprintf(&b, "Capabilities: %s\n", caps)
	}
	if len(detail.RecentErrors) > 0 {
		b.WriteString(sectionLabel("recent errors") + "\n")
		for _, err := range detail.RecentErrors {
			fmt.Fprintf(&b, "  %s\n", err)
		}
	}
	if detail.Diagnostics == nil {
		b.WriteString(ghostText("Diagnostics: not reported by source") + "\n")
		return strings.TrimSpace(b.String())
	}
	writeSerfDiagnostics(&b, detail.Diagnostics)
	return strings.TrimSpace(b.String())
}

func modelAndProfile(model, profile string) string {
	switch {
	case model != "" && profile != "":
		return model + " (" + profile + ")"
	case model != "":
		return model
	default:
		return profile
	}
}

func writeModelOrProviderLine(b *strings.Builder, model, profile string) {
	model = strings.TrimSpace(model)
	profile = strings.TrimSpace(profile)
	switch {
	case model != "":
		fmt.Fprintf(b, "Model:    %s\n", modelAndProfile(model, profile))
	case profile != "":
		fmt.Fprintf(b, "Provider: %s\n", profile)
	}
}

func writeSerfDiagnostics(b *strings.Builder, diag *appwire.SerfDiagnostics) {
	core := []string{}
	mcpTools := map[string][]string{}
	custom := []string{}
	for _, tool := range diag.Tools {
		switch {
		case tool.Source == "core":
			core = append(core, tool.Name)
		case strings.HasPrefix(tool.Source, "mcp:"):
			mcpTools[strings.TrimPrefix(tool.Source, "mcp:")] = append(mcpTools[strings.TrimPrefix(tool.Source, "mcp:")], tool.Name)
		default:
			custom = append(custom, tool.Name)
		}
	}

	fmt.Fprintf(b, "\nTools (%d):", len(diag.Tools))
	if len(core) > 0 {
		writeWrappedList(b, "Core:", core, 80)
	}
	servers := sortedKeys(mcpTools)
	for _, server := range servers {
		writeWrappedList(b, fmt.Sprintf("MCP [%s]:", server), mcpTools[server], 80)
	}
	if len(custom) > 0 {
		writeWrappedList(b, "Custom:", custom, 80)
	}

	fmt.Fprintf(b, "\n\nMCP Servers (%d):", len(diag.MCP))
	for _, srv := range diag.MCP {
		fmt.Fprintf(b, "\n  %s (%d tools)", srv.Name, len(srv.Tools))
		if srv.Status != "" {
			fmt.Fprintf(b, " — %s", srv.Status)
		}
		if srv.Error != "" {
			fmt.Fprintf(b, " — last error: %s", srv.Error)
		}
	}

	fmt.Fprintf(b, "\n\nSkills (%d):", len(diag.Skills))
	for _, skill := range diag.Skills {
		fmt.Fprintf(b, "\n  %s", skill.Name)
	}

	fmt.Fprintf(b, "\n\nPlugins (%d):", len(diag.Plugins))
	for _, plugin := range diag.Plugins {
		version := plugin.Version
		if version == "" {
			version = "?"
		}
		fmt.Fprintf(b, "\n  %s v%s (%d skills, %d agents, %d hooks)", plugin.Name, version, plugin.SkillCount, plugin.AgentCount, plugin.HookCount)
	}

	fmt.Fprintf(b, "\n\nHooks (%d):", len(diag.Hooks))
	hooks := sortedHookEntries(diag.Hooks)
	if len(hooks) > 0 {
		fmt.Fprintf(b, "\n  %s", strings.Join(hooks, "  "))
	}

	fmt.Fprintf(b, "\n\nJobs (%d):", len(diag.Jobs))
	for _, job := range diag.Jobs {
		fmt.Fprintf(b, "\n  %s (%s, %s)", job.JobID, job.JobType, job.Status)
	}

	fmt.Fprintf(b, "\n\nAgents (%d):", len(diag.Agents))
	for _, agent := range diag.Agents {
		fmt.Fprintf(b, "\n  %s", agent)
	}
}

func sortedKeys(values map[string][]string) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedHookEntries(hooks map[string]int) []string {
	out := make([]string, 0, len(hooks))
	for event, count := range hooks {
		out = append(out, fmt.Sprintf("%s: %d", event, count))
	}
	sort.Strings(out)
	return out
}

func capabilityList(caps hubSessionCapabilities) string {
	var out []string
	if caps.Send {
		out = append(out, "send")
	}
	if caps.Steer {
		out = append(out, "steer")
	}
	if caps.Interrupt {
		out = append(out, "interrupt")
	}
	if caps.Compact {
		out = append(out, "compact")
	}
	if caps.Clear {
		out = append(out, "clear")
	}
	if caps.Fork {
		out = append(out, "fork")
	}
	if caps.Resume {
		out = append(out, "resume")
	}
	if caps.Shutdown {
		out = append(out, "shutdown")
	}
	if caps.ChangeModel {
		out = append(out, "change model")
	}
	return strings.Join(out, ", ")
}
