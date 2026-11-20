//go:build linux || darwin

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"primeradiant.com/evener/internal/devtool/procgroup"
)

// targetTiming is one build target's position in a timestamped xcodebuild log.
type targetTiming struct {
	Name     string
	Compiles int
	First    float64
	Last     float64
}

var targetNamePattern = regexp.MustCompile(`in target '([^']+)'`)

// timestampWriter prefixes every complete output line with its elapsed seconds.
// xcodebuild prints a line when a task finishes, so the prefix turns the phase
// log into a timeline of per-target completion even without a result bundle.
type timestampWriter struct {
	mu    sync.Mutex
	w     io.Writer
	start time.Time
	buf   []byte
}

func newTimestampWriter(w io.Writer, start time.Time) *timestampWriter {
	return &timestampWriter{w: w, start: start}
}

func (t *timestampWriter) Write(p []byte) (int, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.buf = append(t.buf, p...)
	for {
		idx := strings.IndexByte(string(t.buf), '\n')
		if idx < 0 {
			return len(p), nil
		}
		line := t.buf[:idx]
		t.buf = t.buf[idx+1:]
		elapsed := time.Since(t.start).Seconds()
		if _, err := fmt.Fprintf(t.w, "%.3f\t%s\n", elapsed, line); err != nil {
			return len(p), err
		}
	}
}

// flush writes any trailing line that never received its newline.
func (t *timestampWriter) flush() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.buf) == 0 {
		return
	}
	fmt.Fprintf(t.w, "%.3f\t%s\n", time.Since(t.start).Seconds(), t.buf)
	t.buf = nil
}

// commandOutputTimed runs args exactly as commandOutput does, but stamps every
// line of the phase log with elapsed seconds.
func (r *runner) commandOutputTimed(ctx context.Context, name string, args ...string) (result error) {
	if err := ctx.Err(); err != nil {
		return err
	}
	logPath := filepath.Join(r.output, name+".log")
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer func() { result = loggerClose(result, log) }()
	timed := newTimestampWriter(log, time.Now())
	fmt.Fprintf(os.Stderr, "iOS qualification: %s (%s)\n", name, logPath)
	cmd := exec.Command(args[0], args[1:]...) //nolint:noctx // Cancellation below stops and reaps the whole owned process group, not just the direct child.
	cmd.Dir = r.native
	cmd.Env = append(os.Environ(), "CI=1", "APPLE_TEAM_ID=EVENERCI01")
	cmd.Stdout, cmd.Stderr = timed, timed
	if err := procgroup.Start(cmd); err != nil {
		return err
	}
	done, reaped := make(chan error, 1), make(chan struct{})
	go func() {
		err := cmd.Wait()
		close(reaped)
		done <- err
	}()
	select {
	case err = <-done:
	case <-ctx.Done():
		procgroup.Stop(cmd.Process.Pid, reaped, 5*time.Second)
		err = errors.Join(ctx.Err(), <-done)
	}
	timed.flush()
	if err != nil {
		return fmt.Errorf("%s failed, inspect %s: %w", name, logPath, err)
	}
	return nil
}

func loggerClose(result error, log *os.File) error {
	return errors.Join(result, log.Close())
}

// parseTargetTimings reads a timestamped phase log and reports, per target,
// how many compile tasks it ran and when its first and last lines landed.
func parseTargetTimings(data []byte) []targetTiming {
	byName := map[string]*targetTiming{}
	for _, line := range strings.Split(string(data), "\n") {
		elapsedText, content, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		elapsed, err := strconv.ParseFloat(elapsedText, 64)
		if err != nil {
			continue
		}
		match := targetNamePattern.FindStringSubmatch(content)
		if match == nil {
			continue
		}
		name := match[1]
		entry, ok := byName[name]
		if !ok {
			entry = &targetTiming{Name: name, First: elapsed}
			byName[name] = entry
		}
		entry.Last = elapsed
		if strings.HasPrefix(content, "CompileC ") || strings.HasPrefix(content, "SwiftCompile ") {
			entry.Compiles++
		}
	}
	rows := make([]targetTiming, 0, len(byName))
	for _, entry := range byName {
		rows = append(rows, *entry)
	}
	sort.Slice(rows, func(i, j int) bool {
		if rows[i].Last != rows[j].Last {
			return rows[i].Last > rows[j].Last
		}
		return rows[i].Name < rows[j].Name
	})
	return rows
}

// buildEnd returns the largest elapsed stamp in a timestamped phase log.
func buildEnd(data []byte) float64 {
	end := 0.0
	for _, line := range strings.Split(string(data), "\n") {
		elapsedText, _, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		elapsed, err := strconv.ParseFloat(elapsedText, 64)
		if err == nil && elapsed > end {
			end = elapsed
		}
	}
	return end
}

// buildTimingReport prints a ranked per-target table for the release build into
// the job log, together with the timing summary and a best-effort read of the
// per-target durations Xcode records in the result bundle.
func (r *runner) buildTimingReport(ctx context.Context, logPath, bundle string) {
	fmt.Fprintln(os.Stderr, "iOS qualification: build-timings")
	data, err := os.ReadFile(logPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "build-timings: cannot read %s: %v\n", logPath, err)
		return
	}
	rows, end := parseTargetTimings(data), buildEnd(data)
	fmt.Fprintf(os.Stderr, "build-timings: build end %.3fs, %d targets\n", end, len(rows))
	fmt.Fprintf(os.Stderr, "%-46s %8s %9s %9s %9s\n", "target", "compiles", "first_s", "last_s", "tail_s")
	for i, row := range rows {
		if i == 40 {
			break
		}
		fmt.Fprintf(os.Stderr, "%-46s %8d %9.1f %9.1f %9.1f\n", row.Name, row.Compiles, row.First, row.Last, end-row.Last)
	}
	printTimingSummary(os.Stderr, data)
	printHoistWindow(os.Stderr, data)
	r.probeBuildResults(ctx, bundle)
}

// printHoistWindow echoes the E16 hoist markers with their elapsed seconds, so
// the tail comparison (hoisted bundle during the compile wave, copy in the app
// target) reads straight out of the job log.
func printHoistWindow(w io.Writer, data []byte) {
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.Contains(line, "E16 metro-") {
			continue
		}
		stamp, text, ok := strings.Cut(line, "\t")
		if !ok {
			continue
		}
		fmt.Fprintf(w, "build-timings: hoist %s %s\n", stamp, strings.TrimSpace(text))
	}
}

// printTimingSummary echoes the -showBuildTimingSummary block into the job log.
func printTimingSummary(w *os.File, data []byte) {
	lines := strings.Split(string(data), "\n")
	for i, line := range lines {
		_, content, ok := strings.Cut(line, "\t")
		if !ok || !strings.Contains(content, "Build Timing Summary") {
			continue
		}
		fmt.Fprintln(w, "build-timings: timing summary")
		for _, follow := range lines[i:min(i+40, len(lines))] {
			_, text, ok := strings.Cut(follow, "\t")
			if !ok {
				continue
			}
			fmt.Fprintf(w, "  %s\n", text)
		}
		return
	}
}

// probeBuildResults asks xcresulttool for the result bundle's per-target
// durations. It never fails the build: a rejection is reported and the
// timestamp table above still answers the question.
func (r *runner) probeBuildResults(ctx context.Context, bundle string) {
	if _, err := os.Stat(bundle); err != nil {
		fmt.Fprintf(os.Stderr, "build-timings: no result bundle at %s: %v\n", bundle, err)
		return
	}
	out, err := exec.CommandContext(ctx, "xcrun", "xcresulttool", "get", "build-results", "--path", bundle, "--format", "json").Output()
	if err != nil {
		fmt.Fprintf(os.Stderr, "build-timings: xcresulttool get build-results failed: %v\n", err)
		help, helpErr := exec.CommandContext(ctx, "xcrun", "xcresulttool", "--help").Output()
		if helpErr == nil {
			for _, line := range strings.Split(string(help), "\n")[:min(30, len(strings.Split(string(help), "\n")))] {
				fmt.Fprintf(os.Stderr, "  help: %s\n", line)
			}
		}
		return
	}
	if writeErr := os.WriteFile(filepath.Join(r.output, "build-results.json"), out, 0o600); writeErr != nil {
		fmt.Fprintf(os.Stderr, "build-timings: cannot keep build-results.json: %v\n", writeErr)
	}
	durations := collectTargetDurations(out)
	fmt.Fprintf(os.Stderr, "build-timings: %d duration entries from xcresulttool\n", len(durations))
	for i, entry := range durations {
		if i == 30 {
			break
		}
		fmt.Fprintf(os.Stderr, "  %-46s %9.2f\n", entry.Name, entry.Seconds)
	}
}

type durationEntry struct {
	Name    string
	Seconds float64
}

var (
	durationKeyPattern = regexp.MustCompile(`(?i)duration|elapsed|walltime|totaltime`)
	targetKeyPattern   = regexp.MustCompile(`(?i)^(target|targetname|name)$`)
)

// collectTargetDurations walks an xcresulttool document and pairs every target
// name it finds with a neighbouring duration, so the report survives schema
// changes instead of pinning one shape.
func collectTargetDurations(data []byte) []durationEntry {
	var document any
	if err := json.Unmarshal(data, &document); err != nil {
		return nil
	}
	var entries []durationEntry
	var walk func(node any)
	walk = func(node any) {
		switch value := node.(type) {
		case map[string]any:
			name, seconds, ok := targetAndDuration(value)
			if ok {
				entries = append(entries, durationEntry{Name: name, Seconds: seconds})
			}
			for _, child := range value {
				walk(child)
			}
		case []any:
			for _, child := range value {
				walk(child)
			}
		}
	}
	walk(document)
	sort.Slice(entries, func(i, j int) bool { return entries[i].Seconds > entries[j].Seconds })
	return entries
}

func targetAndDuration(node map[string]any) (string, float64, bool) {
	name, seconds := "", 0.0
	foundName, foundDuration := false, false
	for key, value := range node {
		switch typed := value.(type) {
		case string:
			if !foundName && targetKeyPattern.MatchString(key) {
				name, foundName = typed, true
			}
		case float64:
			if !foundDuration && durationKeyPattern.MatchString(key) {
				seconds, foundDuration = typed, true
			}
		}
	}
	return name, seconds, foundName && foundDuration && seconds > 0
}
