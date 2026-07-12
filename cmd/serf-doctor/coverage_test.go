package main

import (
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"

	"primeradiant.com/serf/agent/doctor"
)

type errorWriter struct{}

func (errorWriter) Write([]byte) (int, error) { return 0, errors.New("write") }

type failAfterWriter struct{ writes int }

func (w *failAfterWriter) Write(p []byte) (int, error) {
	w.writes++
	if w.writes > 1 {
		return 0, errors.New("write")
	}
	return len(p), nil
}

func TestMainAndWriterFailures(t *testing.T) {
	oldExit, oldArgs := osExit, os.Args
	t.Cleanup(func() { osExit, os.Args = oldExit, oldArgs })
	os.Args = []string{"serf-doctor", "help"}
	got := -1
	osExit = func(code int) { got = code }
	main()
	if got != 0 {
		t.Fatalf("exit = %d", got)
	}
	if writef(errorWriter{}, "x") != 1 || writeln(errorWriter{}, "x") != 1 || writeText(errorWriter{}, "x") != 1 {
		t.Fatal("writer failures not propagated")
	}
	if emitJSON(errorWriter{}, map[string]string{"x": "y"}) != 1 {
		t.Fatal("json writer failure")
	}
	if fail(errorWriter{}, "x", errors.New("bad")) != 1 {
		t.Fatal("failure writer code")
	}
}

func TestRunDispatchAndFlagFailures(t *testing.T) {
	var out, errOut bytes.Buffer
	if got := run(nil, &out, &errOut); got != 2 {
		t.Fatalf("no args = %d", got)
	}
	if got := run([]string{"unknown"}, &out, errorWriter{}); got != 1 {
		t.Fatalf("unknown write fail = %d", got)
	}
	if got := run([]string{"unknown"}, &out, &failAfterWriter{}); got != 1 {
		t.Fatalf("unknown usage write fail = %d", got)
	}
	for _, sub := range []string{"locate", "transcript", "apilog", "watches", "tree", "plugins"} {
		out.Reset()
		errOut.Reset()
		if got := run([]string{sub, "--definitely-invalid"}, &out, &errOut); got != 2 {
			t.Errorf("%s invalid flag = %d", sub, got)
		}
	}
}

func TestDoctorRemainingOutputs(t *testing.T) {
	base, sid := fixture(t)
	for _, args := range [][]string{
		{"transcript", "--state-dir", base, sid},
		{"transcript", "--json", "--state-dir", base, sid},
		{"transcript", "--json", "--count", "communicate", "--state-dir", base, sid},
		{"watches", "--json", "--state-dir", base, sid},
	} {
		var out, errOut bytes.Buffer
		if got := run(args, &out, &errOut); got != 0 {
			t.Errorf("%v = %d, %s", args, got, errOut.String())
		}
	}
	for _, sub := range []string{"transcript", "apilog", "watches", "tree"} {
		var out, errOut bytes.Buffer
		if got := run([]string{sub, "--state-dir", t.TempDir(), "missing"}, &out, &errOut); got != 1 || !strings.Contains(errOut.String(), sub) {
			t.Errorf("%s missing = %d %q", sub, got, errOut.String())
		}
	}
	var out, errOut bytes.Buffer
	if got := run([]string{"transcript", "--count", "x", "--state-dir", t.TempDir(), "missing"}, &out, &errOut); got != 1 {
		t.Errorf("count missing = %d", got)
	}
	root := t.TempDir() + "/not-a-directory"
	if err := os.WriteFile(root, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := run([]string{"plugins", "--store-root", root}, &out, &errOut); got != 1 {
		t.Errorf("plugins broken root = %d", got)
	}
}

func TestLocateOverrideRootLabel(t *testing.T) {
	old := doctorLocate
	t.Cleanup(func() { doctorLocate = old })
	doctorLocate = func(string, string) (doctor.Paths, error) { return doctor.Paths{SessionID: "s"}, nil }
	var out, errOut bytes.Buffer
	if got := run([]string{"locate", "s"}, &out, &errOut); got != 0 || !strings.Contains(out.String(), "(override root)") {
		t.Fatalf("locate = %d %q %q", got, out.String(), errOut.String())
	}
}
