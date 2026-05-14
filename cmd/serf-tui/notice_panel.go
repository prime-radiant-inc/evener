package main

import (
	"errors"
	"strings"

	"primeradiant.com/serf/internal/appwire"
)

type noticePanel struct {
	Title      string
	Category   string
	Summary    string
	Source     string
	Reason     string
	NextAction string
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
		lines = append(lines, notice.Text())
	}
	lines = append(lines, "ctrl+x: dismiss notice")
	return strings.Join(lines, "\n\n") + "\n"
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
