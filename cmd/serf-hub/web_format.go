package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"primeradiant.com/serf/agent/schema"
	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-hub/internal/hubcore"
	"primeradiant.com/serf/cmd/serf-hub/internal/strutil"
	"primeradiant.com/serf/hubapi"
)

// Pure presentation/data-mapping helpers for the workspace partials. These are
// stateless free functions (no *WebServer receiver); the render methods that
// use them live on WebServer in web_workspace.go.

func htmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, `"`, "&quot;")
	return s
}

func workspaceDataFromAppThread(thread appwire.Thread) WorkspaceData {
	ref := thread.Serf.Ref
	if ref == "" {
		ref = appwire.Ref{SourceID: thread.Source, ThreadID: thread.ID}.String()
	}
	title := thread.Name
	if title == "" {
		title = thread.Preview
	}
	if title == "" {
		title = strutil.FirstNonEmpty(thread.SessionID, thread.ID)
	}
	state := hubcore.NormalizeState(thread.Status.Type)
	if state == "" {
		state = "idle"
	}
	data := WorkspaceData{
		ID:           ref,
		SourceLabel:  sourceLabelFromRefText(ref),
		Title:        title,
		State:        state,
		StateLabel:   stateLabel(state, false),
		TurnCount:    completedTurnCount(thread.Turns),
		ActiveTurnID: activeTurnIDFromAppwireThread(thread),
		RunningFor:   activeTurnRunningFor(thread),
		Model:        thread.ModelProvider,
		WorkingDir:   thread.CWD,
		Capabilities: hubCapabilitiesFromAppwire(thread.Serf.Capabilities),
		// WorkMillis/Usage/ActiveTurnStartedAt are WS2's working-state/token
		// metrics, read on demand by the daemon rather than pushed per event.
		WorkMillis:          thread.Serf.WorkMillis,
		Usage:               thread.Serf.Usage,
		ActiveTurnStartedAt: thread.Serf.ActiveTurnStartedAt,
	}
	data.Cost = appwire.EstimateCost(data.Model, data.Usage)
	if goal := thread.Serf.Goal; goal != nil {
		data.GoalStatus = goal.Status
		data.GoalIterations = goal.Iterations
	}
	return data
}

func activeTurnRunningFor(thread appwire.Thread) string {
	for _, turn := range thread.Turns {
		if turn.Status != appwire.TurnStatusInProgress || turn.StartedAt == nil || *turn.StartedAt <= 0 {
			continue
		}
		return compactDuration(time.Since(time.Unix(*turn.StartedAt, 0)))
	}
	return ""
}

func compactDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		seconds := int(d.Seconds())
		if seconds < 1 {
			seconds = 1
		}
		return fmt.Sprintf("%ds", seconds)
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
}

// formatWorkMillis renders a session's accumulated work time compactly
// ("45s"/"3m"/"1h 4m"), mirroring compactDuration's convention so the status
// row's work-time cluster reads the same way as the rest of the hub's
// duration displays.
func formatWorkMillis(millis int64) string {
	return compactDuration(time.Duration(millis) * time.Millisecond)
}

func activeTurnIDFromAppwireThread(thread appwire.Thread) string {
	if thread.Serf.ActiveTurnID != "" {
		return thread.Serf.ActiveTurnID
	}
	for _, turn := range thread.Turns {
		if turn.Status == appwire.TurnStatusInProgress {
			return turn.ID
		}
	}
	return ""
}

// completedTurnCount counts only turns whose Status is "completed" — kata
// k5t4. Failed / canceled / in-flight turns don't count. Keeps the live
// status and the past-index display consistent.
func completedTurnCount(turns []appwire.Turn) int {
	n := 0
	for _, t := range turns {
		if t.Status == appwire.TurnStatusCompleted {
			n++
		}
	}
	return n
}

func sourceLabelFromRefText(refText string) string {
	ref, err := appwire.ParseRef(refText)
	if err != nil {
		return "serf"
	}
	if ref.SourceID == "" || ref.SourceID == "local" {
		return "serf"
	}
	return ref.SourceID
}

// liveTitle prefers the generated short session name from metadata, but avoids
// using the full initial prompt as the workspace title. If the past index does
// not have the session yet, fall back to a compact session ID.
func liveTitle(id string, le hubcore.LiveEntry, past *hubcore.PastIndex) string {
	if past != nil {
		if pe, ok := past.Find(id); ok {
			return pastTitle(pe)
		}
	}
	return hubcore.ShortID(id)
}

func pastTitle(pe hubcore.PastEntry) string {
	meta := pe.Meta
	if fresh, err := schema.LoadSessionMeta(pe.StateDir, pe.Meta.ID); err == nil {
		meta = fresh
	}
	return sessionTitleFromMeta(meta)
}

func sessionTitleFromMeta(meta schema.SessionMeta) string {
	if title := strings.TrimSpace(meta.Name); title != "" {
		return title
	}
	if prompt := compactSessionPromptTitle(meta.OriginalPrompt); prompt != "" {
		return prompt
	}
	return hubcore.ShortID(meta.ID)
}

func compactSessionPromptTitle(prompt string) string {
	prompt = strings.TrimSpace(strings.ReplaceAll(prompt, "\r", ""))
	if prompt == "" {
		return ""
	}
	if idx := strings.IndexByte(prompt, '\n'); idx >= 0 {
		prompt = strings.TrimSpace(prompt[:idx])
	}
	const maxLen = 80
	if len(prompt) <= maxLen {
		return prompt
	}
	return strings.TrimSpace(prompt[:maxLen-1]) + "…"
}

func searchPastTitle(pe hubcore.PastEntry) string {
	if title := strings.TrimSpace(pe.Meta.Name); title != "" {
		return title
	}
	return hubcore.ShortID(pe.Meta.ID)
}

// stateLabel returns the unified display word (Track A §1) for a normalized
// state. Delegates to hubapi.StateWord so the web and the TUI can never
// independently drift on vocabulary. askPending selects the needs-you band
// (Track A §2); pass false where the caller has no ask-pending information.
func stateLabel(state string, askPending bool) string {
	return hubapi.StateWord(state, askPending)
}

func formatContextNumbers(used, window, remaining int) string {
	if window <= 0 {
		return ""
	}
	if remaining < 0 {
		remaining = 0
	}
	return fmt.Sprintf("%s / %s tokens (%s left)", formatTokenCount(used), formatTokenCount(window), formatTokenCount(remaining))
}

func formatTokenCount(n int) string {
	if n < 0 {
		n = 0
	}
	if n < 1000 {
		return strconv.Itoa(n)
	}
	return fmt.Sprintf("%dk", (n+500)/1000)
}
