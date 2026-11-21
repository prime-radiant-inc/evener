package main

import (
	"bytes"
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
// An empty GOGC is the one set value that is not an operator's target: the Go
// runtime reads an empty GOGC as unset, so evener treats it the same way.
//
// Not parallel: each case sets the variable for the process.
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
			name:    "an empty GOGC is treated as unset",
			current: gcDefaultPercent,
			want:    gcTargetPercent,
			calls:   []int{gcTargetPercent},
			gogcSet: true,
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
		{
			name:    "a malformed GOGC is the runtime's to handle",
			current: gcDefaultPercent,
			want:    gcDefaultPercent,
			calls:   nil,
			gogc:    "banana",
			gogcSet: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			setGoGC(t, tc.gogc, tc.gogcSet)
			current := tc.current
			var calls []int
			tuneGCTarget(func(percent int) int {
				calls = append(calls, percent)
				previous := current
				current = percent
				return previous
			})
			if current != tc.want {
				t.Errorf("collector target = %d, want %d", current, tc.want)
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
	tuneGCTarget(debug.SetGCPercent)
	report := os.Getenv(gogcStartupReportEnv)
	if err := os.WriteFile(report, []byte(strconv.Itoa(goGCTargetPercent(t))), 0o644); err != nil {
		t.Fatal(err)
	}
}

// TestGOGCFromTheEnvironmentIsWhatTheCollectorPacesAgainst pins the knob end to
// end: a GOGC exported for the process is the target the collector runs with
// after evener's startup, and an absent GOGC still lands on evener's own 200.
// A malformed value is not evener's to refuse: the runtime's silent fallback to
// 100 is what the process runs with, and startup succeeds. The child is this
// test binary re-executed, so the runtime's real startup read is in play rather
// than a simulated one.
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
		{name: "a malformed GOGC falls back like the runtime", gogc: "banana", gogcSet: true, want: strconv.Itoa(gcDefaultPercent)},
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

// gogcMainHelperEnv marks the child side of the entry-point test.
const gogcMainHelperEnv = "RUNTIME_TUNING_GOGC_MAIN_HELPER"

// TestGOGCMainHelper is the child side: the shipped entry point, with its own
// os.Exit, asked to start under whatever GOGC the parent exported. --version is
// the argument because the startup tuning runs before any work, so no subcommand
// has to be real for the test.
func TestGOGCMainHelper(t *testing.T) {
	if os.Getenv(gogcMainHelperEnv) != "1" {
		t.Skip("helper process only")
	}
	os.Args = []string{"evener", "--version"}
	main()
}

// TestMainStartsUnderAMalformedGOGC is main's own wiring, not the seam's: a
// GOGC the runtime cannot parse must not stop evener at startup any more. The
// process reaches the version query and exits zero; the runtime's silent
// fallback to 100 is the only consequence of the bad value.
func TestMainStartsUnderAMalformedGOGC(t *testing.T) {
	cmd := exec.Command(os.Args[0], "-test.run=^TestGOGCMainHelper$")
	cmd.Env = append(withoutEnv(os.Environ(), envvars.GoGC.Name),
		gogcMainHelperEnv+"=1", envvars.GoGC.Name+"=banana")
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("main failed to start under a malformed GOGC: %v\nstderr:\n%s", err, stderr.String())
	}
	if !strings.Contains(stdout.String(), "evener ") {
		t.Fatalf("main did not reach the version query; stdout:\n%s", stdout.String())
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
	tuneGCTarget(debug.SetGCPercent)
	if got := goGCTargetPercent(t); got != gcTargetPercent {
		t.Fatalf("collector target = %d after tuning, want %d", got, gcTargetPercent)
	}

	const operatorPercent = 50
	debug.SetGCPercent(operatorPercent)
	setGoGC(t, strconv.Itoa(operatorPercent), true)
	tuneGCTarget(debug.SetGCPercent)
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
