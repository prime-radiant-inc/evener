package main

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const compactThreshold = 0.90 // must match agent/context_manager.go SummarizeThreshold

// statusBarInfo holds the data for the hub session persistent statusbar.
type statusBarInfo struct {
	Connected bool
	HubAddr   string
	Provider  string
	Queued    int     // inflight LLM requests
	CtxUsed   int     // tokens used
	CtxLimit  int     // window size
	Cost      float64 // dollars; 0 hides
	Version   string  // serf-tui version
	Width     int
}

// renderStatusBar renders a persistent status bar using the hub session theme.
func renderStatusBar(info statusBarInfo) string {
	th := activeTheme()
	parts := []string{}

	// Health dot + label
	healthClr := th.StateAwaiting
	healthLabel := "disconnected"
	if info.Connected {
		healthClr = th.StateIdle
		healthLabel = "connected"
	}
	health := lipgloss.NewStyle().Foreground(healthClr).Bold(true).Render("●") +
		" " + lipgloss.NewStyle().Foreground(th.TextDim).Render(healthLabel)
	parts = append(parts, health)

	// Hub address
	if info.HubAddr != "" {
		parts = append(parts, lipgloss.NewStyle().Foreground(th.TextDim).Render(info.HubAddr))
	}

	// Provider + queued
	if info.Provider != "" {
		provText := info.Provider
		if info.Queued > 0 {
			provText = fmt.Sprintf("%s %d", info.Provider, info.Queued)
		}
		parts = append(parts, lipgloss.NewStyle().Foreground(th.TextDim).Render(provText))
	}

	// Context usage with threshold colors
	if info.CtxLimit > 0 {
		ratio := float64(info.CtxUsed) / float64(info.CtxLimit)
		ctxClr := th.TextDim
		switch {
		case ratio >= 0.90: // matches existing agent/context_manager compactThreshold
			ctxClr = th.StateAwaiting
		case ratio >= 0.75:
			ctxClr = th.StateWarning
		}
		ctxText := fmt.Sprintf("ctx %s/%s", formatTokenCount(info.CtxUsed), formatTokenCount(info.CtxLimit))
		parts = append(parts, lipgloss.NewStyle().Foreground(ctxClr).Render(ctxText))
	}

	// Cost
	if info.Cost > 0 {
		parts = append(parts, lipgloss.NewStyle().Foreground(th.TextDim).Render(fmt.Sprintf("$%.2f", info.Cost)))
	}

	left := strings.Join(parts, lipgloss.NewStyle().Foreground(th.RuleSoft).Render(" · "))

	// Version right-aligned
	if info.Version != "" && info.Width > 0 {
		right := lipgloss.NewStyle().Foreground(th.TextGhost).Render(info.Version)
		gap := info.Width - lipgloss.Width(left) - lipgloss.Width(right)
		if gap > 2 {
			return left + strings.Repeat(" ", gap) + right
		}
	}
	// Enforce width on all paths — hub sessions pass no Version but can
	// still produce a left side wider than the terminal if the URL or
	// provider string is long. Use ANSI-aware truncation to avoid slicing
	// mid-escape-sequence inside the styled spans that make up left.
	if info.Width > 0 && lipgloss.Width(left) > info.Width {
		return ansi.Truncate(left, info.Width, "...")
	}
	return left
}

// formatTokenCount formats a token count compactly: e.g. 1234 → "1k", 12345 → "12k".
func formatTokenCount(n int) string {
	if n >= 1000 {
		return fmt.Sprintf("%dk", n/1000)
	}
	return fmt.Sprintf("%d", n)
}

// formatTokens formats a token count compactly for the standalone model view:
// e.g. 1234 → "1.2k", 12345 → "12k".
func formatTokens(n int) string {
	if n < 1000 {
		return fmt.Sprintf("%d", n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%dk", n/1000)
}

