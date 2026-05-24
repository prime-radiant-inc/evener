package main

import (
	"strings"
	"testing"
)

func TestDetailsDrawerHasSectionLabels(t *testing.T) {
	withTestColorProfile(t)
	d := detailsDrawer{Detail: hubSessionDetail{
		Title: "Test",
		State: "awaiting",
		Model: "openai/gpt-5.5",
	}}
	got := d.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "AWAITING") {
		t.Errorf("details drawer should show state badge AWAITING: %q", plain)
	}
}
