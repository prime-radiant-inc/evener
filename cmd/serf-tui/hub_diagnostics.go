package main

import (
	"strings"

	"primeradiant.com/serf/internal/appwire"
)

func formatHubDiagnostic(title, source, message, fallback string) string {
	message = strings.TrimSpace(message)
	title = strings.TrimSpace(title)
	if title == "" {
		title = defaultHubDiagnosticTitle(source, fallback)
	}
	if message == "" {
		return title
	}
	return title + ": " + message
}

func formatHubTurnError(err *appwire.TurnError, fallback string) string {
	if err == nil {
		return formatHubDiagnostic("", "", "", fallback)
	}
	return formatHubDiagnostic(err.Title, err.Source, err.Message, fallback)
}

func defaultHubDiagnosticTitle(source, fallback string) string {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "provider":
		return "Provider error"
	case "serf":
		return "Serf error"
	case "hub":
		return "Hub error"
	case "ui":
		return "UI error"
	default:
		if strings.TrimSpace(fallback) != "" {
			return strings.TrimSpace(fallback)
		}
		return "Session warning"
	}
}
