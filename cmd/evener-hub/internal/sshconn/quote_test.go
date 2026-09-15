package sshconn

import (
	"strings"
	"testing"
)

func TestShellQuote(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"", "''"},
		{"/opt/evener/bin/evener", "/opt/evener/bin/evener"},
		{"evener-hub.service", "evener-hub.service"},
		{"127.0.0.1:9180", "127.0.0.1:9180"},
		{"4242", "4242"},
		{"--stdio", "--stdio"},
		{"evener-appwire-v5", "evener-appwire-v5"},
		{"~/bin/evener", "~/bin/evener"},
		{"`id`", "'`id`'"},
		{"/home/dev/my hub.log", "'/home/dev/my hub.log'"},
		{"/tmp/a;rm -rf /", "'/tmp/a;rm -rf /'"},
		{"a'b", `'a'\''b'`},
		{"$(id -u)", "'$(id -u)'"},
		{"a\nb", "'a\nb'"},
	}
	for _, tc := range cases {
		if got := shellQuote(tc.in); got != tc.want {
			t.Errorf("shellQuote(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestStripPSHeader(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"header", "COMMAND\n/opt/evener/bin/evener hub -addr 0.0.0.0:9180\n", "/opt/evener/bin/evener hub -addr 0.0.0.0:9180"},
		{"suppressed", "/opt/evener/bin/evener hub -addr 0.0.0.0:9180\n", "/opt/evener/bin/evener hub -addr 0.0.0.0:9180"},
		{"header only", "COMMAND\n", ""},
		{"empty", "", ""},
		// A command that merely starts with the word COMMAND is not a header.
		{"command word", "COMMANDER --flag\n", "COMMANDER --flag"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := stripPSHeader(tc.in); got != tc.want {
				t.Fatalf("stripPSHeader(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestRelaunchCommandQuotesHostDerivedValues proves the recovered command line
// and the recovered log path cannot inject extra shell commands: a host that
// returns an argv word or `lsof` log path containing metacharacters must not have
// them interpreted by the shell that starts the relaunch. Every recovered word is
// quoted individually and the line is never re-parsed by `sh -c`.
func TestRelaunchCommandQuotesHostDerivedValues(t *testing.T) {
	argv := []string{"evener", "hub", "-config", "/home/dev/my hub.log; touch /tmp/pwned"}
	logPath := "/home/dev/my hub.log"
	got := relaunchCommand(argv, logPath)
	want := "nohup evener hub -config '/home/dev/my hub.log; touch /tmp/pwned' >>'/home/dev/my hub.log' 2>&1 </dev/null &"
	if got != want {
		t.Fatalf("relaunchCommand:\n got %q\nwant %q", got, want)
	}
	if strings.Contains(got, "sh -c") {
		t.Fatalf("relaunch re-parses the recovered line with a shell: %q", got)
	}
}

// TestSupervisorRestartRemoteQuotesLabel proves the supervisor label is quoted:
// a label recovered from the host's service listing must not be able to add
// commands to the restart invocation.
func TestSupervisorRestartRemoteQuotesLabel(t *testing.T) {
	cases := []struct {
		sup  supervisor
		want string
	}{
		{supervisor{supervisorSystemd, "evener-hub.service"}, "systemctl restart evener-hub.service"},
		{supervisor{supervisorSystemdUser, "evener-hub.service"}, "systemctl --user restart evener-hub.service"},
		{supervisor{supervisorLaunchd, "com.example.evener-hub"}, "launchctl kickstart -k gui/$(id -u)/com.example.evener-hub"},
		{supervisor{supervisorSystemd, "evener; rm -rf /"}, "systemctl restart 'evener; rm -rf /'"},
	}
	for _, tc := range cases {
		if got := tc.sup.restartRemote(); got != tc.want {
			t.Errorf("restartRemote() = %q, want %q", got, tc.want)
		}
	}
}
