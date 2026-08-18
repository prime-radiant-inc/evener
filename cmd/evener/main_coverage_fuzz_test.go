//go:build serffuzz

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"
)

func FuzzMainSeedCoverage(f *testing.F) {
	f.Add(uint8(0))
	f.Fuzz(func(t *testing.T, _ uint8) {
		t.Run("main", testMainEntrypoint)
		t.Run("main branches", testMainWithDepsBranches)
		t.Run("dispatch injected", testDispatchCLICommandWith)
		t.Run("dispatch defaults", testDispatchCLICommandDefaults)
	})
}

func baseMainDeps() (mainDeps, *bytes.Buffer, *bytes.Buffer, *[]int) {
	stdout, stderr := &bytes.Buffer{}, &bytes.Buffer{}
	exits := []int{}
	return mainDeps{
		stdin: strings.NewReader(""), stdout: stdout, stderr: stderr,
		stdinMode: func() (os.FileMode, error) { return 0, nil },
		exit:      func(code int) { exits = append(exits, code) },
		dispatch: func([]string, io.Reader, io.Writer, io.Writer) (bool, string, error) {
			return false, "", nil
		},
		startCPU:   func(string) (func(), error) { return func() {}, nil },
		startTrace: func(string) (func(), error) { return func() {}, nil },
		notify: func(ctx context.Context, _ ...os.Signal) (context.Context, context.CancelFunc) {
			return context.WithCancel(ctx)
		},
		run: func(context.Context, runConfig) error { return nil },
	}, stdout, stderr, &exits
}

func testMainEntrypoint(t *testing.T) {
	oldArgs, oldStdout := os.Args, os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Args, os.Stdout = []string{"serf", "--version"}, w
	t.Cleanup(func() { os.Args, os.Stdout = oldArgs, oldStdout })
	main()
	_ = w.Close()
	got, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(got), "serf ") {
		t.Fatalf("version output = %q", got)
	}

	deps := defaultMainDeps()
	if _, err := deps.stdinMode(); err != nil {
		t.Fatalf("stdinMode: %v", err)
	}
	closed, err := os.CreateTemp(t.TempDir(), "closed-stdin")
	if err != nil {
		t.Fatal(err)
	}
	if err := closed.Close(); err != nil {
		t.Fatal(err)
	}
	if mode, err := defaultMainDepsWithStdin(closed).stdinMode(); err == nil || mode != 0 {
		t.Fatalf("closed stdin mode = %v, %v", mode, err)
	}
}

func testMainWithDepsBranches(t *testing.T) {
	for _, tc := range []struct {
		name     string
		edit     func(*mainDeps, *int)
		wantExit []int
	}{
		{name: "version", edit: func(d *mainDeps, _ *int) { d.args = []string{"--version"} }},
		{name: "dispatch success", edit: func(d *mainDeps, _ *int) {
			d.dispatch = func([]string, io.Reader, io.Writer, io.Writer) (bool, string, error) { return true, "x", nil }
		}},
		{name: "dispatch help", edit: func(d *mainDeps, _ *int) {
			d.dispatch = func([]string, io.Reader, io.Writer, io.Writer) (bool, string, error) { return true, "x", flag.ErrHelp }
		}},
		{name: "dispatch error", edit: func(d *mainDeps, _ *int) {
			d.dispatch = func([]string, io.Reader, io.Writer, io.Writer) (bool, string, error) {
				return true, "x", errors.New("bad")
			}
		}, wantExit: []int{1}},
		{name: "parse help", edit: func(d *mainDeps, _ *int) { d.args = []string{"--help"} }},
		{name: "parse error", edit: func(d *mainDeps, _ *int) { d.args = []string{"--not-a-flag"} }, wantExit: []int{2}},
		{name: "cpu error", edit: func(d *mainDeps, _ *int) {
			d.args = []string{"--cpu-profile=x", "prompt"}
			d.startCPU = func(string) (func(), error) { return nil, errors.New("cpu") }
		}, wantExit: []int{1}},
		{name: "trace error", edit: func(d *mainDeps, stops *int) {
			d.args = []string{"--cpu-profile=x", "--trace=y", "prompt"}
			d.startCPU = func(string) (func(), error) { return func() { *stops++ }, nil }
			d.startTrace = func(string) (func(), error) { return nil, errors.New("trace") }
		}, wantExit: []int{1}},
		{name: "missing prompt", edit: func(d *mainDeps, _ *int) {}, wantExit: []int{1}},
		{name: "run success profiles", edit: func(d *mainDeps, stops *int) {
			d.args = []string{"--cpu-profile=x", "--trace=y", "prompt"}
			d.startCPU = func(string) (func(), error) { return func() { *stops++ }, nil }
			d.startTrace = func(string) (func(), error) { return func() { *stops++ }, nil }
		}},
		{name: "run error resume", edit: func(d *mainDeps, _ *int) {
			d.args = []string{"--list-sessions"}
			d.stdinMode = func() (os.FileMode, error) { return os.ModeCharDevice, errors.New("ignored") }
			d.run = func(context.Context, runConfig) error { return errors.New("run") }
		}, wantExit: []int{1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deps, _, _, exits := baseMainDeps()
			stops := 0
			tc.edit(&deps, &stops)
			mainWithDeps(deps)
			if !equalInts(*exits, tc.wantExit) {
				t.Fatalf("exits = %v, want %v", *exits, tc.wantExit)
			}
			if tc.name == "trace error" && stops != 0 {
				t.Fatalf("stops = %d, want 0", stops)
			}
			if tc.name == "run success profiles" && stops != 2 {
				t.Fatalf("stops = %d, want 2", stops)
			}
		})
	}
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func testDispatchCLICommandWith(t *testing.T) {
	called := ""
	runners := cliCommandRunners{
		serve: func(args []string) error { called = "serve:" + strings.Join(args, ","); return nil },
		launchCheck: func(args []string, _ io.Reader, _, _ io.Writer) error {
			called = "launch-check:" + strings.Join(args, ",")
			return nil
		},
		openAI: func(args []string, _ io.Reader, _, _ io.Writer) error {
			called = "openai:" + strings.Join(args, ",")
			return nil
		},
		upgrade: func(args []string, _ io.Reader, _, _ io.Writer) error {
			called = "upgrade:" + strings.Join(args, ",")
			return nil
		},
		plugin: func(args []string, _ io.Reader, _, _ io.Writer) error {
			called = "plugin:" + strings.Join(args, ",")
			return nil
		},
	}
	for _, command := range []string{"serve", "launch-check", "openai", "upgrade", "plugin"} {
		handled, label, err := dispatchCLICommandWith([]string{command, "arg"}, strings.NewReader(""), io.Discard, io.Discard, runners)
		if err != nil || !handled || label != "serf "+command || called != command+":arg" {
			t.Fatalf("%s: handled=%v label=%q err=%v called=%q", command, handled, label, err, called)
		}
	}
	for _, args := range [][]string{nil, {"unknown"}} {
		handled, label, err := dispatchCLICommandWith(args, strings.NewReader(""), io.Discard, io.Discard, runners)
		if err != nil || handled || label != "" {
			t.Fatalf("args %v: handled=%v label=%q err=%v", args, handled, label, err)
		}
	}
}

func testDispatchCLICommandDefaults(t *testing.T) {
	for _, args := range [][]string{{"launch-check", "--help"}, {"openai", "--help"}, {"upgrade", "--bad"}, {"plugin", "--help"}} {
		handled, _, _ := dispatchCLICommand(args, strings.NewReader(""), io.Discard, io.Discard)
		if !handled {
			t.Fatalf("%v not handled", args)
		}
	}
}
