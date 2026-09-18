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
// Two cases are refused rather than forwarded. -C changes directory before the
// command runs, and the gate has already anchored the enumeration to each
// module's own directory, so applying it to only one of `go list` and `go test`
// would recreate the tree mismatch this exists to prevent. A value cannot be
// carried whole either when it is empty as a separate argument, contains
// whitespace, or contains a shell glob metacharacter: the gate word-splits and
// pathname-expands its `go test` invocation, so the two commands would see
// different values (a separate empty argument would vanish entirely, and a glob
// would expand to whatever filenames match). That is checked for every
// value-taking flag, not only the ones forwarded to `go list`: a `-run "Smoke
// -race"` value hands `go test` a real `-race` build flag that the enumeration,
// reading the preserved argv, never applied.

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
			if inline {
				if value != "" {
					if err := checkValue(name, value, false); err != nil {
						return nil, err
					}
				}
				out = append(out, name+"="+value)
				continue
			}
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s was given with nothing after it, and its value decides which packages exist", name)
			}
			i++
			if err := checkValue(name, args[i], true); err != nil {
				return nil, err
			}
			out = append(out, name+"="+args[i])
		case packageSelectionBareFlags[name]:
			out = append(out, whole)
		case consumesValue(name):
			// A value-taking flag this walker does not forward still has its
			// value checked: the gate word-splits every one of its `go test`
			// flags, so a value that splits or globs there changes what `go
			// test` builds even for a flag `go list` never sees. `-run "Smoke
			// -race"` is the case -- `-race` becomes a real build flag for `go
			// test` while the enumeration, which reads the preserved argv,
			// keeps it inside the regex.
			if inline {
				if value != "" {
					if err := checkValue(name, value, false); err != nil {
						return nil, err
					}
				}
				continue
			}
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s was given with nothing after it, and its value decides what runs", name)
			}
			i++
			if err := checkValue(name, args[i], true); err != nil {
				return nil, err
			}
		}
	}
	return out, nil
}

// checkValue refuses a value the gate's word-split, pathname-expanding `go test`
// invocation cannot carry whole: the enumeration is handed the preserved argv
// and would see it intact, so the two commands would build different trees. An
// empty value written as a separate argument is covered too, since that argv
// word is lost entirely to the split, and a glob metacharacter is covered
// because the unquoted expansion matches filenames. An inline empty value is
// allowed: it is one word and survives.
func checkValue(name, value string, separate bool) error {
	if value == "" {
		if !separate {
			return nil
		}
		return fmt.Errorf("the %s value is empty as a separate argument, which the gate's word-split go test invocation drops; write it inline (%s=) or omit the flag", name, name)
	}
	if strings.ContainsAny(value, " \t\n") {
		return fmt.Errorf("the %s value %q contains whitespace, which the gate's word-split go test invocation cannot forward intact; pass it without whitespace", name, value)
	}
	if strings.ContainsAny(value, `*?[`) {
		return fmt.Errorf("the %s value %q contains a shell glob metacharacter, which the gate's unquoted go test invocation would pathname-expand; pass a literal value without * ? or [", name, value)
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
