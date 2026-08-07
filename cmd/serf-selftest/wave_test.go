package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func writeSuite(t *testing.T, dir, name, body string) {
	t.Helper()
	path := filepath.Join(dir, name+"-selftest.sh")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestAllSuitesPassPrintsPassAndExitsZero(t *testing.T) {
	dir := t.TempDir()
	writeSuite(t, dir, "alpha", "echo alpha-ran\nexit 0\n")
	writeSuite(t, dir, "beta", "exit 0\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"alpha", "beta"}, KillGrace: time.Second, Out: &out})
	if code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
	for _, want := range []string{"PASS  alpha", "PASS  beta"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("missing %q in:\n%s", want, out.String())
		}
	}
	if strings.Contains(out.String(), "alpha-ran") {
		t.Fatalf("passing suite's log must not be replayed:\n%s", out.String())
	}
}

func TestFailingSuiteReplaysItsLogOnce(t *testing.T) {
	dir := t.TempDir()
	writeSuite(t, dir, "alpha", "exit 0\n")
	writeSuite(t, dir, "beta", "echo beta-broke\nexit 3\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"alpha", "beta"}, KillGrace: time.Second, Out: &out})
	if code != 1 {
		t.Fatalf("exit %d, want 1; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "PASS  alpha") {
		t.Fatalf("alpha should still pass:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "FAIL  beta") {
		t.Fatalf("missing FAIL beta:\n%s", out.String())
	}
	if got := strings.Count(out.String(), "beta-broke"); got != 1 {
		t.Fatalf("failing log replayed %d times, want 1:\n%s", got, out.String())
	}
}

func TestSuiteLeavingTempFilesFails(t *testing.T) {
	dir := t.TempDir()
	writeSuite(t, dir, "tidy", "exit 0\n")
	writeSuite(t, dir, "messy", "touch \"$TMPDIR/leak\"\nexit 0\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"tidy", "messy"}, KillGrace: time.Second, Out: &out})
	if code != 1 {
		t.Fatalf("exit %d, want 1; output:\n%s", code, out.String())
	}
	if !strings.Contains(out.String(), "FAIL  messy") {
		t.Fatalf("missing FAIL messy:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "leaked") || !strings.Contains(out.String(), "leak") {
		t.Fatalf("leak diagnosis missing:\n%s", out.String())
	}
	if !strings.Contains(out.String(), "PASS  tidy") {
		t.Fatalf("tidy should pass:\n%s", out.String())
	}
}

func TestSuiteGetsPrivateTmpdir(t *testing.T) {
	dir := t.TempDir()
	report := filepath.Join(dir, "report")
	if err := os.Mkdir(report, 0o755); err != nil {
		t.Fatal(err)
	}
	// Each suite records the TMPDIR it saw OUTSIDE that TMPDIR (leaving it
	// clean), so the test can compare after the wave completes.
	writeSuite(t, dir, "one", "echo \"$TMPDIR\" >"+report+"/one\nexit 0\n")
	writeSuite(t, dir, "two", "echo \"$TMPDIR\" >"+report+"/two\nexit 0\n")
	var out bytes.Buffer
	code := runWave(waveConfig{ScriptsDir: dir, Suites: []string{"one", "two"}, KillGrace: time.Second, Out: &out})
	if code != 0 {
		t.Fatalf("exit %d, output:\n%s", code, out.String())
	}
	oneTmp := readTrimmed(t, filepath.Join(report, "one"))
	twoTmp := readTrimmed(t, filepath.Join(report, "two"))
	if oneTmp == "" || oneTmp == twoTmp {
		t.Fatalf("suites must see distinct private TMPDIRs, got %q and %q", oneTmp, twoTmp)
	}
	if _, err := os.Stat(oneTmp); !os.IsNotExist(err) {
		t.Fatalf("run dir should be removed after the wave, %s still exists", oneTmp)
	}
}

func readTrimmed(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(b))
}
