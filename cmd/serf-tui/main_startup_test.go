package main

import (
	"errors"
	"flag"
	"io"
	"os"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestParseTUIStartupOptionsDefaults(t *testing.T) {
	opts, err := parseTUIStartupOptions(nil, func(string) string { return "" })
	if err != nil {
		t.Fatalf("parseTUIStartupOptions: %v", err)
	}
	if opts.HubAddr != defaultHubAddr {
		t.Fatalf("HubAddr=%q, want %q", opts.HubAddr, defaultHubAddr)
	}
	if !opts.AutoStartHub {
		t.Fatal("AutoStartHub=false, want true")
	}
	if opts.Debug {
		t.Fatal("Debug=true, want false")
	}
}

func TestParseTUIStartupOptionsUsesEnvironmentDefaults(t *testing.T) {
	env := map[string]string{
		"SERF_HUB_ADDR":     "http://env-hub:9180",
		"SERF_HUB_BIN":      "/env/serf-hub",
		"SERF_STATE_DIR":    "/env/state/serf",
		"SERF_TUI_LOG_FILE": "/env/serf-tui.log",
	}
	opts, err := parseTUIStartupOptions(nil, func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("parseTUIStartupOptions: %v", err)
	}
	if opts.HubAddr != env["SERF_HUB_ADDR"] || opts.HubBin != env["SERF_HUB_BIN"] || opts.StateDir != env["SERF_STATE_DIR"] || opts.LogFile != env["SERF_TUI_LOG_FILE"] {
		t.Fatalf("options=%+v", opts)
	}
}

func TestParseTUIStartupOptionsFlagsOverrideEnvironment(t *testing.T) {
	env := map[string]string{
		"SERF_HUB_ADDR":     "http://env-hub:9180",
		"SERF_HUB_BIN":      "/env/serf-hub",
		"SERF_STATE_DIR":    "/env/state/serf",
		"SERF_TUI_LOG_FILE": "/env/serf-tui.log",
	}
	opts, err := parseTUIStartupOptions([]string{
		"--hub-addr", "http://flag-hub:9180",
		"--hub-bin", "/flag/serf-hub",
		"--state-dir", "/flag/state/serf",
		"--log-file", "/flag/serf-tui.log",
		"--no-auto-start-hub",
		"--debug",
	}, func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("parseTUIStartupOptions: %v", err)
	}
	if opts.HubAddr != "http://flag-hub:9180" || opts.HubBin != "/flag/serf-hub" || opts.StateDir != "/flag/state/serf" || opts.LogFile != "/flag/serf-tui.log" {
		t.Fatalf("options=%+v", opts)
	}
	if opts.AutoStartHub {
		t.Fatal("AutoStartHub=true, want false")
	}
	if !opts.Debug {
		t.Fatal("Debug=false, want true")
	}
}

func TestParseTUIStartupOptionsHelpReturnsErrHelp(t *testing.T) {
	_, err := parseTUIStartupOptions([]string{"--help"}, func(string) string { return "" })
	if !errors.Is(err, flag.ErrHelp) {
		t.Fatalf("parseTUIStartupOptions(--help) err = %v, want flag.ErrHelp", err)
	}
}

func TestParseTUIStartupOptionsHelpUsesEnvironmentHubAddr(t *testing.T) {
	origStderr := os.Stderr
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("Pipe: %v", err)
	}
	os.Stderr = w
	t.Cleanup(func() { os.Stderr = origStderr })

	_, parseErr := parseTUIStartupOptions([]string{"--help"}, func(key string) string {
		if key == "SERF_HUB_ADDR" {
			return "http://env-hub:9180"
		}
		return ""
	})
	_ = w.Close()
	out, readErr := io.ReadAll(r)
	if readErr != nil {
		t.Fatalf("ReadAll: %v", readErr)
	}
	if !errors.Is(parseErr, flag.ErrHelp) {
		t.Fatalf("parseTUIStartupOptions(--help) err = %v, want flag.ErrHelp", parseErr)
	}
	if got := string(out); !strings.Contains(got, "default: http://env-hub:9180") {
		t.Fatalf("help output missing env hub addr default:\n%s", got)
	}
}

func TestPostQuitMessageFromHubModel(t *testing.T) {
	m := newSessionHubModel(nil)
	m.postQuitMessage = "Restore this session: serf-tui --hub-addr http://hub.test, then open local:01SEND"

	got := postQuitMessageFromModel(m)
	if got != m.postQuitMessage {
		t.Fatalf("postQuitMessageFromModel()=%q, want %q", got, m.postQuitMessage)
	}
}

func TestPostQuitMessageIgnoresOtherModels(t *testing.T) {
	if got := postQuitMessageFromModel(dummyTeaModel{}); got != "" {
		t.Fatalf("postQuitMessageFromModel()=%q, want empty", got)
	}
}

type dummyTeaModel struct{}

func (dummyTeaModel) Init() tea.Cmd {
	return nil
}

func (dummyTeaModel) Update(tea.Msg) (tea.Model, tea.Cmd) {
	return dummyTeaModel{}, nil
}

func (dummyTeaModel) View() string {
	return ""
}
