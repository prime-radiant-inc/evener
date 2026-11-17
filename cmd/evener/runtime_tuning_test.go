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
			calls:   []int{gcTargetPercent, 50},
		},
		{
			name:    "GOGC=off stays off",
			current: -1,
			want:    -1,
			calls:   []int{gcTargetPercent, -1},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
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
	tuneGCTarget(debug.SetGCPercent)
	if got := goGCTargetPercent(t); got != gcTargetPercent {
		t.Fatalf("collector target = %d after tuning, want %d", got, gcTargetPercent)
	}

	const operatorPercent = 50
	debug.SetGCPercent(operatorPercent)
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
// Not parallel: it replaces os.Args and os.Stdout and moves the process-wide
// collector target.
func TestMainTunesTheCollectorBeforeDoingAnyWork(t *testing.T) {
	previous := debug.SetGCPercent(gcDefaultPercent)
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

	if got := goGCTargetPercent(t); got != gcTargetPercent {
		t.Fatalf("collector target after main = %d, want %d", got, gcTargetPercent)
	}
}
