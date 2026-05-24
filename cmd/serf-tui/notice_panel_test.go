package main

import (
	"strings"
	"testing"
)

func TestNoticePanelHasStateBar(t *testing.T) {
	withTestColorProfile(t)
	np := noticePanel{
		Summary:    "spawn failed: model provider not reported by harness",
		Source:     "serf",
		Reason:     "selected provider openai not in discovery",
		NextAction: "refresh spawn options or choose a reported harness model",
		State:      "awaiting",
	}
	got := np.View()
	plain := ansiPattern.ReplaceAllString(got, "")
	if !strings.Contains(plain, "▍") {
		t.Errorf("notice should have state bar: %q", plain)
	}
	if !strings.Contains(plain, "source") || !strings.Contains(plain, "next") {
		t.Errorf("notice should include source + next labels: %q", plain)
	}
}
