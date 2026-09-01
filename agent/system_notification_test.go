package agent

import "testing"

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
	want := "<system-notification>dir: \"/tmp/skill\"</system-notification>"
	if got != want {
		t.Fatalf("systemNotificationf = %q, want %q", got, want)
	}
}
