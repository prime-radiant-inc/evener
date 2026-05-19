package main

import (
	"strings"

	"primeradiant.com/serf/internal/appwire"
)

func formatHubDiagnostic(title, source, message, fallback string) string {
	message = strings.TrimSpace(message)
	title = strings.TrimSpace(title)
	source = strings.TrimSpace(source)
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
	return formatHubDiagnosticWithCause(err.Title, err.Source, err.Message, fallback, err.Cause)
}

func formatHubDiagnosticWithCause(title, source, message, fallback string, cause *appwire.DiagnosticCause) string {
	if cause != nil && strings.EqualFold(strings.TrimSpace(cause.Kind), "provider") {
		source = "provider"
		if isLegacyNonProviderDiagnosticTitle(title) {
			title = ""
		}
	}
	return formatHubDiagnostic(title, source, message, fallback)
}

func isLegacyNonProviderDiagnosticTitle(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "", "serf error", "serf warning", "hub error", "hub warning", "ui error", "ui warning", "session error", "session warning":
		return true
	default:
		return false
	}
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
