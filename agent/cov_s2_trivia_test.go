package agent

import (
	"strings"
	"testing"

	"primeradiant.com/serf/agent/events"
	"primeradiant.com/serf/agent/provider"
)

// s2cov_ tests for small pure helpers with defensive branches that the
// higher-level flows rarely exercise directly.

func TestS2Cov_WarningHookMessage(t *testing.T) {
	t.Parallel()
	wd := events.WarningData{Message: "boom"}
	if got := warningHookMessage(wd); got != "boom" {
		t.Fatalf("value = %q, want boom", got)
	}
	if got := warningHookMessage(&wd); got != "boom" {
		t.Fatalf("pointer = %q, want boom", got)
	}
	var nilWD *events.WarningData
	// A typed-nil *WarningData falls through to the default fmt.Sprint branch.
	if got := warningHookMessage(nilWD); got == "" {
		t.Fatalf("nil pointer = %q, want non-empty fmt.Sprint", got)
	}
	if got := warningHookMessage(events.CompactionTurnData{Kind: "SUMMARY"}); got == "" {
		t.Fatalf("default branch returned empty for non-warning data")
	}
}

func TestS2Cov_QueuedEntryPreviewLine(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name  string
		entry queuedInput
		want  string
	}{
		{"text wins", queuedInput{Text: "hello\nworld"}, "hello"},
		{"single image", queuedInput{Images: []ImageAttachment{{}}}, "[image]"},
		{"multiple images", queuedInput{Images: []ImageAttachment{{}, {}, {}}}, "[3 images]"},
		{"empty", queuedInput{}, ""},
		{"blank text falls to image", queuedInput{Text: "   ", Images: []ImageAttachment{{}}}, "[image]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := queuedEntryPreviewLine(tc.entry); got != tc.want {
				t.Fatalf("queuedEntryPreviewLine = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestS2Cov_JobNotifications(t *testing.T) {
	t.Parallel()
	if got := jobNotifications(nil); got != nil {
		t.Fatalf("nil input = %v, want nil", got)
	}
	in := []deliverableJobNotification{
		{notification: jobNotification{JobID: "j1", Status: "done"}},
		{notification: jobNotification{JobID: "j2", Status: "failed"}},
	}
	out := jobNotifications(in)
	if len(out) != 2 || out[0].JobID != "j1" || out[1].JobID != "j2" {
		t.Fatalf("out = %+v, want the two unwrapped notifications", out)
	}
}

func TestS2Cov_SanitizeSessionName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{"  Fix Flaky Test  ", "Fix Flaky Test"},
		{`"Quoted Title"`, "Quoted Title"},
		{"Trailing Punctuation!!!", "Trailing Punctuation"},
		{"a  b   c", "a b c"},
		{strings.Repeat("A", 80), strings.Repeat("A", sessionNameMaxRunes)},
	}
	for _, tc := range cases {
		if got := sanitizeSessionName(tc.in); got != tc.want {
			t.Fatalf("sanitizeSessionName(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestS2Cov_TrimForSessionNamer(t *testing.T) {
	t.Parallel()
	if got := trimForSessionNamer("  short  "); got != "short" {
		t.Fatalf("short = %q, want short", got)
	}
	long := strings.Repeat("x", 5000)
	got := trimForSessionNamer(long)
	if !strings.HasSuffix(got, "...[truncated]") {
		t.Fatalf("long input not truncated: %q", got[len(got)-20:])
	}
	if len([]rune(got)) != 4000+len([]rune("\n...[truncated]")) {
		t.Fatalf("truncated length = %d runes", len([]rune(got)))
	}
}

func TestS2Cov_SessionNameSourceLabel(t *testing.T) {
	t.Parallel()
	if got := sessionNameSourceLabel(sessionNameSourceCompaction); got != "compaction" {
		t.Fatalf("compaction label = %q", got)
	}
	if got := sessionNameSourceLabel(sessionNameSourcePrompt); got != "prompt" {
		t.Fatalf("prompt label = %q", got)
	}
	if got := sessionNameSourceLabel("anything-else"); got != "prompt" {
		t.Fatalf("default label = %q, want prompt", got)
	}
}

func TestS2Cov_SessionNamerModelGuards(t *testing.T) {
	t.Parallel()
	// Nil profile: every accessor returns the empty / disabled value.
	if sessionNamerEnabled(nil) {
		t.Fatal("nil profile should not enable the namer")
	}
	if got := sessionNamerModel(nil); got != "" {
		t.Fatalf("sessionNamerModel(nil) = %q, want empty", got)
	}
	if got := configuredSessionNamerModel(nil); got != "" {
		t.Fatalf("configuredSessionNamerModel(nil) = %q, want empty", got)
	}
	// A profile with no configured cheap model falls back to the active model.
	p := NewOpenAIProfile("gpt-5.2")
	if got := sessionNamerModel(p); got != "gpt-5.2" {
		t.Fatalf("sessionNamerModel = %q, want gpt-5.2 fallback", got)
	}
	var _ *provider.Profile = p
}
