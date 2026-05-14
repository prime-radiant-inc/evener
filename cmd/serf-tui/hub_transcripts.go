package main

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/internal/appwire"
)

func hubTranscriptPickerItems(targets []appwire.ThreadTranscriptTarget) []modelPickerItem {
	items := make([]modelPickerItem, 0, len(targets))
	for _, target := range targets {
		if strings.TrimSpace(target.Ref) == "" {
			continue
		}
		display := strings.TrimSpace(target.Title)
		if display == "" {
			display = target.Ref
		}
		if target.Kind == "subagent" {
			details := []string{}
			if target.Status != "" {
				details = append(details, target.Status)
			}
			if target.TurnsUsed > 0 {
				details = append(details, fmt.Sprintf("%d turns", target.TurnsUsed))
			}
			if len(details) > 0 {
				display += " (" + strings.Join(details, ", ") + ")"
			}
		}
		items = append(items, modelPickerItem{id: target.Ref, display: display})
	}
	return items
}

func hubTranscriptTargetByRef(targets []appwire.ThreadTranscriptTarget, ref string) (appwire.ThreadTranscriptTarget, bool) {
	for _, target := range targets {
		if target.Ref == ref {
			return target, true
		}
	}
	return appwire.ThreadTranscriptTarget{}, false
}
