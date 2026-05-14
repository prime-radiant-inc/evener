package main

import (
	"strings"
)

type noticePanel struct {
	Title      string
	Summary    string
	Source     string
	Reason     string
	NextAction string
}

func (n noticePanel) Text() string {
	var lines []string
	if title := strings.TrimSpace(n.Title); title != "" {
		lines = append(lines, title)
	}
	if summary := strings.TrimSpace(n.Summary); summary != "" {
		lines = append(lines, summary)
	}
	if source := strings.TrimSpace(n.Source); source != "" {
		lines = append(lines, "source: "+source)
	}
	if reason := strings.TrimSpace(n.Reason); reason != "" {
		lines = append(lines, "reason: "+reason)
	}
	if next := strings.TrimSpace(n.NextAction); next != "" {
		lines = append(lines, "next: "+next)
	}
	return strings.Join(lines, "\n")
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
	m.addSessionSystem(noticePanel{
		Title:   "Action unavailable",
		Summary: strings.TrimSpace(summary),
		Source:  m.sourceLabelForNotice(),
		Reason:  reason,
	}.Text())
}
