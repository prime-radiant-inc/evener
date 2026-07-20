// Command appwirets generates the AppWire TypeScript protocol types
// (cmd/serf-hub/frontend/src/protocol/types.gen.ts) from the declarative
// catalog in package appwire (appwire.Methods and appwire.Notifications),
// the same catalog internal/appwiredoc reflects over for the Markdown
// protocol reference. It is run via `go generate` on the appwire package;
// the committed file is verified up-to-date by TestGeneratedFileCurrent, so
// the catalog in code is the single source of truth and the TS types cannot
// drift from it.
//
// It never invents field shapes: every interface's fields are reflected
// from the catalog's Go types. See EmitCatalog (emit.go) for the mapping
// rules and the one documented exception (a notification with a nil
// Payload).
package main

import (
	"flag"
	"fmt"
	"os"
)

var exitProcess = os.Exit

func main() {
	exitProcess(run(os.Args[1:], os.Stderr, os.WriteFile))
}

func run(args []string, stderr *os.File, writeFile func(string, []byte, os.FileMode) error) int {
	flags := flag.NewFlagSet("appwirets", flag.ContinueOnError)
	flags.SetOutput(stderr)
	out := flags.String("out", "", "output TypeScript path")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *out == "" {
		_, _ = fmt.Fprintln(stderr, "appwirets: -out is required")
		return 2
	}
	if err := writeFile(*out, []byte(EmitCatalog()), 0o644); err != nil {
		_, _ = fmt.Fprintln(stderr, "appwirets: write:", err)
		return 1
	}
	return 0
}
