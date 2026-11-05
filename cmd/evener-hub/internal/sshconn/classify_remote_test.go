package sshconn

import "testing"

// Every supervised start and restart command production builds must classify as
// the operation it performs, or fake hosts answer it as unknown and
// requireNoRemoteOps cannot see it.
func TestClassifyRemoteRecognizesSupervisorCommands(t *testing.T) {
	cases := []struct {
		name    string
		kind    supervisorKind
		restart remoteOp
		start   remoteOp
	}{
		{"system unit", supervisorSystemd, opSystemdRestart, opSystemdStart},
		{"user unit", supervisorSystemdUser, opSystemdRestart, opSystemdStart},
		{"launchd job", supervisorLaunchd, opLaunchdKick, opLaunchdKick},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := supervisor{kind: tc.kind, label: "evener-hub.service"}
			if tc.kind == supervisorLaunchd {
				s.label = "com.evener.hub"
			}
			restart, ok := s.restartRemote("501")
			if !ok {
				t.Fatal("restartRemote built no command")
			}
			if got := classifyRemote([]string{restart}); got != tc.restart {
				t.Fatalf("restart classified as %q, want %q", got, tc.restart)
			}
			start, ok := s.startRemote("501")
			if !ok {
				t.Fatal("startRemote built no command")
			}
			if got := classifyRemote([]string{start}); got != tc.start {
				t.Fatalf("start classified as %q, want %q", got, tc.start)
			}
		})
	}
}
