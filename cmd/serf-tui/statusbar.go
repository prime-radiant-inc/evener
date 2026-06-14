package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

// compactThreshold and warnThreshold must match agent/context_manager.go
// SummarizeThreshold and WarnThreshold respectively.
const (
	compactThreshold = 0.95
	warnThreshold    = 0.75
)

// ctxBand classifies a context-usage ratio into a color band.
type ctxBand int

const (
	bandNormal  ctxBand = iota
	bandWarn
	bandCompact
)

// ctxBandFor returns the color band for a given context-usage ratio.
func ctxBandFor(ratio float64) ctxBand {
	switch {
	case ratio >= compactThreshold:
		return bandCompact
	case ratio >= warnThreshold:
		return bandWarn
	default:
		return bandNormal
	}
}

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
	th := tuitheme.ActiveTheme()
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
		switch ctxBandFor(ratio) {
		case bandCompact:
			ctxClr = th.StateAwaiting
		case bandWarn:
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
	return strconv.Itoa(n)
}

// formatTokens formats a token count compactly for the standalone model view:
// e.g. 1234 → "1.2k", 12345 → "12k".
func formatTokens(n int) string {
	if n < 1000 {
		return strconv.Itoa(n)
	}
	if n < 10000 {
		return fmt.Sprintf("%.1fk", float64(n)/1000)
	}
	return fmt.Sprintf("%dk", n/1000)
}
