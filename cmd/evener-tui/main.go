package tui

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-tui/internal/hubstart"
	"primeradiant.com/evener/cmd/evener-tui/internal/tuitheme"
	"primeradiant.com/evener/cmdutil"
)

type tuiProgram interface {
	Run() (tea.Model, error)
	Send(tea.Msg)
}

var (
	exitProcess                     = os.Exit //nolint:unused // swapped in fuzz tests (main_fuzz_test.go)
	processArgs                     = func() []string { return os.Args }
	processExecutable               = os.Executable
	processGetenv                   = os.Getenv
	standardError         io.Writer = os.Stderr
	standardOutput        io.Writer = os.Stdout
	parseStartupOptions             = hubstart.ParseTUIStartupOptions
	ensureUserConfigDirs            = cmdutil.EnsureUserConfigDirs
	startHubClient                  = hubstart.StartHubClient
	probeTerminalDefaults           = tuitheme.ProbeTerminalDefaults
	initThemeFromStateDir           = tuitheme.InitThemeFromStateDir
	applyTerminalBg                 = tuitheme.ApplyTerminalBg
	resetTerminalBg                 = tuitheme.ResetTerminalBg
	newTUIProgram                   = func(model tea.Model, opts ...tea.ProgramOption) tuiProgram {
		return tea.NewProgram(model, opts...)
	}
)

// runConfig carries the injected streams for one TUI invocation. Threading
// them through runWith instead of assigning the package-level stdio seams lets
// Run honor a caller's stdin and keeps concurrent Run calls from racing on
// shared state.
type runConfig struct {
	args   []string
	stdin  io.Reader
	stdout io.Writer
	stderr io.Writer
}

// Run is the library entry point used by the `evener tui` subcommand. args are
// the subcommand arguments without argv[0]; stdin feeds the Bubble Tea program;
// stdout and stderr receive the TUI's own output and diagnostics.
func Run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return runWith(runConfig{
		args:   args,
		stdin:  stdin,
		stdout: stdout,
		stderr: stderr,
	})
}

// run is the process-bound entry point: it reads the process args and the
// package-level stdio seams so the in-package test hooks keep working. Run
// does not go through here.
func run() int {
	args := processArgs()
	if len(args) > 0 {
		args = args[1:]
	}
	return runWith(runConfig{
		args:   args,
		stdout: standardOutput,
		stderr: standardError,
	})
}

func runWith(cfg runConfig) int {
	stdout, stderr := cfg.stdout, cfg.stderr
	startupOpts, err := parseStartupOptions(cfg.args, processGetenv, stderr)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			// Usage has already been printed by the flag package via fs.Usage.
			return 0
		}
		_, _ = fmt.Fprintf(stderr, "evener-tui: %v\n", err)
		return 2
	}
	if err := ensureUserConfigDirs(); err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-tui: %v\n", err)
		return 1
	}

	// Run owns this context. Reconnect backoff and dialing hang off it, so
	// quitting the TUI cancels an in-flight retry instead of letting it dial
	// (and, with autostart on, launch a hub) after the model is gone. A hub that
	// was already launched is a detached child and is not tied to this context.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	hubConfig := hubstart.HubStartConfig{
		RawAddr:           startupOpts.HubAddr,
		HubBin:            startupOpts.HubBin,
		StateDir:          startupOpts.StateDir,
		LogFile:           startupOpts.LogFile,
		AuthToken:         startupOpts.AuthToken,
		CurrentExecutable: currentExecutable(),
		AutoStart:         startupOpts.AutoStartHub,
		HealthTimeout:     5 * time.Second,
	}
	// Every connection is opened the same way, including the ones that replace
	// a dead one: same address, same auth, same autostart — which is what
	// brings back a hub that exited rather than merely blipped. The feed has to
	// exist before the connection does, because it becomes the client's ordered
	// frame handler and that only takes effect installed ahead of the receive
	// loop.
	dialHub := func(ctx context.Context) (hubstart.HubRuntime, *hubFrameFeed, error) {
		frames := newHubFrameFeed()
		config := hubConfig
		config.ObserveFrames = frames.Observe
		runtime, err := startHubClient(ctx, config)
		if err != nil {
			return hubstart.HubRuntime{}, nil, err
		}
		frames.SetTransportCloser(runtime.Client.Close)
		return runtime, frames, nil
	}
	runtime, frames, err := dialHub(ctx)
	if err != nil {
		_, _ = fmt.Fprint(stderr, hubstart.StartupErrorScreen(err))
		return 1
	}

	// Probe the terminal's default fg/bg BEFORE any tuitheme.SetTheme call, so
	// (a) "system" theme detection has cached probe data and (b) the
	// deferred restore on exit can return the exact originals.
	probeTerminalDefaults()
	initThemeFromStateDir(startupOpts.StateDir)
	applyTerminalBg()
	defer resetTerminalBg()

	m := newHubModel(runtime.Client, runtime.Address.BaseURL, startupOpts.StateDir)
	m.lifecycleCtx = ctx
	m.frames = frames
	m.dialHub = func(ctx context.Context) (*appwire.Client, *hubFrameFeed, error) {
		replacement, frames, err := dialHub(ctx)
		return replacement.Client, frames, err
	}
	var programOpts []tea.ProgramOption
	if !startupOpts.Debug {
		programOpts = append(programOpts, tea.WithAltScreen())
	}
	// Route the renderer and the reader to the caller's streams. Bubble Tea
	// already defaults to os.Stdout/os.Stdin, so only a genuinely injected
	// stream needs an explicit option. Leaving os.Stdin alone matters: with
	// customInput Bubble Tea skips the default non-TTY fallback that reopens
	// /dev/tty, so a redirected stdin (evener tui </dev/null) would otherwise
	// lose keyboard input.
	if cfg.stdout != nil && cfg.stdout != io.Writer(os.Stdout) {
		programOpts = append(programOpts, tea.WithOutput(cfg.stdout))
	}
	if cfg.stdin != nil && cfg.stdin != io.Reader(os.Stdin) {
		programOpts = append(programOpts, tea.WithInput(cfg.stdin))
	}
	program := newTUIProgram(m, programOpts...)
	if m.pending != nil {
		m.pending.SetSend(program.Send)
	}
	finalModel, err := program.Run()
	// The model ends on whichever connection it runs on: a reconnect replaces
	// the client dialHub first returned, and it closes the superseded one
	// itself. Close the model's current client on every exit path, so a normal
	// quit does not leave the read loop to the transport's own cleanup.
	defer closeHubClientFromModel(finalModel)
	// Clear the terminal title on every exit. A model-driven quit clears it via
	// quitCmd, but bubbletea's signal handler pushes QuitMsg/InterruptMsg into
	// the event loop without calling Update, so SIGTERM (or SIGINT with stdin
	// not a TTY) would otherwise leave the session title on the terminal after
	// the process exits. The escape is a no-op when the title is already empty.
	_, _ = fmt.Fprint(stdout, "\x1b]2;\x07")
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-tui: %v\n", err)
		return 1
	}
	if message := postQuitMessageFromModel(finalModel); message != "" {
		_, _ = fmt.Fprintln(stdout, message)
	}
	return 0
}

// closeHubClientFromModel releases the hub connection the TUI ended on. The
// model owns the current client; run only borrows it to close it at exit.
func closeHubClientFromModel(model tea.Model) {
	if m, ok := model.(hubModel); ok && m.client != nil {
		_ = m.client.Close()
	}
}

func postQuitMessageFromModel(model tea.Model) string {
	m, ok := model.(hubModel)
	if !ok {
		return ""
	}
	return strings.TrimSpace(m.postQuitMessage)
}

// currentExecutable returns the absolute path of the running evener-tui
// binary. It prefers os.Executable() (always absolute on supported
// platforms) and falls back to os.Args[0] when the OS cannot report a
// path. Returning the absolute path lets binresolve.Resolve locate a
// sibling evener-hub even when evener-tui was launched via a relative path
// like "./evener-tui" — which would otherwise be rejected by exec.ErrDot.
func currentExecutable() string {
	if exe, err := processExecutable(); err == nil && exe != "" {
		return exe
	}
	if args := processArgs(); len(args) > 0 {
		return args[0]
	}
	return ""
}
