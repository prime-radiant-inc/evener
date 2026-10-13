// The copy-oracle subcommand inventories tests that use human-readable copy
// as their oracle (docs/developing-evener/testing.md, "Copy Is Not a Test
// Oracle"), so they can be converted area by area and re-running it shows
// what is left.
//
// The Go half is internal/devtool/copyonly; the TypeScript half is
// cmd/evener-hub/frontend/src/dev/copyonly/copyonly.ts, run under node with
// the frontend's own typescript (`make web-preflight` installs it). Both are
// the engines the copy-only audits use, so the inventory and the ratchets
// cannot disagree.
package dev

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"primeradiant.com/evener/internal/devtool/copyonly"
)

const copyOracleUsage = `usage: evener dev copy-oracle [--path PREFIX] [--only ts|go] [--top N] [--hits FILE]
       evener dev copy-oracle [--path PREFIX] [--only ts|go] --copy-only FILE

Lists the assertions whose oracle is production copy, per package and
category, with the literals each package's tests supplied themselves
(fixtures, excluded). Categories:
  ts-assert     frontend/native text assertion against app copy
  ts-locator    by-text query used only to find a control to click
  ts-constant   by-text query or matcher against an imported copy constant
  go-errstring  Go text check on an error against production copy
  go-copy       the same check on other output (CLI output, logs, text)
  go-asset      Go text check against a phrase from a production text asset

--copy-only lists instead the tests whose every check is a copy claim:
targets (named for wording) and incidental behavior tests, per area, with
every test written to FILE as TSV.

A hit is a candidate to read, not a verdict.
`

// tsInventoryFunc runs the TypeScript half of the inventory.
type tsInventoryFunc func(root, prefix string) (copyonly.Report, error)

func copyOracleMain(args []string) int {
	return copyOracleRun(args, os.Stdout, os.Stderr, runTSInventory)
}

func copyOracleRun(args []string, stdout, stderr io.Writer, tsInventory tsInventoryFunc) int {
	fs := flag.NewFlagSet("evener dev copy-oracle", flag.ContinueOnError)
	fs.SetOutput(stderr)
	prefix := fs.String("path", "", "only scan test files under this repo-relative prefix")
	hits := fs.String("hits", "", "also write every hit as TSV (file, line, category, literal) to this path")
	only := fs.String("only", "", "scan only one language: ts or go")
	top := fs.Int("top", 0, "limit the package table to the N largest (0 = all)")
	copyOnly := fs.String("copy-only", "", "list the copy-only tests instead, writing them as TSV to this path")
	root := fs.String("root", "", "repository root (default: the git checkout around the working directory)")
	fs.Usage = func() {
		_, _ = fmt.Fprint(stderr, copyOracleUsage)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() > 0 || (*only != "" && *only != "ts" && *only != "go") {
		fs.Usage()
		return 2
	}
	if *root == "" {
		out, err := exec.CommandContext(context.Background(), "git", "rev-parse", "--show-toplevel").Output()
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "copy-oracle: not in a git checkout: %v\n", err)
			return 1
		}
		*root = strings.TrimSpace(string(out))
	}

	var reports []copyonly.Report
	if *only != "go" {
		r, err := tsInventory(*root, *prefix)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "copy-oracle: %v\n", err)
			return 1
		}
		reports = append(reports, r)
	}
	if *only != "ts" {
		r, err := copyonly.Inventory(*root, *prefix)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "copy-oracle: %v\n", err)
			return 1
		}
		reports = append(reports, r)
	}
	merged := copyonly.Merge(reports...)

	if *copyOnly != "" {
		var tsv bytes.Buffer
		copyonly.WriteCopyOnly(stdout, &tsv, merged.CopyOnly)
		if err := os.WriteFile(*copyOnly, tsv.Bytes(), 0o644); err != nil {
			_, _ = fmt.Fprintf(stderr, "copy-oracle: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "\n%d tests written to %s\n", len(merged.CopyOnly), *copyOnly)
		return 0
	}
	copyonly.WritePackageTable(stdout, merged, *top)
	if *hits != "" {
		var tsv bytes.Buffer
		copyonly.WriteHitsTSV(&tsv, merged.Hits)
		if err := os.WriteFile(*hits, tsv.Bytes(), 0o644); err != nil {
			_, _ = fmt.Fprintf(stderr, "copy-oracle: %v\n", err)
			return 1
		}
		_, _ = fmt.Fprintf(stderr, "\n%d hits written to %s\n", len(merged.Hits), *hits)
	}
	return 0
}

// tsInventoryScript is the TypeScript half's entry point, relative to the
// repository root.
const tsInventoryScript = "cmd/evener-hub/frontend/src/dev/copyonly/copyonly.ts"

// runTSInventory runs the TypeScript half under node and decodes the report
// it prints.
func runTSInventory(root, prefix string) (copyonly.Report, error) {
	cmd := exec.CommandContext(context.Background(), "node", filepath.Join(root, tsInventoryScript), "--root", root, "--path", prefix)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return copyonly.Report{}, fmt.Errorf("TypeScript inventory (node %s): %w\n%s\n"+
			"It needs node and the frontend's node_modules: run `make web-preflight`, or pass --only go",
			tsInventoryScript, err, strings.TrimSpace(stderr.String()))
	}
	var r copyonly.Report
	if err := json.Unmarshal(out, &r); err != nil {
		return copyonly.Report{}, fmt.Errorf("TypeScript inventory printed no report: %w", err)
	}
	return r, nil
}
