package dev

import (
	"bytes"
	"testing"
)

// TestCheckGateFlagsRefusesWhatTheGateCannotCarry pins the value-aware walk:
// a real terminator or -C is refused, while the same spelling as another flag's
// value is not, and GOFLAGS is split with Go's own quoting.
func TestCheckGateFlagsRefusesWhatTheGateCannotCarry(t *testing.T) {
	for _, tc := range []struct {
		name    string
		goflags string
		args    []string
		want    int
	}{
		{name: "ordinary flags", args: []string{"-short", "-count=1"}, want: 0},
		{name: "terminator -args", args: []string{"-args"}, want: 2},
		{name: "terminator --args=true", args: []string{"--args=true"}, want: 2},
		{name: "bare --", args: []string{"--"}, want: 2},
		{name: "-args as a value is fine", args: []string{"-run", "-args"}, want: 0},
		{name: "-- as a value is fine", args: []string{"-ldflags", "--"}, want: 0},
		{name: "-C in argv", args: []string{"-C", "x"}, want: 2},
		{name: "-C inline in argv", args: []string{"-C=x"}, want: 2},
		{name: "GOFLAGS -C", goflags: "-C=/tmp", args: []string{"-short"}, want: 2},
		{name: "GOFLAGS quoted -C", goflags: `"-C" /tmp`, args: []string{"-short"}, want: 2},
		{name: "GOFLAGS harmless", goflags: "-mod=mod", args: []string{"-short"}, want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			args := []string{"--goflags", tc.goflags, "--"}
			args = append(args, tc.args...)
			var stdout, stderr bytes.Buffer
			got := checkGateFlags(args, &stdout, &stderr)
			if got != tc.want {
				t.Fatalf("checkGateFlags(goflags=%q, args=%q) = %d, want %d; stderr = %q", tc.goflags, tc.args, got, tc.want, stderr.String())
			}
		})
	}
}
