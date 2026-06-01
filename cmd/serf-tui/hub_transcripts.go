package main

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/appwire"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuipick"
)

func hubTranscriptPickerItems(targets []appwire.ThreadTranscriptTarget) []tuipick.ModelPickerItem {
	items := make([]tuipick.ModelPickerItem, 0, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.Ref) == "" {
			continue
		}
		display := strings.TrimSpace(target.Title)
		if display == "" {
			display = target.Ref
		}
		details := []string{}
		if source := transcriptTargetSourceLabel(target); source != "" {
			details = append(details, source)
		}
		if target.Status != "" {
			details = append(details, stateLabel(target.Status))
		}
		if target.Kind == "subagent" && target.TurnsUsed > 0 {
			details = append(details, fmt.Sprintf("%d turns", target.TurnsUsed))
		}
		if len(details) > 0 {
			display += " (" + strings.Join(details, ", ") + ")"
		}
		items = append(items, tuipick.ModelPickerItem{ID: target.Ref, Display: display})
	}
	return items
}

func transcriptTargetSourceLabel(target appwire.ThreadTranscriptTarget) string {
	if source := strings.TrimSpace(target.Source); source != "" {
		return source
	}
	if ref := strings.TrimSpace(target.Ref); ref != "" {
		return sourceLabelFromRefText(ref)
	}
	return ""
}

func hubTranscriptTargetByRef(targets []appwire.ThreadTranscriptTarget, ref string) (appwire.ThreadTranscriptTarget, bool) {
	for _, target := range targets {
		if target.Ref == ref {
			return target, true
		}
	}
	return appwire.ThreadTranscriptTarget{}, false
}
