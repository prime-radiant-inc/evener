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

// allowedTransportPackage is the one package allowed to build an http or sse
// MCP transport. Everything else goes through mcphttp.BuildTransport.
const allowedTransportPackage = "agent/internal/mcphttp"

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
	// The workspace root, absolute: the walk covers every module in the
	// workspace, not just this one.
	workspaceRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	sites, reached, err := transportConstructionSites(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	for _, site := range sites {
		t.Errorf("%s outside %s/; build the transport through mcphttp.BuildTransport so the "+
			"probe and the connection manager keep one policy (#2396)", site, allowedTransportPackage)
	}
	if !reached {
		t.Fatalf("the scan did not reach this package's own production sources under %s, so it did not run as intended", workspaceRoot)
	}
}

// The scan must never prune the directory it starts at. A checkout whose own
// directory name begins with a dot is ordinary — a hidden clone, a CI workspace
// — and skipping it would leave the scan visiting nothing, with the
// proof-of-reaching check in the test above as the only thing that notices.
func TestTransportConstructionScanDoesNotPruneItsRoot(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), ".evener")
	fixture := filepath.Join(root, "agent", "mcpprobe")
	if err := os.MkdirAll(fixture, 0o755); err != nil {
		t.Fatal(err)
	}
	source := "package mcpprobe\n\nimport (\n\tmcpsdk \"" + sdkImportPath + "\"\n)\n\n" +
		"var _ = mcpsdk.SSEClientTransport{}\n"
	if err := os.WriteFile(filepath.Join(fixture, "transport.go"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
	sites, _, err := transportConstructionSites(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sites) != 1 || !strings.Contains(sites[0], "SSEClientTransport") {
		t.Fatalf("a dot-named root's production sources were not scanned: sites = %v", sites)
	}
}

// transportConstructionSites walks root and returns every production source
// outside allowedTransportPackage that names one of the SDK's http/sse transport
// types, each as "relative/path.go:line names mcpsdk.Type", and whether the walk
// reached the allowed package's own production sources at all — false means the
// walk was pruned somewhere above them and its verdict proves nothing.
func transportConstructionSites(root string) (sites []string, reachedAllowed bool, err error) {
	watched := map[string]bool{"SSEClientTransport": true, "StreamableClientTransport": true}
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path == root {
				return nil // the root itself is never pruned; SkipDir here ends the walk
			}
			name := d.Name()
			if name == "testdata" || name == "node_modules" || strings.HasPrefix(name, ".") {
				return fs.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		inThisPackage := strings.HasPrefix(rel, allowedTransportPackage+"/")
		if inThisPackage {
			reachedAllowed = true
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
			sites = append(sites, fmt.Sprintf("%s:%d names mcpsdk.%s", rel, fset.Position(sel.Pos()).Line, sel.Sel.Name))
			return true
		})
		return nil
	})
	return sites, reachedAllowed, err
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
