// serf-namingcheck enforces the project's wire-format naming convention:
//
//	JSON tags  -> snake_case
//	TOML tags  -> snake_case
//	(CLI flags are kebab-case; that's enforced where flags are registered.)
//
// Path carve-outs (all of these speak the codex/appwire wire protocol and
// therefore require camelCase JSON tags):
//
//	internal/appwire/        — the protocol definition itself.
//	internal/appsource/      — clients of the codex/appwire protocol.
//	internal/appserver/      — server-side implementation of the protocol.
//	server/appwire_*.go      — the hub's appwire runtime glue.
//
// Plus one fully-exempt tree, where each provider's upstream wire format
// owns the casing:
//
//	llm/providers/*/         — JSON tags are exempt entirely.
//
// It scans every Go file for struct tags and every TOML file for keys, and
// prints one violation per line in `path:line: message` format. Exits non-zero
// when violations exist.
//
// Run it via `make lint-naming` (or directly: `go run ./cmd/serf-namingcheck`).
// CI also runs it as a separate step.
//
// A single field/key can opt out with a `// serf:naming-ignore` (Go) or
// `# serf:naming-ignore` (TOML) marker on the immediately preceding line. Use
// sparingly — every opt-out should also carry a comment explaining why.
package main

import (
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strings"
)

// Violation describes one naming-convention failure with enough context to
// fix it from the message alone.
type Violation struct {
	File    string
	Line    int
	Message string
}

func (v Violation) String() string {
	return fmt.Sprintf("%s:%d: %s", v.File, v.Line, v.Message)
}

// defaultExcludes is the path-prefix list skipped by the walker. Paths are
// matched relative to the repo root with forward slashes.
var defaultExcludes = []string{
	"inspo/",
	"vendor/",
	"node_modules/",
	".git/",
	"cmd/serf-namingcheck/", // never lint ourselves; we contain test strings
}

// excludeSuffixes matches anywhere in the relative path.
var excludeSuffixes = []string{
	"/testdata/",
	"/.git/",
}

// appwirePrefixes marks files that participate in the codex/appwire wire
// protocol. Codex's wire format is camelCase, so JSON tags in any of these
// trees MUST be camelCase and the snake_case-default rule does not apply.
//
//   - internal/appwire/    is the protocol definition itself.
//   - internal/appsource/  contains clients of the protocol (CodexSource
//     serializes/deserializes the codex wire format).
//   - internal/appserver/  contains the server-side implementation that
//     speaks the same wire format back to clients.
//   - internal/appprojector/ projects codex/appwire payloads (its tests
//     parse the same camelCase wire shapes).
//   - internal/launchconfig/ defines the launch-option schema that appwire
//     mirrors on the wire (appwire.LaunchOption carries the identical
//     camelCase tags); its JSON tags must match the wire spelling verbatim.
var appwirePrefixes = []string{
	"internal/appwire/",
	"internal/appsource/",
	"internal/appserver/",
	"internal/appprojector/",
	"internal/launchconfig/",
}

// appwireServerPrefix matches the hub's appwire runtime glue files
// (server/appwire_runtime.go, server/appwire_projection.go, ...). These
// files thread appwire payloads through the hub and carry the camelCase
// requirement with them.
const appwireServerPrefix = "server/appwire_"

// providersPrefix marks per-provider client code. Each upstream provider has
// its own wire format (OpenAI uses snake_case, Anthropic uses snake_case,
// Google uses camelCase, etc.) so JSON tags here are completely exempt — they
// must match what the upstream API expects, not our project default.
const providersPrefix = "llm/providers/"

// isAppwirePath reports whether rel points at code that speaks the
// codex/appwire wire protocol. JSON tags in any of these trees are exempt
// from the snake_case rule (they're forced camelCase by the codex protocol).
func isAppwirePath(rel string) bool {
	for _, p := range appwirePrefixes {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	// server/appwire_*.go — the hub's appwire runtime glue. Match by file
	// prefix so unrelated files under server/ remain on snake_case.
	if strings.HasPrefix(rel, "server/") {
		base := rel[len("server/"):]
		if strings.HasPrefix(base, "appwire_") {
			return true
		}
	}
	return false
}

// isProvidersPath reports whether rel points inside llm/providers/. JSON tags
// in this tree are exempt from naming checks entirely.
func isProvidersPath(rel string) bool {
	return strings.HasPrefix(rel, providersPrefix)
}

func main() {
	var (
		root    string
		verbose bool
	)
	flag.StringVar(&root, "root", ".", "repo root to scan")
	flag.BoolVar(&verbose, "v", false, "print scanned files")
	flag.Parse()

	abs, err := filepath.Abs(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "namingcheck:", err)
		os.Exit(2)
	}
	violations, err := Run(abs, verbose)
	if err != nil {
		fmt.Fprintln(os.Stderr, "namingcheck:", err)
		os.Exit(2)
	}
	for _, v := range violations {
		fmt.Println(v)
	}
	if len(violations) > 0 {
		fmt.Fprintf(os.Stderr, "\n%d naming violation(s)\n", len(violations))
		os.Exit(1)
	}
}

// Run walks root and returns every violation it finds. Exposed for tests and
// for the analyzer wrapper.
func Run(root string, verbose bool) ([]Violation, error) {
	var out []Violation
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		rel = filepath.ToSlash(rel)

		if d.IsDir() {
			// Skip excluded directories outright.
			if rel != "." && (isExcluded(rel+"/") || strings.HasPrefix(filepath.Base(path), ".")) {
				return fs.SkipDir
			}
			return nil
		}
		if isExcluded(rel) {
			return nil
		}
		switch {
		case strings.HasSuffix(path, ".go"):
			if verbose {
				fmt.Fprintln(os.Stderr, "scan go:", rel)
			}
			vs, err := checkGoFile(path, rel)
			if err != nil {
				return err
			}
			out = append(out, vs...)
		case strings.HasSuffix(path, ".toml"):
			if verbose {
				fmt.Fprintln(os.Stderr, "scan toml:", rel)
			}
			vs, err := checkTOMLFile(path, rel)
			if err != nil {
				return err
			}
			out = append(out, vs...)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].File != out[j].File {
			return out[i].File < out[j].File
		}
		return out[i].Line < out[j].Line
	})
	return out, nil
}

func isExcluded(rel string) bool {
	for _, p := range defaultExcludes {
		if strings.HasPrefix(rel, p) {
			return true
		}
	}
	for _, s := range excludeSuffixes {
		if strings.Contains(rel, s) {
			return true
		}
	}
	// Hidden dirs (anything whose path segment starts with a dot, e.g.
	// .github, .git) are walked at top level but otherwise skipped.
	for _, seg := range strings.Split(rel, "/") {
		if len(seg) > 1 && strings.HasPrefix(seg, ".") && seg != ".github" {
			return true
		}
	}
	return false
}

// --- Go struct tags --------------------------------------------------------

const ignoreMarker = "serf:naming-ignore"

func checkGoFile(path, rel string) ([]Violation, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
	if err != nil {
		// Don't fail the whole run on a parse error; surface it as a
		// violation so it's visible.
		return []Violation{{File: rel, Line: 1, Message: "parse error: " + err.Error()}}, nil
	}

	// Build the set of line numbers that carry the ignore marker so we can
	// skip the field on the next line.
	ignoreLines := map[int]bool{}
	for _, cg := range file.Comments {
		for _, c := range cg.List {
			if strings.Contains(c.Text, ignoreMarker) {
				ignoreLines[fset.Position(c.End()).Line] = true
			}
		}
	}

	var out []Violation
	ast.Inspect(file, func(n ast.Node) bool {
		st, ok := n.(*ast.StructType)
		if !ok || st.Fields == nil {
			return true
		}
		for _, field := range st.Fields.List {
			if field.Tag == nil {
				continue
			}
			fieldLine := fset.Position(field.Pos()).Line
			// An ignore comment on the previous line covers this field.
			if ignoreLines[fieldLine-1] {
				continue
			}
			raw := field.Tag.Value
			// raw is the quoted literal (with surrounding backticks or quotes).
			if len(raw) < 2 {
				continue
			}
			tag := reflect.StructTag(raw[1 : len(raw)-1])
			if v, ok := tag.Lookup("json"); ok {
				if msg := checkJSONTag(v, rel); msg != "" {
					out = append(out, Violation{File: rel, Line: fieldLine, Message: msg})
				}
			}
			if v, ok := tag.Lookup("toml"); ok {
				if msg := checkTOMLTag(v); msg != "" {
					out = append(out, Violation{File: rel, Line: fieldLine, Message: msg})
				}
			}
		}
		return true
	})
	return out, nil
}

// tagKey extracts the actual field name from a struct tag value, dropping the
// option suffix ("name,omitempty" -> "name") and recognising sentinels.
func tagKey(v string) (key string, skip bool) {
	if v == "-" || v == "" {
		return "", true
	}
	if i := strings.Index(v, ","); i >= 0 {
		v = v[:i]
	}
	if v == "" || v == "-" {
		return "", true
	}
	return v, false
}

// checkJSONTag enforces snake_case JSON tags, with two path-based carve-outs:
//
//   - Files that speak the codex/appwire wire protocol are exempt because the
//     protocol forces camelCase. That covers internal/appwire/ (the protocol
//     definition itself), internal/appsource/ and internal/appserver/ (its
//     clients and server-side implementation), and server/appwire_*.go (the
//     hub runtime glue that threads appwire payloads through the hub).
//   - Files under llm/providers/ are exempt entirely; each provider's tag
//     spelling has to match its upstream API verbatim.
//
// Pure-lowercase single-word keys (e.g. "model", "id") satisfy both
// snake_case and camelCase, so they pass everywhere.
func checkJSONTag(v, rel string) string {
	key, skip := tagKey(v)
	if skip {
		return ""
	}
	if isProvidersPath(rel) {
		return ""
	}
	if isAppwirePath(rel) {
		// Appwire is the inverted regime: camelCase is mandatory there.
		if !isCamelCase(key) {
			return fmt.Sprintf("json tag %q must be camelCase in appwire-adjacent code (suggest %q)", key, toCamelCase(key))
		}
		return ""
	}
	if isUpstreamCamelKey(key) {
		return ""
	}
	if !isSnakeCase(key) {
		return fmt.Sprintf("json tag %q must be snake_case (suggest %q)", key, toSnakeCase(key))
	}
	return ""
}

// isUpstreamCamelKey reports whether key is a fixed camelCase key from an
// upstream Claude config format (.mcp.json / settings.json) that serf parses
// verbatim. The names are dictated by upstream, so they are exempt from the
// snake_case rule wherever they appear (this is what previously required
// per-field serf:naming-ignore markers on those structs).
func isUpstreamCamelKey(key string) bool {
	switch key {
	case "mcpServers", "enabledPlugins":
		return true
	}
	return false
}

func checkTOMLTag(v string) string {
	key, skip := tagKey(v)
	if skip {
		return ""
	}
	if !isSnakeCase(key) {
		return fmt.Sprintf("toml tag %q must be snake_case (suggest %q)", key, toSnakeCase(key))
	}
	return ""
}

// --- naming predicates -----------------------------------------------------

var (
	// camelCase: starts with a lowercase letter, then letters/digits, no
	// separators. Acronyms (URL, ID) are allowed as long as the first
	// character is lowercase, e.g. "iconURL" is fine, "URL" is not.
	camelRe = regexp.MustCompile(`^[a-z][a-zA-Z0-9]*$`)

	// kebab-case: lowercase letters/digits, optionally separated by single
	// hyphens. Pure digits aren't valid keys but we treat them as OK.
	kebabRe = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

	// snake_case: lowercase letters/digits, optionally separated by single
	// underscores. A single-word lowercase key (e.g. "model") matches.
	snakeRe = regexp.MustCompile(`^[a-z0-9]+(_[a-z0-9]+)*$`)
)

func isCamelCase(s string) bool { return camelRe.MatchString(s) }
func isKebabCase(s string) bool { return kebabRe.MatchString(s) }
func isSnakeCase(s string) bool { return snakeRe.MatchString(s) }

// toCamelCase produces a best-effort camelCase suggestion. It splits on
// underscores or hyphens and uppercases each non-leading segment's first letter.
func toCamelCase(s string) string {
	parts := splitWords(s)
	if len(parts) == 0 {
		return s
	}
	var b strings.Builder
	b.WriteString(strings.ToLower(parts[0]))
	for _, p := range parts[1:] {
		if p == "" {
			continue
		}
		b.WriteString(strings.ToUpper(p[:1]))
		b.WriteString(strings.ToLower(p[1:]))
	}
	return b.String()
}

// toKebabCase produces a best-effort kebab-case suggestion. CamelCase is split
// on case boundaries; underscores become hyphens; result is lowercased.
func toKebabCase(s string) string {
	parts := splitWords(s)
	for i := range parts {
		parts[i] = strings.ToLower(parts[i])
	}
	return strings.Join(parts, "-")
}

// toSnakeCase produces a best-effort snake_case suggestion. CamelCase is split
// on case boundaries; hyphens become underscores; result is lowercased.
func toSnakeCase(s string) string {
	parts := splitWords(s)
	for i := range parts {
		parts[i] = strings.ToLower(parts[i])
	}
	return strings.Join(parts, "_")
}

// splitWords decomposes a name into word parts. Recognises underscores,
// hyphens, and camelCase/PascalCase boundaries.
func splitWords(s string) []string {
	// First normalise underscores/hyphens into a single delimiter.
	tmp := strings.ReplaceAll(s, "_", " ")
	tmp = strings.ReplaceAll(tmp, "-", " ")
	// Now split camelCase: insert a space before each uppercase letter that
	// follows a lowercase letter, and at the boundary of an acronym run that
	// transitions back to lowercase (e.g. "HTTPRequest" -> "HTTP Request").
	var b strings.Builder
	runes := []rune(tmp)
	for i, r := range runes {
		if i > 0 {
			prev := runes[i-1]
			next := rune(0)
			if i+1 < len(runes) {
				next = runes[i+1]
			}
			lower := func(r rune) bool { return r >= 'a' && r <= 'z' }
			upper := func(r rune) bool { return r >= 'A' && r <= 'Z' }
			if (lower(prev) && upper(r)) || (upper(prev) && upper(r) && lower(next)) {
				b.WriteByte(' ')
			}
		}
		b.WriteRune(r)
	}
	fields := strings.Fields(b.String())
	return fields
}

// --- TOML files ------------------------------------------------------------

var (
	tomlKeyLineRe   = regexp.MustCompile(`^\s*([A-Za-z_][A-Za-z0-9_\-]*(?:\s*\.\s*[A-Za-z_][A-Za-z0-9_\-]*)*)\s*=`)
	tomlTableLineRe = regexp.MustCompile(`^\s*\[\[?\s*([^\]]+?)\s*\]\]?\s*(?:#.*)?$`)
)

func checkTOMLFile(path, rel string) ([]Violation, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var out []Violation
	lines := strings.Split(string(data), "\n")

	inMultilineString := false
	multilineDelim := ""
	prevIgnore := false

	for i, line := range lines {
		lineNo := i + 1
		trimmed := strings.TrimSpace(line)

		// Handle multi-line strings (triple-quoted). Don't lint inside them.
		if inMultilineString {
			if strings.Contains(line, multilineDelim) {
				inMultilineString = false
				multilineDelim = ""
			}
			continue
		}

		if trimmed == "" {
			prevIgnore = false
			continue
		}
		if strings.HasPrefix(trimmed, "#") {
			if strings.Contains(trimmed, ignoreMarker) {
				prevIgnore = true
			}
			continue
		}

		// Table header: [section] or [[array.of.tables]]
		if m := tomlTableLineRe.FindStringSubmatch(line); m != nil {
			for _, part := range strings.Split(m[1], ".") {
				key := strings.TrimSpace(part)
				// Quoted keys are allowed verbatim — used for dotted keys
				// that intentionally embed dots/spaces.
				if strings.HasPrefix(key, `"`) || strings.HasPrefix(key, `'`) {
					continue
				}
				if !prevIgnore && !isSnakeCase(key) {
					out = append(out, Violation{
						File:    rel,
						Line:    lineNo,
						Message: fmt.Sprintf("toml table key %q must be snake_case (suggest %q)", key, toSnakeCase(key)),
					})
				}
			}
			prevIgnore = false
			continue
		}

		// Plain key = value lines.
		if m := tomlKeyLineRe.FindStringSubmatch(line); m != nil {
			for _, part := range strings.Split(m[1], ".") {
				key := strings.TrimSpace(part)
				if !prevIgnore && !isSnakeCase(key) {
					out = append(out, Violation{
						File:    rel,
						Line:    lineNo,
						Message: fmt.Sprintf("toml key %q must be snake_case (suggest %q)", key, toSnakeCase(key)),
					})
				}
			}
			// Watch for the start of a triple-quoted string so we don't
			// misparse content lines inside it.
			rest := line[strings.Index(line, "=")+1:]
			for _, delim := range []string{`"""`, `'''`} {
				if idx := strings.Index(rest, delim); idx >= 0 {
					if strings.Index(rest[idx+3:], delim) < 0 {
						inMultilineString = true
						multilineDelim = delim
					}
				}
			}
			prevIgnore = false
			continue
		}
		prevIgnore = false
	}
	return out, nil
}
