package dev

// list-build-flags answers one question for the test gate: which of the
// caller's `go test` flags must also be given to the `go list` that enumerates
// the packages to test.
//
// The answer matters because `go list` and `go test` do not see the same tree.
// -tags selects files, and so can select whole packages; -race, -msan and -asan
// each set a build tag of their own (race, msan, asan); -overlay can add or
// replace a package's files; -mod, -modfile and -compiler can change which
// module graph and package set resolves. A package that exists for `go test`
// under one of those flags and not for a plain `go list` is never tested, and
// nothing says so, so each one is forwarded with its value.
//
// A flag whose value is the next argument is consumed with it before anything
// is classified, so that value is never read as a flag: `-run -race` is a regex
// whose text is `-race`, and reading it as a build flag would hand the
// enumeration a sanitiser the caller never asked for. Normalisation and the
// value-taking tables are shardplan.go's, so both readers consume values from
// the same spellings, including `-test.run` for `-run`. Both `-args` and the
// build-level `--` end the flags: everything after either belongs to the test
// binary, not to `go test`.
//
// The gate passes the caller's argv through as a quoted array, so a value may
// contain whitespace, a shell glob metacharacter, or be empty and still reach
// `go test` and the enumeration intact. The one thing that cannot survive is a
// newline: the helper hands its flags back one per line, so a value containing
// a newline cannot be represented, and it is refused rather than truncated.
//
// -C is refused rather than forwarded: it changes directory before the command
// runs, and the gate has already anchored the enumeration to each module's own
// directory, so applying it to only one of `go list` and `go test` would
// recreate the tree mismatch this exists to prevent.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// packageSelectionValueFlags take their value as the next argument and change
// which packages or files exist, so they are forwarded to the enumeration.
var packageSelectionValueFlags = map[string]bool{
	"-tags": true, "-overlay": true, "-mod": true, "-modfile": true, "-compiler": true,
}

// packageSelectionBareFlags stand alone and change which packages exist, each
// by setting a build tag of its own name.
var packageSelectionBareFlags = map[string]bool{"-race": true, "-msan": true, "-asan": true}

// consumesValue reports whether name's value is the next argument, and so must
// be skipped over rather than read as a flag. -tags is a selection flag this
// walker forwards, not a skip, and -args/-- terminate the flags; all three are
// handled by the walker itself.
func consumesValue(name string) bool {
	if name == "-tags" || name == "-args" || name == "--" {
		return false
	}
	return buildValueFlags[name] || testForwardValueFlags[name] || testRefusedValueFlags[name]
}

// packageSelectionFlags is the answer, in the spelling `go list` will be given,
// or an error for a flag that cannot be applied to both commands. Every value
// is emitted in the `name=value` form, so a value is never a line of its own
// that a line-oriented reader could drop.
func packageSelectionFlags(args []string) ([]string, error) {
	var out []string
	for i := 0; i < len(args); i++ {
		whole := goFlag(args[i])
		name, value, inline := strings.Cut(whole, "=")
		if name == "-C" {
			return nil, errors.New("-C is not supported here: the gate enumerates each module from its own directory, so a -C would make go list describe a different tree than go test builds")
		}
		if name == "-args" || name == "--" {
			return out, nil
		}
		switch {
		case packageSelectionValueFlags[name]:
			if !inline {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("%s was given with nothing after it, and its value decides which packages exist", name)
				}
				i++
				value = args[i]
			}
			if err := checkValue(name, value); err != nil {
				return nil, err
			}
			out = append(out, name+"="+value)
		case packageSelectionBareFlags[name]:
			out = append(out, whole)
		case consumesValue(name):
			// A value-taking flag this walker does not forward is consumed so
			// its value is not read as a flag, but its value is not emitted, so
			// it needs no representability check: the gate hands the original
			// argv to go test as a quoted array, newlines and all.
			if !inline {
				if i+1 >= len(args) {
					return nil, fmt.Errorf("%s was given with nothing after it, and its value decides what runs", name)
				}
				i++
			}
		}
	}
	return out, nil
}

// checkValue refuses the one value the line-per-flag handoff from the helper to
// the gate cannot carry. Everything else -- whitespace, shell glob
// metacharacters, an empty value -- survives, because the gate keeps the
// caller's argv as a quoted array and expands it quoted for every command.
func checkValue(name, value string) error {
	if strings.Contains(value, "\n") {
		return fmt.Errorf("the %s value %q contains a newline, which the line-per-flag handoff to the gate cannot carry", name, value)
	}
	return nil
}

func listBuildFlagsMain(args []string) int {
	return listBuildFlags(args, os.Stdout, os.Stderr)
}

// listBuildFlags prints one flag per line, each in `name=value` form for a flag
// that takes a value. That is how a shell can read them into an array without
// splitting a value on its spaces and without an empty value vanishing.
func listBuildFlags(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("list-build-flags", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, "usage: evener-dev list-build-flags -- <go test flags...>\n\n"+
			"Prints, one per line, the flags that must also be given to the `go list`\n"+
			"that enumerates the packages `go test` will be handed.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	flags, err := packageSelectionFlags(fs.Args())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "evener-dev list-build-flags: %v\n", err)
		return 2
	}
	for _, f := range flags {
		// A short write would hand the gate a truncated flag list, and it would
		// enumerate under flags the caller never set. Fail loudly instead, so
		// the gate's own guard stops the run.
		line := f + "\n"
		n, err := io.WriteString(stdout, line)
		if err == nil && n != len(line) {
			err = io.ErrShortWrite
		}
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "evener-dev list-build-flags: writing flags: %v\n", err)
			return 1
		}
	}
	return 0
}
