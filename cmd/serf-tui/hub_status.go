package main

import (
	"fmt"
	"strings"

	"primeradiant.com/serf/agent"
	"primeradiant.com/serf/internal/appwire"
)

func renderHubSessionStatus(detail hubSessionDetail, tasks []agent.Task, auth appwire.AuthStatusResponse, taskErr, authErr error) string {
	var b strings.Builder
	b.WriteString("status\n")
	if detail.SessionID != "" {
		fmt.Fprintf(&b, "Session:  %s\n", detail.SessionID)
	}
	if detail.SourceLabel != "" {
		fmt.Fprintf(&b, "Source:   %s\n", detail.SourceLabel)
	}
	writeModelOrProviderLine(&b, detail.Model, detail.Profile)
	if detail.WorkingDir != "" {
		fmt.Fprintf(&b, "Dir:      %s\n", detail.WorkingDir)
	}
	fmt.Fprintf(&b, "Turns:    %d\n", detail.TurnCount)
	if detail.ContextPressure > 0 {
		fmt.Fprintf(&b, "Context:  %.0f%% used\n", detail.ContextPressure*100)
	}
	if taskErr != nil {
		fmt.Fprintf(&b, "Tasks:    unavailable: %s\n", taskErr)
	} else {
		fmt.Fprintf(&b, "Tasks:    %s\n", taskSummary(tasks))
	}
	if authErr != nil {
		fmt.Fprintf(&b, "Auth:     unavailable: %s\n", authErr)
	} else {
		fmt.Fprintf(&b, "Auth:     %s\n", authSummary(auth))
	}
	if len(detail.RecentErrors) > 0 {
		b.WriteString("Recent errors:\n")
		for _, err := range detail.RecentErrors {
			fmt.Fprintf(&b, "  %s\n", err)
		}
	}
	return strings.TrimSpace(b.String())
}

func taskSummary(tasks []agent.Task) string {
	if len(tasks) == 0 {
		return "0/0 done"
	}
	done := 0
	active := 0
	for _, task := range tasks {
		switch task.Status {
		case agent.TaskDone, agent.TaskCancelled:
			done++
		case agent.TaskInProgress:
			active++
		}
	}
	summary := fmt.Sprintf("%d/%d done", done, len(tasks))
	if active > 0 {
		summary += fmt.Sprintf(", %d active", active)
	}
	return summary
}

func authSummary(auth appwire.AuthStatusResponse) string {
	provider := strings.TrimSpace(auth.Provider)
	if provider == "" {
		provider = "auth"
	}
	if !auth.Supported {
		return provider + " not supported"
	}
	if !auth.SignedIn {
		return provider + " signed out"
	}
	source := strings.TrimSpace(auth.ActiveSource)
	if source == "" {
		source = "signed in"
	}
	account := strings.TrimSpace(auth.Email)
	if account == "" {
		account = strings.TrimSpace(auth.StoredEmail)
	}
	if account == "" {
		return provider + " " + source
	}
	return provider + " " + source + " " + account
}

func authProviderForStatus(detail hubSessionDetail) string {
	if provider := strings.TrimSpace(detail.Profile); provider != "" {
		return provider
	}
	return "openai"
}

func hubErrorReason(err error) string {
	if err == nil {
		return ""
	}
	text := err.Error()
	if _, reason, ok := strings.Cut(text, ": "); ok && strings.HasPrefix(text, "appwire ") {
		return reason
	}
	return text
}
