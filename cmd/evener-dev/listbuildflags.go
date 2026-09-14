package dev

// list-build-flags answers one question for the test gate: which of the
// caller's `go test` flags must also be given to the `go list` that enumerates
// the packages to test.
//
// The answer matters because `go list` and `go test` do not see the same tree.
// -tags selects files, and so can select whole packages; -race, -msan and -asan
// each set a build tag of their own (race, msan, asan), so a package whose only
// files sit behind `//go:build race` exists for `go test -race` and not for a
// plain `go list`. The gate enumerates first and hands `go test` the list, so
// anything the enumeration cannot see is not tested, and nothing says so.
//
// A value is consumed with its flag before anything is classified. `-run -race`
// is a regex whose text is `-race`, and reading it as a build flag would hand
// the enumeration a sanitiser the caller never asked for.

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
)

// The two tables below are every flag `go help build` documents that changes
// which packages or files `go list` selects. Each remaining build flag was
// read and left out on purpose:
//
//   - -pgo, -gcflags, -ldflags, -asmflags, -gccgoflags, -toolexec, -installsuffix,
//     -buildmode, -linkshared, -trimpath, -a, -n, -x, -v, -work, -p, -pkgdir,
//     -modcacherw, -buildvcs, -json: they change how the same packages are
//     compiled, linked, named or reported, not which ones exist;
//   - -cover, -covermode, -coverpkg: instrumentation. -coverpkg selects
//     packages to instrument, which is not the same as selecting packages to
//     list, and the enumeration is about the latter;
//   - -C: it moves the working directory, and the enumeration already runs in
//     the module's own directory. Forwarding it would send `go list` somewhere
//     the caller's `go test` is not.
//
// A flag `go help build` grows later belongs in one of these lists or in that
// paragraph.

// packageSelectionValueFlags take their value as the next argument and change
// which packages exist:
//
//   - -tags: build constraints select files, and a package all of whose files
//     are excluded does not exist;
//   - -overlay: it replaces and adds files, so it can introduce a package that
//     is not on disk at all;
//   - -mod and -modfile: which module graph is resolved, and so which packages
//     resolve;
//   - -compiler: gc and gccgo are build tags of their own, and cgo availability
//     differs between them.
var packageSelectionValueFlags = map[string]bool{
	"-tags": true, "-overlay": true, "-mod": true, "-modfile": true,
	"-compiler": true,
}

// packageSelectionBareFlags stand alone and change which packages exist, each
// by setting a build tag of its own name.
var packageSelectionBareFlags = map[string]bool{"-race": true, "-msan": true, "-asan": true}

// goTestValueFlags are the flags whose value is the next argument. Nothing
// here is forwarded: the list exists so that a value is never read as a flag.
// `-run -race` is a regex whose text is `-race`, and enumerating under a
// sanitiser the caller did not ask for would build a different tree from the
// one the tests run in. Every flag that takes a separate value belongs here,
// whether or not the enumeration would want the flag itself.
var goTestValueFlags = map[string]bool{
	// -C takes a directory, and is not forwarded: the enumeration already runs
	// in the module's own directory.
	"-C": true, "-bench": true, "-benchtime": true, "-blockprofile": true,
	"-blockprofilerate": true, "-count": true, "-coverprofile": true,
	"-covermode": true, "-coverpkg": true, "-cpu": true, "-cpuprofile": true,
	"-exec": true, "-fuzz": true, "-fuzzminimizetime": true, "-fuzztime": true,
	"-gocoverdir": true, "-list": true, "-memprofile": true,
	"-memprofilerate": true, "-mutexprofile": true,
	"-mutexprofilefraction": true, "-o": true, "-outputdir": true,
	"-p": true, "-parallel": true, "-run": true, "-shuffle": true,
	"-skip": true, "-timeout": true, "-trace": true, "-vet": true,
	// The build flags that take a value, so that theirs is skipped too.
	"-asmflags": true, "-buildmode": true, "-compiler": true,
	"-gccgoflags": true, "-gcflags": true, "-installsuffix": true,
	"-ldflags": true, "-mod": true, "-modfile": true, "-overlay": true,
	"-pgo": true, "-pkgdir": true, "-toolexec": true,
}

// normalisedFlag is a caller's flag in the one spelling everything here
// compares: go's flag package reads --tags and -tags alike, so a reader that
// knows one of them drops the other.
func normalisedFlag(raw string) (whole, name, value string, inline bool) {
	whole = raw
	if strings.HasPrefix(whole, "--") && len(whole) > 2 {
		whole = whole[1:]
	}
	// `go test` takes the test binary's own spelling too: -test.run is -run.
	// The prefix comes off only when what remains is a flag that exists, so
	// -test.race does not become the sanitiser -- the same rule the shard
	// runner follows, for the same reason.
	if after, ok := strings.CutPrefix(whole, "-test."); ok && after != "" {
		if stripped := "-" + after; isKnownTestFlag(stripped) {
			whole = stripped
		}
	}
	name, value, inline = strings.Cut(whole, "=")
	return whole, name, value, inline
}

// testBinaryFlags are the names `go help testflag` documents, which are the
// ones the compiled binary answers to as -test.<name>. They are listed here
// rather than derived from the tables above, which mix in build flags: -race
// is a build flag, so -test.race is not the sanitiser and its prefix does not
// come off.
var testBinaryFlags = map[string]bool{
	"-artifacts": true, "-bench": true, "-benchmem": true, "-benchtime": true,
	"-blockprofile": true, "-blockprofilerate": true, "-count": true,
	"-cover": true, "-covermode": true, "-coverpkg": true,
	"-coverprofile": true, "-cpu": true, "-cpuprofile": true,
	"-failfast": true, "-fullpath": true, "-fuzz": true,
	"-fuzzminimizetime": true, "-fuzztime": true, "-gocoverdir": true,
	"-json": true, "-list": true, "-memprofile": true,
	"-memprofilerate": true, "-mutexprofile": true,
	"-mutexprofilefraction": true, "-outputdir": true, "-parallel": true,
	"-run": true, "-short": true, "-shuffle": true, "-skip": true,
	"-timeout": true, "-trace": true, "-v": true, "-vet": true,
}

// isKnownTestFlag reports whether a name is one the test binary has, which is
// what makes dropping its -test. prefix safe.
func isKnownTestFlag(name string) bool {
	if i := strings.IndexByte(name, '='); i > 0 {
		name = name[:i]
	}
	return testBinaryFlags[name]
}

// packageSelectionFlags is the answer, in the spelling `go list` will be given.
func packageSelectionFlags(args []string) ([]string, error) {
	var out []string
	for i := 0; i < len(args); i++ {
		whole, name, _, inline := normalisedFlag(args[i])
		if name == "-args" {
			// Everything after -args belongs to the test binary, not to `go
			// test`: a word spelled -race there is an argument whose text is
			// -race, and enumerating under it would build a tree nobody asked
			// for.
			return out, nil
		}
		if name == "-C" {
			// The same refusal the shard runner gives it: the gate decides
			// which directory each module is enumerated and tested in, and a
			// -C would move one of them.
			return nil, errors.New("-C is not supported here: the gate enumerates and tests each module from its own directory")
		}
		switch {
		case packageSelectionValueFlags[name]:
			if inline {
				out = append(out, whole)
				continue
			}
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s was given with nothing after it, and its value decides which packages exist", name)
			}
			i++
			out = append(out, name, args[i])
		case packageSelectionBareFlags[name]:
			out = append(out, whole)
		case goTestValueFlags[name] && !inline:
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s was given with nothing after it, and its value decides what runs", name)
			}
			// Its value is a value, whatever it looks like.
			i++
		}
	}
	return out, nil
}

func listBuildFlagsMain(args []string) int {
	return listBuildFlags(args, os.Stdout, os.Stderr)
}

// listBuildFlags prints one flag per line, which is how a shell can read them
// into an array without splitting a value on its spaces.
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
	forward, err := packageSelectionFlags(fs.Args())
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "list-build-flags: %v\n", err)
		return 2
	}
	for _, f := range forward {
		// A caller reading a truncated answer enumerates under fewer flags
		// than the tests are built with, which is the failure this subcommand
		// exists to prevent -- so a write that did not land, in full, fails
		// the run.
		if !forwardOutput(stdout, []byte(f+"\n"), "list-build-flags: "+f, stderr) {
			return 1
		}
	}
	return 0
}
