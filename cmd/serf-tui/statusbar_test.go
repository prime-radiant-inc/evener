package main

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
	"primeradiant.com/serf/cmd/serf-tui/internal/tuitheme"
)

func TestStatusBarConnectedShowsGreenDot(t *testing.T) {
	got := renderStatusBar(statusBarInfo{
		Connected: true,
		HubAddr:   "http://hub.test",
		Provider:  "openai",
		Width:     100,
	})
	if !strings.Contains(got, "●") {
		t.Errorf("statusbar missing health dot: %q", got)
	}
	if !strings.Contains(got, "connected") {
		t.Errorf("statusbar missing 'connected' label: %q", got)
	}
}

func TestCtxBandFor_TracksThreshold(t *testing.T) {
	if ctxBandFor(0.92) == bandCompact {
		t.Fatal("0.92 must not be compact band when threshold is 0.95")
	}
	if ctxBandFor(0.96) != bandCompact {
		t.Fatal("0.96 must be compact band")
	}
	if ctxBandFor(0.80) != bandWarn {
		t.Fatal("0.80 must be warn band")
	}
	if ctxBandFor(0.50) != bandNormal {
		t.Fatal("0.50 must be normal band")
	}
}

func TestStatusBarShowsCtxWhenLimitKnown(t *testing.T) {
	// When CtxLimit is known the ctx field appears in the statusbar.
	got := renderStatusBar(statusBarInfo{
		Connected: true,
		HubAddr:   "http://hub.test",
		Provider:  "openai",
		CtxUsed:   160000,
		CtxLimit:  200000,
		Width:     100,
	})
	if !strings.Contains(got, "ctx") {
		t.Errorf("statusbar missing ctx info: %q", got)
	}
}

func TestStatusBarCtxWarningThreshold(t *testing.T) {
	// At ≥ warnThreshold usage (80%), renderStatusBar must apply StateWarning color,
	// not TextDim. We verify this by forcing TrueColor so ANSI escapes are emitted,
	// then checking that the output contains exactly the lipgloss-styled token that
	// renderStatusBar produces when the warning branch is taken.
	withTestColorProfile(t)
	th := tuitheme.ActiveTheme()

	ctxAt80 := fmt.Sprintf("ctx %s/%s", formatTokenCount(160000), formatTokenCount(200000))
	warnStyled := lipgloss.NewStyle().Foreground(th.StateWarning).Render(ctxAt80)

	got := renderStatusBar(statusBarInfo{
		Connected: true,
		HubAddr:   "http://hub.test",
		Provider:  "openai",
		CtxUsed:   160000,
		CtxLimit:  200000,
		Width:     100,
	})
	if !strings.Contains(got, warnStyled) {
		t.Errorf("80%% ctx usage: expected StateWarning-colored %q in output; got: %q", warnStyled, got)
	}
}
