package dev

// gate-surface regenerates scripts/lib/gate-surface-lib.sh from
// internal/devtool/gatesurface. The shell file is a generated artifact so the
// one authored definition of the gate's test-selection surface is the Go
// package, while the shell gate and the ratchets keep sourcing a file without
// building evener-dev first.

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"primeradiant.com/evener/internal/devtool/gatesurface"
)

func gateSurfaceMain(args []string) int {
	fs := flag.NewFlagSet("gate-surface", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	root := fs.String("root", "", "repository root to write scripts/lib/gate-surface-lib.sh under")
	fs.Usage = func() {
		_, _ = fmt.Fprint(os.Stderr, "usage: evener-dev gate-surface --root DIR\n\n"+
			"Rewrite DIR/scripts/lib/gate-surface-lib.sh from internal/devtool/gatesurface.\n"+
			"The shell file is a generated artifact; edit the Go package instead.\n")
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *root == "" || fs.NArg() != 0 {
		fs.Usage()
		return 2
	}
	path := filepath.Join(*root, "scripts", "lib", "gate-surface-lib.sh")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "evener-dev gate-surface: %v\n", err)
		return 1
	}
	if err := os.WriteFile(path, []byte(gatesurface.ShellLib()), 0o644); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "evener-dev gate-surface: %v\n", err)
		return 1
	}
	return 0
}
