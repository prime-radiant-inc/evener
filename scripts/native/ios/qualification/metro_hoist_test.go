//go:build linux || darwin

package main

import (
	"bytes"
	"path/filepath"
	"strings"
	"testing"
)

// The hoist window must print with its elapsed seconds and skip every line the
// hoist did not write, so the job log carries the tail comparison directly.
func TestPrintHoistWindowEchoesMarkers(t *testing.T) {
	log := strings.Join([]string{
		"1.000\tBuild settings",
		"702.500\tE16 metro-hoist: begin 11:00:00",
		"745.000\tE16 metro-hoist: done 11:00:43",
		"770.000\tE16 metro-copy: waiting for /tmp/x/.complete",
		"770.100\tE16 metro-copy: copied 18 entries",
	}, "\n")
	var buf bytes.Buffer
	printHoistWindow(&buf, []byte(log))
	got := buf.String()
	for _, want := range []string{
		"build-timings: hoist 702.500 E16 metro-hoist: begin 11:00:00",
		"build-timings: hoist 745.000 E16 metro-hoist: done 11:00:43",
		"build-timings: hoist 770.000 E16 metro-copy: waiting for /tmp/x/.complete",
		"build-timings: hoist 770.100 E16 metro-copy: copied 18 entries",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hoist window missing %q; got:\n%s", want, got)
		}
	}
	if strings.Contains(got, "Build settings") {
		t.Errorf("hoist window echoed an unrelated line; got:\n%s", got)
	}
}

// The staging directory must sit inside the run output: a failed experiment
// keeps its bundle there, and a path outside --output would escape cleanup.
func TestMetroHoistStagingStaysInOutput(t *testing.T) {
	output := "/tmp/qualification-run"
	staging := metroHoistStaging(output)
	if staging != filepath.Join(output, "metro-hoist") {
		t.Fatalf("staging = %q, want %q", staging, filepath.Join(output, "metro-hoist"))
	}
	if filepath.Dir(staging) != output {
		t.Fatalf("staging parent = %q, want %q", filepath.Dir(staging), output)
	}
}

// The patch must name both projects, the host target, the carrier target and an
// absolute staging path, because a missing flag makes the script abort and a
// relative staging path makes it abort too.
func TestMetroHoistArgsCarryThePatchContract(t *testing.T) {
	args := metroHoistArgs("/repo", "/tmp/run")
	joined := map[string]string{}
	for i := 0; i+1 < len(args); i++ {
		if len(args[i]) > 2 && args[i][0] == '-' && args[i][1] == '-' {
			joined[args[i]] = args[i+1]
		}
	}
	want := map[string]string{
		"--app-project":    "ios/Evener.xcodeproj",
		"--pods-project":   "ios/Pods/Pods.xcodeproj",
		"--host-target":    "Evener",
		"--carrier-target": metroHoistCarrierTarget,
		"--staging":        filepath.Join("/tmp/run", "metro-hoist"),
	}
	for flag, value := range want {
		if joined[flag] != value {
			t.Errorf("%s = %q, want %q", flag, joined[flag], value)
		}
	}
	if len(args) < 2 || args[0] != "bundle" || args[1] != "exec" {
		t.Fatalf("args prefix = %v, want bundle exec", args)
	}
	if args[2] != "ruby" {
		t.Fatalf("args[2] = %q, want ruby", args[2])
	}
	if want := filepath.Join("/repo", "scripts", "native", "ios", "qualification", "metro_hoist.rb"); args[3] != want {
		t.Fatalf("script = %q, want %q", args[3], want)
	}
}
