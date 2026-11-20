//go:build linux || darwin

package main

import (
	"slices"
	"testing"
)

func TestParseTargetTimingsRanksByLastActivity(t *testing.T) {
	t.Parallel()
	log := "" +
		"1.000\tCompileC /tmp/a.o /tmp/a.m normal arm64 (in target 'RNScreens' from project 'Pods')\n" +
		"2.500\tCompileC /tmp/b.o /tmp/b.m normal arm64 (in target 'Evener' from project 'Evener')\n" +
		"3.000\tSwiftCompile normal arm64 /tmp/c.swift (in target 'RNScreens' from project 'Pods')\n" +
		"9.000\tCompileC /tmp/d.o /tmp/d.m normal arm64 (in target 'Evener' from project 'Evener')\n" +
		"12.500\tLd /tmp/Evener normal arm64 (in target 'Evener' from project 'Evener')\n" +
		"13.000\t** BUILD SUCCEEDED **\n"

	rows := parseTargetTimings([]byte(log))
	if len(rows) != 2 {
		t.Fatalf("want 2 targets, got %d: %+v", len(rows), rows)
	}
	if rows[0].Name != "Evener" || rows[0].Last != 12.5 {
		t.Fatalf("want Evener last, got %+v", rows[0])
	}
	if rows[0].Compiles != 2 {
		t.Fatalf("want 2 Evener compiles, got %d", rows[0].Compiles)
	}
	if rows[1].Name != "RNScreens" || rows[1].First != 1.0 || rows[1].Compiles != 2 {
		t.Fatalf("want RNScreens first 1.0 with 2 compiles, got %+v", rows[1])
	}
	if end := buildEnd([]byte(log)); end != 13.0 {
		t.Fatalf("want build end 13.0, got %v", end)
	}
}

func TestCollectTargetDurationsFindsPairedFields(t *testing.T) {
	t.Parallel()
	document := []byte(`{"tests":[{"targetName":"RNScreens","duration":41.5},{"targetName":"Evener","duration":3.25},{"name":"other"}]}`)
	entries := collectTargetDurations(document)
	if len(entries) != 2 {
		t.Fatalf("want 2 duration entries, got %d: %+v", len(entries), entries)
	}
	names := []string{entries[0].Name, entries[1].Name}
	if !slices.Equal(names, []string{"RNScreens", "Evener"}) {
		t.Fatalf("want durations ranked high to low, got %v", names)
	}
}

func TestReleaseBuildCommandRecordsTiming(t *testing.T) {
	t.Parallel()
	args := releaseBuildCommand("UDID", "/tmp/derived", "/tmp/build.xcresult")
	for _, want := range []string{"-showBuildTimingSummary", "-resultBundlePath", "/tmp/build.xcresult"} {
		if !slices.Contains(args, want) {
			t.Fatalf("want %q in %v", want, args)
		}
	}
}
