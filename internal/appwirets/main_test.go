package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRun(t *testing.T) {
	stderr, err := os.CreateTemp(t.TempDir(), "stderr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stderr.Close() })
	if got := run(nil, stderr, os.WriteFile); got != 2 {
		t.Fatalf("missing out code=%d", got)
	}
	if got := run([]string{"-bad"}, stderr, os.WriteFile); got != 2 {
		t.Fatalf("bad flag code=%d", got)
	}
	out := filepath.Join(t.TempDir(), "types.gen.ts")
	if got := run([]string{"-out", out}, stderr, os.WriteFile); got != 0 {
		t.Fatalf("success code=%d", got)
	}
	data, err := os.ReadFile(out)
	if err != nil || !strings.Contains(string(data), "export type AnyNotification") {
		t.Fatalf("output err=%v", err)
	}
	if got := run([]string{"-out", out}, stderr, func(string, []byte, os.FileMode) error { return errors.New("disk") }); got != 1 {
		t.Fatalf("write failure code=%d", got)
	}
}

func TestMainExitsOnMissingOut(t *testing.T) {
	oldExit, oldArgs := exitProcess, os.Args
	t.Cleanup(func() { exitProcess, os.Args = oldExit, oldArgs })
	os.Args = []string{"appwirets"}
	var code int
	exitProcess = func(got int) { code = got }
	main()
	if code != 2 {
		t.Fatalf("main exit=%d", code)
	}
}
