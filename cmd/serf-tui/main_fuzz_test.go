//go:build serffuzz

package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"strings"
	"sync"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/serf/cmd/serf-tui/internal/hubstart"
)

type scriptedTUIProgram struct {
	model tea.Model
	err   error
	sent  bool
}

func (p *scriptedTUIProgram) Run() (tea.Model, error) { return p.model, p.err }
func (p *scriptedTUIProgram) Send(tea.Msg)            { p.sent = true }

func FuzzRootTUIMain(f *testing.F) {
	for selector := byte(0); selector < 10; selector++ {
		f.Add(selector)
	}
	f.Fuzz(func(t *testing.T, selector byte) {
		if selector%10 == 9 {
			testRootTUIMainAndExecutableBoundaries(t)
			return
		}
		if selector%10 == 8 {
			// Construction does not start terminal I/O; Run remains fully faked.
			_ = processArgs()
			warmModelCatalog()
			_ = newTUIProgram(hubModel{})
		}
		var stderr, stdout bytes.Buffer
		var reset, applied, probed, initialized, altScreen, sent bool
		var warm sync.WaitGroup
		warm.Add(1)
		parseErr := error(nil)
		dirsErr := error(nil)
		hubErr := error(nil)
		programErr := error(nil)
		opts := hubstart.TUIStartupOptions{StateDir: "state", Debug: selector%2 == 0}
		switch selector % 9 {
		case 0:
			parseErr = flag.ErrHelp
		case 1:
			parseErr = errors.New("parse")
		case 2:
			dirsErr = errors.New("dirs")
		case 3:
			hubErr = errors.New("hub")
		case 4:
			programErr = errors.New("program")
		}

		oldExit, oldArgs, oldExe, oldGetenv := exitProcess, processArgs, processExecutable, processGetenv
		oldErr, oldOut := standardError, standardOutput
		oldParse, oldDirs, oldWarm, oldStart := parseStartupOptions, ensureUserConfigDirs, warmModelCatalog, startHubClient
		oldProbe, oldInit, oldApply, oldReset, oldProgram := probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram
		t.Cleanup(func() {
			exitProcess, processArgs, processExecutable, processGetenv = oldExit, oldArgs, oldExe, oldGetenv
			standardError, standardOutput = oldErr, oldOut
			parseStartupOptions, ensureUserConfigDirs, warmModelCatalog, startHubClient = oldParse, oldDirs, oldWarm, oldStart
			probeTerminalDefaults, initThemeFromStateDir, applyTerminalBg, resetTerminalBg, newTUIProgram = oldProbe, oldInit, oldApply, oldReset, oldProgram
		})
		processArgs = func() []string { return []string{"serf-tui", "--fixture"} }
		processGetenv = func(string) string { return "fixture" }
		standardError, standardOutput = &stderr, &stdout
		parseStartupOptions = func(args []string, getenv func(string) string) (hubstart.TUIStartupOptions, error) {
			if len(args) != 1 || getenv("x") != "fixture" {
				t.Fatal("process inputs not forwarded")
			}
			return opts, parseErr
		}
		ensureUserConfigDirs = func() error { return dirsErr }
		warmModelCatalog = func() { warm.Done() }
		startHubClient = func(context.Context, hubstart.HubStartConfig) (hubstart.HubRuntime, error) {
			return hubstart.HubRuntime{Address: hubstart.HubAddress{BaseURL: "http://fixture"}}, hubErr
		}
		probeTerminalDefaults = func() bool { probed = true; return true }
		initThemeFromStateDir = func(state string) { initialized = state == "state" }
		applyTerminalBg = func() { applied = true }
		resetTerminalBg = func() { reset = true }
		newTUIProgram = func(model tea.Model, programOpts ...tea.ProgramOption) tuiProgram {
			altScreen = len(programOpts) == 1
			m := model.(hubModel)
			m.postQuitMessage = "  goodbye  "
			p := &scriptedTUIProgram{model: m, err: programErr}
			if m.pending != nil {
				m.pending.SetSend(func(tea.Msg) { p.sent = true })
			}
			return p
		}

		got := run()
		want := 0
		switch selector % 9 {
		case 1:
			want = 2
		case 2, 3, 4:
			want = 1
		}
		if got != want {
			t.Fatalf("run()=%d want %d", got, want)
		}
		if selector%9 >= 4 {
			warm.Wait()
		}
		if selector%9 >= 4 && (!probed || !initialized || !applied || !reset) {
			t.Fatal("theme lifecycle incomplete")
		}
		if selector%9 >= 4 && altScreen == opts.Debug {
			t.Fatal("alternate-screen option mismatch")
		}
		_ = sent
		if selector%9 >= 5 && strings.TrimSpace(stdout.String()) != "goodbye" {
			t.Fatalf("stdout=%q", stdout.String())
		}
		if selector%9 == 4 && !strings.Contains(stderr.String(), "program") {
			t.Fatalf("stderr=%q", stderr.String())
		}
	})
}

func TestRootTUIMainAndExecutableBoundaries(t *testing.T) {
	testRootTUIMainAndExecutableBoundaries(t)
}

func testRootTUIMainAndExecutableBoundaries(t *testing.T) {
	oldExit, oldArgs, oldExe, oldParse := exitProcess, processArgs, processExecutable, parseStartupOptions
	t.Cleanup(func() {
		exitProcess, processArgs, processExecutable, parseStartupOptions = oldExit, oldArgs, oldExe, oldParse
	})
	processArgs = func() []string { return nil }
	parseStartupOptions = func([]string, func(string) string) (hubstart.TUIStartupOptions, error) {
		return hubstart.TUIStartupOptions{}, flag.ErrHelp
	}
	exited := -1
	exitProcess = func(code int) { exited = code }
	main()
	if exited != 0 {
		t.Fatalf("exit=%d", exited)
	}

	processExecutable = func() (string, error) { return "/bin/serf-tui", nil }
	if got := currentExecutable(); got != "/bin/serf-tui" {
		t.Fatal(got)
	}
	processExecutable = func() (string, error) { return "", nil }
	processArgs = func() []string { return []string{"relative"} }
	if got := currentExecutable(); got != "relative" {
		t.Fatal(got)
	}
	processExecutable = func() (string, error) { return "ignored", errors.New("no executable") }
	processArgs = func() []string { return nil }
	if got := currentExecutable(); got != "" {
		t.Fatal(got)
	}

	if got := postQuitMessageFromModel(nil); got != "" {
		t.Fatal(got)
	}
}
