package main

import (
	"errors"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/internal/appwire"
)

type noticePanel struct {
	Title      string
	Category   string
	Summary    string
	Source     string
	Reason     string
	NextAction string
	State      string // optional; used by View() for state-colored bar
}

// View renders the diagnostic voice: state-colored ▍ left bar + ● dot,
// followed by 3 indented key/value lines. This is a non-modal inline render.
func (n noticePanel) View() string {
	th := activeThemeV2()
	state := strings.TrimSpace(n.State)
	if state == "" {
		state = "idle"
	}
	stateClr := stateColor(state)
	bar := lipgloss.NewStyle().Foreground(stateClr).Render("▍")
	dot := lipgloss.NewStyle().Foreground(stateClr).Render("●")
	ruleSoft := lipgloss.NewStyle().Foreground(th.RuleSoft).Render(" · ")
	dim := func(s string) string { return lipgloss.NewStyle().Foreground(th.TextDim).Render(s) }
	text := func(s string) string { return lipgloss.NewStyle().Foreground(th.Text).Render(s) }

	summary := strings.TrimSpace(n.Summary)
	if summary == "" {
		summary = strings.TrimSpace(n.Title)
	}
	line1 := bar + " " + dot + " " + text(summary)
	line2 := "  " + dim("source ") + text(strings.TrimSpace(n.Source)) +
		ruleSoft + dim("cause ") + text(strings.TrimSpace(n.Reason))
	line3 := "  " + dim("next  ") + text(strings.TrimSpace(n.NextAction))
	return strings.Join([]string{line1, line2, line3}, "\n")
}

func (n noticePanel) Text() string {
	var lines []string
	styles := defaultTUIStyles()
	if title := strings.TrimSpace(n.Title); title != "" {
		lines = append(lines, styles.Section.Render(title))
	}
	if summary := strings.TrimSpace(n.Summary); summary != "" {
		lines = append(lines, summary)
	}
	if category := strings.TrimSpace(n.Category); category != "" {
		lines = append(lines, styles.Muted.Render("category: "+category))
	}
	if source := strings.TrimSpace(n.Source); source != "" {
		lines = append(lines, styles.Muted.Render("source: "+source))
	}
	if reason := strings.TrimSpace(n.Reason); reason != "" {
		lines = append(lines, styles.Muted.Render("reason: "+reason))
	}
	if next := strings.TrimSpace(n.NextAction); next != "" {
		lines = append(lines, styles.Muted.Render("next: "+next))
	}
	return strings.Join(lines, "\n")
}

func (m hubModel) renderNotices() string {
	if len(m.notices) == 0 {
		return ""
	}
	var lines []string
	for _, notice := range m.notices {
		lines = append(lines, notice.View())
	}
	lines = append(lines, "ctrl+x: dismiss notice")
	return renderPopupPane(strings.Join(lines, "\n\n"), m.width) + "\n"
}

func (m *hubModel) addNotice(notice noticePanel) {
	if strings.TrimSpace(notice.Title) == "" && strings.TrimSpace(notice.Summary) == "" {
		return
	}
	key := noticeKey(notice)
	for i, existing := range m.notices {
		if noticeKey(existing) == key {
			m.notices[i] = notice
			return
		}
	}
	m.notices = append(m.notices, notice)
}

func (m *hubModel) addHubErrorNotice(title, fallbackCategory string, err error, nextAction string) {
	m.addNotice(noticePanel{
		Title:      title,
		Category:   noticeCategoryForError(err, fallbackCategory),
		Summary:    noticeSummaryForError(err, title),
		Source:     m.sourceLabelForNotice(),
		Reason:     err.Error(),
		NextAction: nextAction,
	})
}

func (m *hubModel) dismissNotice() {
	if len(m.notices) == 0 {
		return
	}
	m.notices = m.notices[1:]
}

func (m *hubModel) clearNoticesByCategory(category string) {
	category = strings.TrimSpace(category)
	if category == "" {
		return
	}
	kept := m.notices[:0]
	for _, notice := range m.notices {
		if strings.TrimSpace(notice.Category) != category {
			kept = append(kept, notice)
		}
	}
	m.notices = kept
}

func noticeKey(notice noticePanel) string {
	return strings.Join([]string{
		strings.TrimSpace(notice.Category),
		strings.TrimSpace(notice.Source),
		strings.TrimSpace(notice.Title),
	}, "\x00")
}

// classifyWarningCategory returns the notice category for a NotifyWarning
// payload (kata 5q3p). It prefers the typed Cause when present so we
// stop substring-matching the message in the common case; cause-less
// legacy payloads still fall back to a message-prefix match so existing
// behavior is preserved.
func classifyWarningCategory(message string, cause *appwire.DiagnosticCause) string {
	if cause != nil {
		switch cause.Kind {
		case "provider":
			return "provider"
		}
	}
	trimmed := strings.ToLower(strings.TrimSpace(message))
	switch {
	case strings.HasPrefix(trimmed, "provider error"):
		return "provider"
	case strings.HasPrefix(trimmed, "serf error"):
		return "serf"
	}
	return "serf"
}

func noticeCategoryForError(err error, fallback string) string {
	var wire appwire.WireError
	if errors.As(err, &wire) {
		if data, ok := wire.Data.(appwire.ErrorData); ok {
			switch data.SerfErrorInfo {
			case appwire.ErrorProviderUnavailable:
				return "provider"
			case appwire.ErrorActionUnavailable:
				return "action"
			case appwire.ErrorSessionUnavailable, appwire.ErrorMethodNotFound, appwire.ErrorInvalidParams:
				return "appwire"
			case appwire.ErrorHubLaunch:
				return "launch"
			}
		}
	}
	if fallback != "" {
		return fallback
	}
	return "appwire"
}

func noticeSummaryForError(err error, fallback string) string {
	var wire appwire.WireError
	if errors.As(err, &wire) {
		if data, ok := wire.Data.(appwire.ErrorData); ok && data.SerfErrorInfo == appwire.ErrorProviderUnavailable {
			return "Check provider auth and runtime readiness."
		}
	}
	if fallback == "" {
		return "Hub request failed."
	}
	return fallback + "."
}

func (m hubModel) sourceLabelForNotice() string {
	if label := strings.TrimSpace(m.detail.SourceLabel); label != "" {
		return label
	}
	return sourceLabelFromRefText(m.detail.Ref)
}

func (m *hubModel) addActionUnavailableNotice(action, summary, reason string) {
	action = strings.TrimSpace(action)
	if reason == "" && action != "" {
		reason = "source does not advertise " + action
	}
	m.addNotice(noticePanel{
		Title:      "Action unavailable",
		Category:   "action-unavailable",
		Summary:    strings.TrimSpace(summary),
		Source:     m.sourceLabelForNotice(),
		Reason:     reason,
		NextAction: "Open /help to see source-supported actions.",
	})
}
