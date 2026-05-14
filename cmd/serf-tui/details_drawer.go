package main

import (
	"fmt"
	"strings"
)

type detailsDrawer struct {
	Detail hubSessionDetail
}

func (d detailsDrawer) View() string {
	var b strings.Builder
	b.WriteString("details\n")
	detail := d.Detail
	if detail.SessionID != "" {
		fmt.Fprintf(&b, "Session:  %s\n", detail.SessionID)
	}
	if detail.SourceLabel != "" {
		fmt.Fprintf(&b, "Source:   %s\n", detail.SourceLabel)
	}
	if detail.Model != "" || detail.Profile != "" {
		fmt.Fprintf(&b, "Model:    %s (%s)\n", detail.Model, detail.Profile)
	}
	if detail.WorkingDir != "" {
		fmt.Fprintf(&b, "Dir:      %s\n", detail.WorkingDir)
	}
	if detail.Branch != "" {
		fmt.Fprintf(&b, "Branch:   %s\n", detail.Branch)
	}
	if detail.TurnCount > 0 {
		fmt.Fprintf(&b, "Turns:    %d\n", detail.TurnCount)
	}
	if caps := capabilityList(detail.Capabilities); caps != "" {
		fmt.Fprintf(&b, "Capabilities: %s\n", caps)
	}
	return strings.TrimSpace(b.String())
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
