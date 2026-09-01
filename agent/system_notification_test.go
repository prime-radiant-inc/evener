package agent

import (
	"strings"
	"testing"
)

func TestSystemNotification(t *testing.T) {
	t.Parallel()
	got := systemNotification("hello")
	want := "<system-notification>hello</system-notification>"
	if got != want {
		t.Fatalf("systemNotification = %q, want %q", got, want)
	}
}

func TestSystemNotificationf(t *testing.T) {
	t.Parallel()
	got := systemNotificationf("dir: %q", "/tmp/skill")
	want := `<system-notification>dir: "/tmp/skill"</system-notification>`
	if got != want {
		t.Fatalf("systemNotificationf = %q, want %q", got, want)
	}
}

func TestSystemReminder(t *testing.T) {
	t.Parallel()
	got := systemReminder("nudge")
	want := "<system-reminder>nudge</system-reminder>"
	if got != want {
		t.Fatalf("systemReminder = %q, want %q", got, want)
	}
}

func TestSystemReminderf(t *testing.T) {
	t.Parallel()
	got := systemReminderf("depth ~%d", 3)
	want := "<system-reminder>depth ~3</system-reminder>"
	if got != want {
		t.Fatalf("systemReminderf = %q, want %q", got, want)
	}
}

func TestSystemReminderBlock(t *testing.T) {
	t.Parallel()
	got := systemReminderBlock("inner content")
	if !strings.HasPrefix(got, "<SYSTEM-REMINDER>\n") {
		t.Fatalf("missing opening tag+newline: %q", got)
	}
	if !strings.HasSuffix(got, "</SYSTEM-REMINDER>") {
		t.Fatalf("missing closing tag: %q", got)
	}
	if !strings.Contains(got, "inner content") {
		t.Fatalf("missing inner content: %q", got)
	}
}

func TestSystemReminderBlock_TrailingNewline(t *testing.T) {
	t.Parallel()
	// Inner content that already ends with \n should not get a doubled newline.
	got := systemReminderBlock("line1\n")
	want := "<SYSTEM-REMINDER>\nline1\n</SYSTEM-REMINDER>"
	if got != want {
		t.Fatalf("systemReminderBlock = %q, want %q", got, want)
	}
}

func TestSystemReminderBlockBuilder(t *testing.T) {
	t.Parallel()
	b := systemReminderBlockBuilder()
	b.WriteString("task: do thing\n")
	got := finishSystemReminderBlock(b)
	want := "<SYSTEM-REMINDER>\ntask: do thing\n</SYSTEM-REMINDER>"
	if got != want {
		t.Fatalf("builder result = %q, want %q", got, want)
	}
}
