// Command evener-go-deps compares the Go module graph of a pull request with
// its base and lists added, removed, and version-changed modules with their
// licenses. The CI job fails an unacknowledged change so a reviewer must sign
// off on every new or changed Go dependency (issue #614).
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// module is the subset of `go list -m -json all` this check needs.
type module struct {
	Path    string
	Version string
	Main    bool
	Dir     string
	Replace *module
}

// change is one module whose presence or version differs between base and head.
type change struct {
	Path    string
	Base    string
	Head    string
	License string
}

func main() {
	event := flag.String("event", "", "GitHub event name; only pull_request is checked")
	base := flag.String("base", "", "base git revision to compare against")
	acknowledged := flag.Bool("acknowledged", false, "a reviewer acknowledged the dependency change")
	flag.Parse()

	if *event != "pull_request" {
		fmt.Printf("event %q is not a pull request; no dependency review needed\n", *event)
		return
	}
	if strings.TrimSpace(*base) == "" {
		fail("--base is required on a pull request")
	}
	head, err := listModules(".")
	if err != nil {
		fail("list the head module graph: %v", err)
	}
	baseModules, err := listModulesAt(*base)
	if err != nil {
		fail("list the base module graph at %s: %v", *base, err)
	}
	changes := diffModules(baseModules, head)
	if len(changes) == 0 {
		fmt.Println("no Go module changes against the base")
		return
	}
	summary := renderChanges(*base, changes)
	fmt.Println(summary)
	if summaryPath := os.Getenv("GITHUB_STEP_SUMMARY"); summaryPath != "" {
		if file, err := os.OpenFile(summaryPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); err == nil {
			_, _ = file.WriteString(summary)
			_ = file.Close()
		}
	}
	if !*acknowledged {
		fmt.Println("\nA reviewer must sign off on these Go dependency changes. Ask a reviewer to approve the pull request after reviewing the modules and licenses above; if the repository defines the `go-dependency-reviewed` label, adding it also acknowledges.")
		os.Exit(1)
	}
	fmt.Println("\nGo dependency changes acknowledged by a reviewer label or approval.")
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}

// listModules reads the module graph in dir.
func listModules(dir string) ([]module, error) {
	// A module that no package imports is still in the graph but not in the
	// module cache, so its license file would be unavailable. Downloading the
	// graph is best-effort: a failure leaves those licenses "unknown" while the
	// version diff still decides the check.
	download := exec.Command("go", "mod", "download", "all")
	download.Dir = dir
	download.Env = append(os.Environ(), "GOFLAGS=-mod=readonly")
	_ = download.Run()

	cmd := exec.Command("go", "list", "-m", "-json", "all")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOFLAGS=-mod=readonly")
	out, err := cmd.Output()
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	var modules []module
	decoder := json.NewDecoder(bytes.NewReader(out))
	for {
		var m module
		if err := decoder.Decode(&m); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, fmt.Errorf("decode go list output: %w", err)
		}
		modules = append(modules, m)
	}
	return modules, nil
}

// listModulesAt builds a detached worktree at revision and reads its graph.
func listModulesAt(revision string) ([]module, error) {
	dir, err := os.MkdirTemp("", "evener-go-deps-base-*")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	add := exec.Command("git", "worktree", "add", "--detach", dir, revision)
	if out, err := add.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("git worktree add %s: %w: %s", revision, err, strings.TrimSpace(string(out)))
	}
	defer func() {
		_ = exec.Command("git", "worktree", "remove", "--force", dir).Run()
	}()
	return listModules(dir)
}

// diffModules reports modules added, removed, or changed in identity between
// base and head. A main module (the workspace's local modules) is identified by
// its path alone.
func diffModules(base, head []module) []change {
	baseByPath := make(map[string]module, len(base))
	for _, m := range base {
		baseByPath[m.Path] = m
	}
	headByPath := make(map[string]module, len(head))
	for _, m := range head {
		headByPath[m.Path] = m
	}
	paths := make(map[string]bool, len(baseByPath)+len(headByPath))
	for path := range baseByPath {
		paths[path] = true
	}
	for path := range headByPath {
		paths[path] = true
	}
	ordered := make([]string, 0, len(paths))
	for path := range paths {
		ordered = append(ordered, path)
	}
	sort.Strings(ordered)

	var changes []change
	for _, path := range ordered {
		before, hadBefore := baseByPath[path]
		after, hadAfter := headByPath[path]
		switch {
		case !hadBefore:
			changes = append(changes, change{Path: path, Head: moduleIdentity(after), License: licenseFor(after)})
		case !hadAfter:
			changes = append(changes, change{Path: path, Base: moduleIdentity(before), License: licenseFor(before)})
		case moduleIdentity(before) != moduleIdentity(after):
			changes = append(changes, change{Path: path, Base: moduleIdentity(before), Head: moduleIdentity(after), License: licenseFor(after)})
		}
	}
	return changes
}

// moduleIdentity is the version a reviewer is signing off on, including a
// replacement target because that is the code actually built.
func moduleIdentity(m module) string {
	version := m.Version
	if version == "" {
		version = "(main module)"
	}
	if m.Replace == nil {
		return version
	}
	replacement := m.Replace.Version
	if replacement == "" {
		replacement = m.Replace.Path
	} else {
		replacement = m.Replace.Path + " " + replacement
	}
	return version + " => " + replacement
}

// renderChanges writes a stable, human-readable list for the job log and
// summary.
func renderChanges(base string, changes []change) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Go module graph changed against %s:\n\n", base)
	b.WriteString("| module | base | head | license |\n")
	b.WriteString("| --- | --- | --- | --- |\n")
	for _, c := range changes {
		fmt.Fprintf(&b, "| %s | %s | %s | %s |\n", c.Path, displayVersion(c.Base), displayVersion(c.Head), c.License)
	}
	return b.String()
}

func displayVersion(version string) string {
	if version == "" {
		return "—"
	}
	return version
}

// licenseFor finds the module's license name from its LICENSE/COPYING file.
// The module cache is read-only, so this only reads. An unrecognized or absent
// file is reported as such rather than guessed.
func licenseFor(m module) string {
	if m.Dir == "" {
		return "unknown (module not downloaded)"
	}
	for _, filename := range []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "COPYING", "COPYING.md"} {
		path := filepath.Join(m.Dir, filename)
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		detected := licenseName(string(data))
		if detected != "" {
			return detected
		}
		return "unrecognized (" + filename + ")"
	}
	return "no license file"
}

// licenseName recognizes the common licenses by their text. It is a coarse
// label for the reviewer's list, not a legal determination.
func licenseName(text string) string {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "apache license"):
		return "Apache-2.0"
	case strings.Contains(lower, "mit license"), strings.Contains(lower, "permission is hereby granted, free of charge"):
		return "MIT"
	case strings.Contains(lower, "mozilla public license"):
		return "MPL-2.0"
	case strings.Contains(lower, "gnu general public license"):
		return "GPL"
	case strings.Contains(lower, "redistribution and use in source and binary forms"):
		return "BSD"
	case strings.Contains(lower, "permission to use, copy, modify, and/or distribute"):
		return "ISC"
	default:
		return ""
	}
}
