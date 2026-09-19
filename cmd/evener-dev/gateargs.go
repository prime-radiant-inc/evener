package dev

// check-gate-flags validates the test gate's caller argv and effective GOFLAGS
// before any module runs, and root-test-flags prints the same argv with short
// mode removed for the root module under ROOT_FULL.
//
// Both exist so the gate does not carry a second flag parser in shell. They walk
// the arguments with walkFlags, the shared value-aware walker shardplan.go
// already uses: it normalises spellings (-test.run is -run, --tags is -tags),
// consumes each value with the full tables, and fails on a flag whose value is
// missing rather than indexing past it. goflagsEntries is Go's own quoted.Split,
// so GOFLAGS is read the way go reads it.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
)

// errRefused stops a walk at the first token the caller wants to report.
var errRefused = errors.New("refused")

func checkGateFlagsMain(args []string) int {
	return checkGateFlags(args, os.Stdout, os.Stderr)
}

func checkGateFlags(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("check-gate-flags", flag.ContinueOnError)
	fs.SetOutput(stderr)
	goflags := fs.String("goflags", "", "the effective GOFLAGS value to validate")
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, "usage: evener-dev check-gate-flags --goflags <GOFLAGS> -- <go test flags...>\n\n"+
			"Fails if the gate cannot carry the caller's flags: a value-taking flag with\n"+
			"no value, a flag terminator (-args, --), or a -C anywhere, including inside\n"+
			"GOFLAGS under Go's own quoting.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	// GOFLAGS is applied to every go command the gate runs, so a -C there moves
	// the enumeration and the tests out of the module directory alike.
	if name, found, err := firstRefusedArg(goflagsEntries(*goflags), false); err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-dev check-gate-flags: GOFLAGS: %v\n", err)
		return 2
	} else if found {
		_, _ = fmt.Fprintf(stderr, "evener-dev check-gate-flags: GOFLAGS carries %s, which would move the gate out of the module directory it anchors; remove it\n", name)
		return 2
	}

	name, found, err := firstRefusedArg(fs.Args(), true)
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-dev check-gate-flags: %v\n", err)
		return 2
	}
	if found {
		if name == "-C" {
			_, _ = fmt.Fprintln(stderr, "evener-dev check-gate-flags: -C is not supported: the gate anchors each module's enumeration and its tests to that module's own directory")
		} else {
			_, _ = fmt.Fprintf(stderr, "evener-dev check-gate-flags: %s ends flag parsing, which this gate cannot honour: it appends its own -run/-skip filters and package list after the caller's flags\n", name)
		}
		return 2
	}
	return 0
}

// firstRefusedArg walks a `go test` argument list with the shared value-aware
// walker and reports the first -C or, when terminators are refused, the first
// -args/-- . A walk error (a flag whose value is missing) is returned too, so
// the caller reports it instead of a later command crashing on it.
func firstRefusedArg(args []string, refuseTerminators bool) (string, bool, error) {
	var found string
	err := walkFlags(args, func(tok flagToken) error {
		if tok.name == "-C" {
			found = tok.name
			return errRefused
		}
		if refuseTerminators && (tok.name == "-args" || tok.name == "--") {
			found = tok.name
			return errRefused
		}
		return nil
	})
	if errors.Is(err, errRefused) {
		return found, true, nil
	}
	if err != nil {
		return "", false, err
	}
	return "", false, nil
}

// rootTestFlagsMain prints the caller's `go test` flags with short mode removed,
// one per line, for the root module under ROOT_FULL.
func rootTestFlagsMain(args []string) int {
	return rootTestFlags(args, os.Stdout, os.Stderr)
}

// errWriteFailed stops the walk once an output write has failed.
var errWriteFailed = errors.New("write failed")

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
	write := func(s string) error {
		line := s + "\n"
		n, err := io.WriteString(stdout, line)
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "evener-dev root-test-flags: writing flags: %v\n", err)
			return errWriteFailed
		}
		return nil
	}
	// walkFlags consumes each value with the shared tables and fails on a flag
	// whose value is missing, so a value that spells -short is emitted as the
	// value it is and a dangling flag is a usage error, not a panic.
	err := walkFlags(fs.Args(), func(tok flagToken) error {
		if tok.name == "-short" {
			// A bare or inline boolean: dropping it drops short mode, and it
			// takes no separate value to keep.
			return nil
		}
		// Every token and value here is handed back one per line, so neither can
		// carry a newline.
		if err := checkValue(tok.name, tok.whole); err != nil {
			return err
		}
		if err := write(tok.whole); err != nil {
			return err
		}
		if tok.hasValue && !tok.inline {
			if err := checkValue(tok.name, tok.value); err != nil {
				return err
			}
			if err := write(tok.value); err != nil {
				return err
			}
		}
		return nil
	})
	if errors.Is(err, errWriteFailed) {
		return 1
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-dev root-test-flags: %v\n", err)
		return 2
	}
	return 0
}
