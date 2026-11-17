package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"strconv"
	"strings"
	"testing"

	"primeradiant.com/evener/envvars"
)

// goGCTargetPercent reads the target the collector is actually pacing against,
// which is the value tuneGCTarget sets. GOGC=off reports as a negative
// percentage, which this metric carries as the largest uint64.
func goGCTargetPercent(t *testing.T) int {
	t.Helper()
	const name = "/gc/gogc:percent"
	all := metrics.All()
	var samples []metrics.Sample
	for _, d := range all {
		if d.Name == name {
			samples = append(samples, metrics.Sample{Name: d.Name})
		}
	}
	if len(samples) == 0 {
		t.Fatalf("runtime does not report %s", name)
	}
	metrics.Read(samples)
	v := samples[0].Value
	if v.Kind() != metrics.KindUint64 {
		t.Fatalf("%s has kind %v, want %v", name, v.Kind(), metrics.KindUint64)
	}
	return int(v.Uint64())
}

// tuneGCTarget must do nothing to a process whose target an operator already
// moved. GOGC is in effect before main runs, so its value is what the
// collector reports back as the previous target: leaving a non-default one
// alone is the difference between tuning the daemon and blowing up the RSS of
// the memory-constrained host that set GOGC on purpose.
//
// Not parallel: each case sets the variable for the process and reads it
// through the registry row the production code reads.
func TestTuneGCTargetRaisesTheCollectorOnlyWhenUntuned(t *testing.T) {
	cases := []struct {
		name    string
		current int
		want    int
		calls   []int
		// gogc is the value the operator exported, or the zero value for a
		// genuinely unset variable, which is what the tuning is for.
		gogc    string
		gogcSet bool
	}{
		{
			name:    "Go's default target is raised",
			current: gcDefaultPercent,
			want:    gcTargetPercent,
			calls:   []int{gcTargetPercent},
		},
		{
			name:    "an operator's GOGC is kept",
			current: 50,
			want:    50,
			calls:   nil,
			gogc:    "50",
			gogcSet: true,
		},
		{
			name:    "GOGC=off stays off",
			current: -1,
			want:    -1,
			calls:   nil,
			gogc:    "off",
			gogcSet: true,
		},
		{
			name:    "an operator's explicit GOGC=100 is kept",
			current: gcDefaultPercent,
			want:    gcDefaultPercent,
			calls:   nil,
			gogc:    "100",
			gogcSet: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setGoGC(t, tc.gogc, tc.gogcSet)
			current := tc.current
			var calls []int
			if err := tuneGCTarget(func(percent int) int {
				calls = append(calls, percent)
				previous := current
				current = percent
				return previous
			}); err != nil {
				t.Fatalf("tuneGCTarget: %v", err)
			}
			if current != tc.want {
				t.Errorf("collector target = %d, want %d", current, tc.want)
			}
			if !slices.Equal(calls, tc.calls) {
				t.Errorf("SetGCPercent calls = %v, want %v", calls, tc.calls)
			}
		})
	}
}

// A GOGC the collector would not honor is refused before the process runs, and
// the collector is never moved: a typo must not be read as Go's default 100
// while evener behaves as though nobody configured it, which is what the
// runtime's own parse does with an unparseable value. The sentinel is matched
// with errors.Is, the class rather than the wording.
func TestTuneGCTargetRefusesAValueTheCollectorWouldNotHonor(t *testing.T) {
	for _, value := range []string{"banana", "150%", "1e3", " 100", "100 ", "", "-0x10"} {
		t.Run(value, func(t *testing.T) {
			setGoGC(t, value, true)
			called := false
			err := tuneGCTarget(func(int) int { called = true; return gcDefaultPercent })
			if !errors.Is(err, errInvalidGOGC) {
				t.Fatalf("tuneGCTarget(%q) error = %v, want errInvalidGOGC", value, err)
			}
			if called {
				t.Errorf("SetGCPercent called for a refused %q; the collector must not move on a value the process refuses", value)
			}
		})
	}
}

// Every value Go's own startup read accepts is accepted here, so validation
// never refuses a GOGC the collector would have honored. The runtime's read is
// "off" or a base-10 int32, and a negative percentage disables collection.
func TestParseGCPercentAcceptsWhatTheCollectorAccepts(t *testing.T) {
	for _, tc := range []struct {
		value string
		want  int
	}{
		{"off", -1},
		{"0", 0},
		{"100", 100},
		{"200", 200},
		{"-1", -1},
		{"2147483647", 2147483647},
	} {
		got, err := parseGCPercent(tc.value)
		if err != nil || got != tc.want {
			t.Errorf("parseGCPercent(%q) = %d, %v; want %d, nil", tc.value, got, err, tc.want)
		}
	}
}

// The refusal is what an operator sees, so it must exit nonzero before any work
// and leave a diagnostic on stderr. applyGCTuning takes the process's own
// seams, so this drives it exactly as main does.
func TestApplyGCTuningRefusesAMalformedValueAtStartup(t *testing.T) {
	setGoGC(t, "not-a-number", true)
	var stderr bytes.Buffer
	var exits []int
	applyGCTuning(func(int) int { return gcDefaultPercent }, &stderr, func(code int) { exits = append(exits, code) })
	if !slices.Equal(exits, []int{1}) {
		t.Fatalf("exit codes = %v, want [1]: a bad GOGC must stop the process before it runs", exits)
	}
	if stderr.Len() == 0 {
		t.Error("refusal wrote nothing to stderr; the operator gets no clue which variable failed")
	}
}

// applyGCTuning forwards the policy and exits nowhere when the configuration is
// accepted: it is the startup seam, so both arms matter — an unset variable
// still reaches the collector, and an exported one reaches no call at all.
func TestApplyGCTuningForwardsThePolicyWithoutExiting(t *testing.T) {
	cases := []struct {
		name    string
		gogc    string
		gogcSet bool
		calls   []int
	}{
		{name: "unset tunes the collector", calls: []int{gcTargetPercent}},
		{name: "an exported value is left in place", gogc: "300", gogcSet: true, calls: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setGoGC(t, tc.gogc, tc.gogcSet)
			var stderr bytes.Buffer
			var exits []int
			var calls []int
			applyGCTuning(func(percent int) int {
				calls = append(calls, percent)
				return gcDefaultPercent
			}, &stderr, func(code int) { exits = append(exits, code) })
			if len(exits) != 0 {
				t.Fatalf("exit codes = %v, want none", exits)
			}
			if stderr.Len() != 0 {
				t.Errorf("stderr = %q, want nothing", stderr.String())
			}
			if !slices.Equal(calls, tc.calls) {
				t.Errorf("SetGCPercent calls = %v, want %v", calls, tc.calls)
			}
		})
	}
}

// gogcStartupHelperEnv marks the child side of the test below. A test that
// starts the binary under a GOGC cannot read the child's collector target any
// other way, and an in-process test cannot either: the runtime reads GOGC once,
// at startup, so by the time a test runs, an exported value has already been
// applied and can no longer be simulated.
const (
	gogcStartupHelperEnv = "RUNTIME_TUNING_GOGC_HELPER"
	// gogcStartupReportEnv names the file the child writes its target to. A
	// file, not stdout, because the test framework shares stdout with it.
	gogcStartupReportEnv = "RUNTIME_TUNING_GOGC_REPORT"
)

// TestGOGCStartupHelper is the child side. It reports the target the collector
// paces against after evener's startup code has run, which is what "the value
// reaches the process" means for a variable the runtime reads itself.
func TestGOGCStartupHelper(t *testing.T) {
	if os.Getenv(gogcStartupHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	applyGCTuning(debug.SetGCPercent, os.Stderr, os.Exit)
	report := os.Getenv(gogcStartupReportEnv)
	if err := os.WriteFile(report, []byte(strconv.Itoa(goGCTargetPercent(t))), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGOGCFromTheEnvironmentIsWhatTheCollectorPacesAgainst pins the knob end to
// end: a GOGC exported for the process is the target the collector runs with
// after evener's startup, and an absent GOGC still lands on evener's own 200.
// The child is this test binary re-executed, so the runtime's real startup read
// is in play rather than a simulated one.
func TestGOGCFromTheEnvironmentIsWhatTheCollectorPacesAgainst(t *testing.T) {
	for _, tc := range []struct {
		name    string
		gogc    string
		gogcSet bool
		want    string
	}{
		{name: "an exported GOGC is the target", gogc: "300", gogcSet: true, want: "300"},
		{name: "GOGC=off disables collection", gogc: "off", gogcSet: true, want: "-1"},
		{name: "an absent GOGC is evener's 200", want: strconv.Itoa(gcTargetPercent)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			report := filepath.Join(t.TempDir(), "gogc-report")
			cmd := exec.Command(os.Args[0], "-test.run=^TestGOGCStartupHelper$")
			// The child inherits this process's environment so it sees the same
			// toolchain and caches; GOGC is then removed first and set only when
			// the case exports one, so a value this test-runner happened to
			// inherit cannot compete with the case's own.
			cmd.Env = append(withoutEnv(os.Environ(), envvars.GoGC.Name),
				gogcStartupHelperEnv+"=1", gogcStartupReportEnv+"="+report)
			if tc.gogcSet {
				cmd.Env = append(cmd.Env, envvars.GoGC.Name+"="+tc.gogc)
			}
			var stdout, stderr bytes.Buffer
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			if err := cmd.Run(); err != nil {
				t.Fatalf("helper: %v\n%s", err, stderr.String())
			}
			written, err := os.ReadFile(report)
			if err != nil {
				t.Fatalf("helper wrote no report: %v\nstdout:\n%s\nstderr:\n%s", err, stdout.String(), stderr.String())
			}
			if got := strings.TrimSpace(string(written)); got != tc.want {
				t.Fatalf("collector target in the child = %q, want %q; stderr:\n%s", got, tc.want, stderr.String())
			}
		})
	}
}

// withoutEnv returns env with the named variable removed, keeping every other
// entry. It is how a case asks for a genuinely absent variable, which no
// "NAME=" entry imitates.
func withoutEnv(env []string, name string) []string {
	prefix := name + "="
	out := env[:0:0]
	for _, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// gogcMainHelperEnv marks the child side of the entry-point refusal test.
const gogcMainHelperEnv = "RUNTIME_TUNING_GOGC_MAIN_HELPER"

// TestGOGCBadValueMainHelper is the child side: the shipped entry point, with
// its own os.Exit, asked to start under whatever GOGC the parent exported.
// --version is the argument because a refused configuration must stop the
// process before any work, so no subcommand has to be real for the test.
func TestGOGCBadValueMainHelper(t *testing.T) {
	if os.Getenv(gogcMainHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	os.Args = []string{"evener", "--version"}
	main()
}

// TestMainRefusesAMalformedGOGC is main's own wiring, not the seam's: with a
// GOGC the collector would not honor, the process exits nonzero and says
// nothing on stdout — it never reaches the version query that would prove the
// refusal came too late.
func TestMainRefusesAMalformedGOGC(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestGOGCBadValueMainHelper$")
	cmd.Env = append(withoutEnv(os.Environ(), envvars.GoGC.Name),
		gogcMainHelperEnv+"=1", envvars.GoGC.Name+"=banana")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("main exited successfully under a malformed GOGC (err = %v); stdout:\n%s", err, stdout.String())
	}
	if got := exitErr.ExitCode(); got != 1 {
		t.Fatalf("exit code = %d, want 1; stderr:\n%s", got, stderr.String())
	}
	if stdout.Len() != 0 {
		t.Fatalf("main wrote %q to stdout before refusing; the refusal must come before the version query", stdout.String())
	}
	if stderr.Len() == 0 {
		t.Error("refusal wrote nothing to stderr; the operator gets no clue which variable failed")
	}
}

// The policy above is a recorder; this proves the production function moves
// the real collector when it is handed the real setter, and leaves an
// operator's target in place afterwards.
//
// Not parallel: it changes the process-wide collector target.
func TestTuneGCTargetMovesTheRealCollectorTarget(t *testing.T) {
	previous := debug.SetGCPercent(gcDefaultPercent)
	t.Cleanup(func() { debug.SetGCPercent(previous) })

	if got := goGCTargetPercent(t); got != gcDefaultPercent {
		t.Fatalf("collector target = %d before tuning, want %d", got, gcDefaultPercent)
	}
	setGoGC(t, "", false)
	if err := tuneGCTarget(debug.SetGCPercent); err != nil {
		t.Fatalf("tuneGCTarget: %v", err)
	}
	if got := goGCTargetPercent(t); got != gcTargetPercent {
		t.Fatalf("collector target = %d after tuning, want %d", got, gcTargetPercent)
	}

	const operatorPercent = 50
	debug.SetGCPercent(operatorPercent)
	setGoGC(t, strconv.Itoa(operatorPercent), true)
	if err := tuneGCTarget(debug.SetGCPercent); err != nil {
		t.Fatalf("tuneGCTarget over an operator's value: %v", err)
	}
	if got := goGCTargetPercent(t); got != operatorPercent {
		t.Fatalf("collector target = %d after tuning over an operator's %d, want %d", got, operatorPercent, operatorPercent)
	}
}

// The tuning is only worth anything if the process entry point runs it, and
// that wiring is what a future edit to main can silently drop. main is the
// shipped binary's own entry, so this drives it exactly as the binary does:
// --version returns before any run, making the assertion about what main did
// on its way in rather than about work a subcommand happened to do.
//
// main reads GOGC from the real environment, so each case runs the entry point
// under the environment it is about. An inherited GOGC is an operator's target
// and main must leave it alone; the variable's absence is what the tuning is
// for. A case that assumed an absent GOGC without clearing it failed whenever
// the caller exported one, which is how this repo runs GC-stress loops.
//
// Not parallel: it replaces os.Args and os.Stdout, edits the environment, and
// moves the process-wide collector target.
func TestMainTunesTheCollectorBeforeDoingAnyWork(t *testing.T) {
	const operatorPercent = 50
	cases := []struct {
		name string
		// gogc is exported when gogcSet is true. GOGC=off and GOGC=100 are
		// cases the operator would also export, but one exported value proves
		// the presence test; the value itself is covered by the table above.
		gogc    string
		gogcSet bool
		from    int
		want    int
	}{
		{
			name: "no operator GOGC tunes the collector",
			from: gcDefaultPercent,
			want: gcTargetPercent,
		},
		{
			name:    "an operator's GOGC is left in place",
			gogc:    "50",
			gogcSet: true,
			from:    operatorPercent,
			want:    operatorPercent,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setGoGC(t, tc.gogc, tc.gogcSet)

			previous := debug.SetGCPercent(tc.from)
			t.Cleanup(func() { debug.SetGCPercent(previous) })

			oldArgs, oldStdout := os.Args, os.Stdout
			r, w, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			os.Args, os.Stdout = []string{"evener", "--version"}, w
			t.Cleanup(func() {
				os.Args, os.Stdout = oldArgs, oldStdout
				_ = r.Close()
			})

			main()
			_ = w.Close()
			if _, err := io.ReadAll(r); err != nil {
				t.Fatalf("read --version output: %v", err)
			}

			if got := goGCTargetPercent(t); got != tc.want {
				t.Fatalf("collector target after main = %d, want %d", got, tc.want)
			}
		})
	}
}

// setGoGC puts exactly the environment each case is about in front of the code
// under test: absent when set is false, otherwise the value as exported. The
// nonempty value goes through t.Setenv, whose restore is what keeps a case from
// leaking into the next; an absent variable needs the t.Setenv-then-Unsetenv
// shape clearOpenAIKey uses, because setting "" is not the same thing.
func setGoGC(t *testing.T, value string, set bool) {
	t.Helper()
	if !set {
		clearGOGC(t)
		return
	}
	t.Setenv(envvars.GoGC.Name, value)
}

// clearGOGC leaves GOGC genuinely unset for the test, so the entry point reads
// the absence its tuning is for rather than whatever the caller exported.
// t.Setenv registers the restore; os.Unsetenv is what "unset" means. This is
// the shape clearOpenAIKey uses for the same reason.
func clearGOGC(t *testing.T) {
	t.Helper()
	t.Setenv(envvars.GoGC.Name, "")
	if err := os.Unsetenv(envvars.GoGC.Name); err != nil {
		t.Fatal(err)
	}
}
