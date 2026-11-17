package main

import (
	"io"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"slices"
	"testing"
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
func TestTuneGCTargetRaisesTheCollectorOnlyWhenUntuned(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		current int
		want    int
		calls   []int
		// gogcSet says the operator exported GOGC. Its value is then theirs
		// even when it names Go's own default, because that is how an operator
		// opts out of this tuning on a memory-tight host.
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
			gogcSet: true,
		},
		{
			name:    "GOGC=off stays off",
			current: -1,
			want:    -1,
			calls:   nil,
			gogcSet: true,
		},
		{
			name:    "an operator's explicit GOGC=100 is kept",
			current: gcDefaultPercent,
			want:    gcDefaultPercent,
			calls:   nil,
			gogcSet: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			current := tc.current
			var calls []int
			tuneGCTarget(tc.gogcSet, func(percent int) int {
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
	tuneGCTarget(false, debug.SetGCPercent)
	if got := goGCTargetPercent(t); got != gcTargetPercent {
		t.Fatalf("collector target = %d after tuning, want %d", got, gcTargetPercent)
	}

	const operatorPercent = 50
	debug.SetGCPercent(operatorPercent)
	tuneGCTarget(true, debug.SetGCPercent)
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
			if tc.gogcSet {
				t.Setenv("GOGC", tc.gogc)
			} else {
				clearGOGC(t)
			}

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

// clearGOGC leaves GOGC genuinely unset for the test, so the entry point reads
// the absence its tuning is for rather than whatever the caller exported.
// t.Setenv registers the restore; os.Unsetenv is what "unset" means. This is
// the shape clearOpenAIKey uses for the same reason.
func clearGOGC(t *testing.T) {
	t.Helper()
	t.Setenv("GOGC", "")
	if err := os.Unsetenv("GOGC"); err != nil {
		t.Fatal(err)
	}
}
