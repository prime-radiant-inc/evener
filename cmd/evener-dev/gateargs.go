package dev

// check-gate-flags validates the test gate's caller argv and effective GOFLAGS
// before any module runs.
//
// It exists so the gate does not carry a second flag parser in shell. The gate
// appends its own -run/-skip filters and package list after the caller's flags,
// so a caller flag that ends flag parsing (or a -C that moves the command out of
// the module directory the gate anchored) must be refused. Detecting either
// correctly means knowing which flags take a value -- `-run -args` is a regex;
// `-args` alone is a terminator -- and GOFLAGS needs Go's own quoting rules.
// Both answers already live here: consumesValue is the shared value-taking
// table (and goFlag the shared spelling), and goflagsEntries is Go's
// quoted.Split, so this is the same knowledge, not a copy of the tables.

import (
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

func checkGateFlagsMain(args []string) int {
	return checkGateFlags(args, os.Stdout, os.Stderr)
}

// rootTestFlagsMain prints the caller's `go test` flags with short mode removed,
// one per line, for the root module under ROOT_FULL. It is value-aware, so a
// value that happens to spell -short (`-run -short`) is kept, and it normalises
// spellings, so -short=true and -test.short are removed too.
func rootTestFlagsMain(args []string) int {
	return rootTestFlags(args, os.Stdout, os.Stderr)
}

func rootTestFlags(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("root-test-flags", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, "usage: evener-dev root-test-flags -- <go test flags...>\n\n"+
			"Prints the flags with short mode (-short, -short=true, -test.short) removed,\n"+
			"one per line, without mistaking a value that spells -short for the flag.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	rest := fs.Args()
	for i := 0; i < len(rest); i++ {
		whole := goFlag(rest[i])
		name := whole
		inline := false
		if j := strings.IndexByte(name, '='); j > 0 {
			name = name[:j]
			inline = true
		}
		if name == "-short" {
			continue
		}
		_, _ = fmt.Fprintln(stdout, whole)
		if takesValue(name) && !inline {
			i++
			_, _ = fmt.Fprintln(stdout, rest[i])
		}
	}
	return 0
}

func checkGateFlags(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check-gate-flags", flag.ContinueOnError)
	fs.SetOutput(stderr)
	goflags := fs.String("goflags", "", "the effective GOFLAGS value to validate")
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, "usage: evener-dev check-gate-flags --goflags <GOFLAGS> -- <go test flags...>\n\n"+
			"Fails if the gate cannot carry the caller's flags: a flag terminator\n"+
			"(-args, --), or a -C anywhere, including inside GOFLAGS under Go's own\n"+
			"quoting.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// GOFLAGS is applied to every go command the gate runs, so a -C there moves
	// the enumeration and the tests out of the module directory alike. It is
	// split with Go's own quoting before the walk.
	if name, found := firstRefusedArg(goflagsEntries(*goflags), false); found {
		_, _ = fmt.Fprintf(stderr, "evener-dev check-gate-flags: GOFLAGS carries %s, which would move the gate out of the module directory it anchors; remove it\n", name)
		return 2
	}

	if name, found := firstRefusedArg(fs.Args(), true); found {
		switch name {
		case "-C":
			_, _ = fmt.Fprintln(stderr, "evener-dev check-gate-flags: -C is not supported: the gate anchors each module's enumeration and its tests to that module's own directory")
		default:
			_, _ = fmt.Fprintf(stderr, "evener-dev check-gate-flags: %s ends flag parsing, which this gate cannot honour: it appends its own -run/-skip filters and package list after the caller's flags\n", name)
		}
		return 2
	}
	return 0
}

// firstRefusedArg walks a `go test` argument list once, consuming the value of
// every flag that takes one (the shared consumesValue table) so a value that
// happens to spell -C or -args is not read as a flag, and reports the first -C
// or, when terminators are refused, the first -args/-- .
func firstRefusedArg(args []string, refuseTerminators bool) (string, bool) {
	for i := 0; i < len(args); i++ {
		whole := goFlag(args[i])
		name := whole
		inline := false
		if j := strings.IndexByte(name, '='); j > 0 {
			name = name[:j]
			inline = true
		}
		if name == "-C" {
			return name, true
		}
		if refuseTerminators && (name == "-args" || name == "--") {
			return name, true
		}
		// Only a value written as the next argument is consumed; an inline
		// value (`-count=1`) must not swallow the flag after it. takesValue
		// includes -tags, which consumesValue leaves out because
		// packageSelectionFlags handles that flag by name.
		if takesValue(name) && !inline {
			i++
		}
	}
	return "", false
}
