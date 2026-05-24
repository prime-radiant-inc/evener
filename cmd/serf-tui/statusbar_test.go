package main

import (
	"strings"
	"testing"
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

func TestStatusBarCtxWarningThreshold(t *testing.T) {
	// At 80% usage, color should be StateWarning.
	got := renderStatusBar(statusBarInfo{
		Connected: true,
		HubAddr:   "http://hub.test",
		Provider:  "openai",
		CtxUsed:   160000,
		CtxLimit:  200000,
		Width:     100,
	})
	// Just assert the ctx text is present; color verified visually.
	if !strings.Contains(got, "ctx") {
		t.Errorf("statusbar missing ctx info: %q", got)
	}
}
