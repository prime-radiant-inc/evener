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

// A value is consumed with its flag before anything is classified, and which
// flags take a separate value is read off the shard runner's tables through
// walkFlags: there is one answer to that question in this package, not two
// that can drift. The same goes for the spelling a flag arrives in -- goFlag
// normalises --tags to -tags and -test.short to -short -- and for the refusal
// -C gets.
//
// The prefix rule is the shard runner's too, and it costs nothing here:
// measured on go1.27.0, every name where the two tables disagreed about
// -test.<name> is one `go test` refuses at the door -- -test.vet, -test.args,
// -test.exec, -test.o, -test.c and -test.n all answer `flag provided but not
// defined`, and -test.race with them.

// errAfterArgs stops the walk at -args. Everything after it belongs to the
// test binary, not to `go test`: a word spelled -race there is an argument
// whose text is -race, and enumerating under it would build a tree nobody
// asked for -- including the flag after it, whose value it is not.
var errAfterArgs = errors.New("-args")

// packageSelectionFlags is the answer, in the spelling `go list` will be given.
func packageSelectionFlags(args []string) ([]string, error) {
	var out []string
	err := walkFlags(args, func(tok flagToken) error {
		switch {
		case tok.name == "-args":
			return errAfterArgs
		case tok.name == "-C":
			// The same refusal the shard runner gives it, in the same words:
			// the gate decides which directory each module is enumerated and
			// tested in, and a -C would move one of them.
			return errUnsupportedC()
		case packageSelectionValueFlags[tok.name]:
			if tok.inline {
				out = append(out, tok.whole)
				return nil
			}
			// The value goes on as the caller wrote it: it is data, and
			// normalising it would rewrite a path or a build tag list.
			out = append(out, tok.name, tok.value)
		case packageSelectionBareFlags[tok.name]:
			out = append(out, tok.whole)
		}
		return nil
	})
	if err != nil && !errors.Is(err, errAfterArgs) {
		return nil, err
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
