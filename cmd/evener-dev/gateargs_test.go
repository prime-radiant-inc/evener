package dev

import (
	"bytes"
	"reflect"
	"strings"
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
		{name: "dangling value flag", args: []string{"-run"}, want: 2},
		{name: "dangling -tags", args: []string{"-tags"}, want: 2},
		{name: "-args as a value is fine", args: []string{"-run", "-args"}, want: 0},
		{name: "-- as a value is fine", args: []string{"-ldflags", "--"}, want: 0},
		// -tags is a value-taking flag the selection walker handles by name, so
		// the shared consumesValue leaves it out; this walker must still consume
		// its value.
		{name: "-C as a -tags value is fine", args: []string{"-tags", "-C"}, want: 0},
		{name: "a terminator after a -tags value is still refused", args: []string{"-tags", "-run", "-args"}, want: 2},
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

// TestRootTestFlagsRejectsDanglingAndNewlines pins that the root-flags helper
// fails cleanly -- not with an index panic -- on a flag whose value is missing,
// and refuses a value the line-per-flag handoff cannot carry. Both were reachable
// from the gate before checkValue/ walkFlags were shared.
func TestRootTestFlagsRejectsDanglingAndNewlines(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
	}{
		{name: "dangling value", args: []string{"-run"}},
		{name: "dangling tags", args: []string{"-tags"}},
		{name: "newline value", args: []string{"-run", "a\nb"}},
		{name: "newline in an inline value", args: []string{"-tags=a\nb"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := rootTestFlags(append([]string{"--"}, tc.args...), &stdout, &stderr); code == 0 {
				t.Fatalf("rootTestFlags(%q) = 0, want a usage error; stdout = %q", tc.args, stdout.String())
			}
		})
	}
}

// TestRootTestFlagsRemovesOnlyTheRealShortFlag pins the value-aware short-mode
// removal: the flag and its spellings go, a value that happens to spell -short
// stays, and a value with a space stays on one line.
func TestRootTestFlagsRemovesOnlyTheRealShortFlag(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		want []string
	}{
		{name: "removes short", args: []string{"-short", "-count=1"}, want: []string{"-count=1"}},
		{name: "and its other spellings", args: []string{"-short=true", "--short", "-test.short", "-race"}, want: []string{"-race"}},
		{name: "keeps a value that spells short", args: []string{"-run", "-short", "-count=1"}, want: []string{"-run", "-short", "-count=1"}},
		{name: "keeps a value with a space", args: []string{"-tags", "a b", "-short"}, want: []string{"-tags", "a b"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := rootTestFlags(append([]string{"--"}, tc.args...), &stdout, &stderr); code != 0 {
				t.Fatalf("rootTestFlags = %d, stderr = %q", code, stderr.String())
			}
			got := strings.Split(strings.TrimRight(stdout.String(), "\n"), "\n")
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("rootTestFlags(%q) = %q, want %q", tc.args, got, tc.want)
			}
		})
	}
}
