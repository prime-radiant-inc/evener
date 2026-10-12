package mcphttp_test

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// sdkImportPath is the MCP SDK both callers import, whatever local name they
// give it.
const sdkImportPath = "github.com/modelcontextprotocol/go-sdk/mcp"

// TestTransportConstructionStaysInThisPackage pins the boundary the MCP
// transport policy lives behind (#2396, architecture audit EXT-04). The http and
// sse SDK transports are built by this package for both callers — the connection
// manager (agent/internal/mcp) and the reachability probe (agent/mcpprobe) — so
// the two cannot drift apart: a production file anywhere in this workspace,
// outside this package, that names either SDK transport type at all — a
// composite literal, new(), an address, a slice element type, a type alias, a
// declaration, a signature — fails this scan.
//
// It reads each file's own imports to find the local name the SDK was imported
// under, so a renamed or aliased import is still caught, and it reads a file's
// bytes before parsing it, so only the handful that import the SDK are parsed.
// It does not follow a value back to where it was built, so a transport a caller
// receives from elsewhere is out of its reach, and it scans production sources
// only (a test file is not a production construction site). Its proof that the
// walk reaches other packages is the mutation recorded in #2396's pull request:
// rebuilding a transport privately in agent/mcpprobe fails this test.
func TestTransportConstructionStaysInThisPackage(t *testing.T) {
	t.Parallel()
	// The workspace root, absolute, so the walk's root is not itself a dot-named
	// directory that the hidden-directory skip below would prune (which would
	// silently scan nothing), and so every module in the workspace is covered.
	workspaceRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	const allowed = "agent/internal/mcphttp"
	watched := map[string]bool{"SSEClientTransport": true, "StreamableClientTransport": true}
	reachedThisPackage := false

	err = filepath.WalkDir(workspaceRoot, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch name := d.Name(); {
			case name == "testdata" || name == "node_modules" || strings.HasPrefix(name, "."):
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(workspaceRoot, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		inThisPackage := strings.HasPrefix(rel, allowed+"/")
		if inThisPackage {
			reachedThisPackage = true
		}

		source, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		if !bytes.Contains(source, []byte(sdkImportPath)) {
			return nil // cannot name one of the SDK's types without importing it
		}
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, path, source, 0)
		if parseErr != nil {
			return fmt.Errorf("parse %s: %w", rel, parseErr)
		}
		local := sdkLocalName(file)
		if local == "" || local == "_" || local == "." {
			return nil
		}
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			qualifier, ok := sel.X.(*ast.Ident)
			if !ok || qualifier.Name != local || !watched[sel.Sel.Name] || inThisPackage {
				return true
			}
			t.Errorf("%s:%d names mcpsdk.%s outside %s/; build the transport through "+
				"mcphttp.BuildTransport so the probe and the connection manager keep one "+
				"policy (#2396)", rel, fset.Position(sel.Pos()).Line, sel.Sel.Name, allowed)
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reachedThisPackage {
		t.Fatalf("the scan did not reach this package's own production sources under %s, so it did not run as intended", workspaceRoot)
	}
}

// sdkLocalName returns the name this file refers to the MCP SDK by — the import
// alias when it has one, the path's last element otherwise. Empty means the file
// does not import the SDK, so it cannot name one of its types.
func sdkLocalName(file *ast.File) string {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != sdkImportPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "mcp"
	}
	return ""
}
