package repoaudit

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

// scenarioSourceCitationGroups is the length FindStringSubmatch returns for a
// scenarioSourceCitation match: the whole span, its captured path, and its
// captured symbol (empty when the anchor is a `:line` range rather than a
// `#symbol`).
const scenarioSourceCitationGroups = 3

// TestScenarioSourceCitationNeedleReadsEveryCompiledExtension pins the widening
// itself. scenarioSourceCitationsResolve is a corpus scan, so it is green both when the corpus
// is clean and when the needle quietly stops matching a language; the resolved
// floor catches a needle that matches NOTHING, and this catches one that
// matches only Go again.
func TestScenarioSourceCitationNeedleReadsEveryCompiledExtension(t *testing.T) {
	for extension := range scenarioNeedleSourceExtensions {
		cited := "panes/session/composer/Composer" + extension
		// scenarioSourceCitation captures two groups, the path and the symbol,
		// so a match is [whole, path, symbol]. Any other length means the
		// needle lost a group.
		m := scenarioSourceCitation.FindStringSubmatch("(`" + cited + ":641-643`)")
		if len(m) != scenarioSourceCitationGroups || m[1] != cited || m[2] != "" {
			t.Fatalf("the source-citation needle does not read a %s citation: %v", extension, m)
		}
		symbolCited := "Composer" + extension
		m = scenarioSourceCitation.FindStringSubmatch("`" + symbolCited + "#handleSteerClick`")
		if len(m) != scenarioSourceCitationGroups || m[1] != symbolCited || m[2] != "handleSteerClick" {
			t.Fatalf("the source-citation needle does not read a %s symbol anchor: %v", extension, m)
		}
	}
	// An anchorless path is a mention, not a citation — "the package was
	// extracted out of `cmd/evener-tui/pending.go`" names a file on purpose.
	for _, mention := range []string{"`Composer.tsx`", "`docs/job-control.md:940`", "`fixture.tsx.snap:12`"} {
		if m := scenarioSourceCitation.FindStringSubmatch(mention); m != nil {
			t.Fatalf("the source-citation needle read %s as a citation: %v", mention, m)
		}
	}
}

func TestScenarioDeclarationsPreserveReceiverQualification(t *testing.T) {
	path := filepath.Join(t.TempDir(), "methods.go")
	const source = `package fixture

const IssuerBaseURL = "https://auth.example.test"

type Alpha struct {
	Field int
}
type Beta struct {
	Field int
}
type Config struct {
	IssuerBaseURL string
}
type Contract interface {
	Do()
}

func (Alpha) Run() {}
func (*Beta) Run() {}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	names, err := scenarioDeclarationsIn(map[string]map[string]bool{}, path)
	if err != nil {
		t.Fatalf("scenarioDeclarationsIn: %v", err)
	}
	for _, want := range []string{"fixture.IssuerBaseURL", "Alpha.Run", "Beta.Run", "Alpha.Field", "Config.IssuerBaseURL", "Contract.Do"} {
		if !names[want] {
			t.Fatalf("declarations missing qualified method %q: %v", want, names)
		}
	}
	for _, ambiguous := range []string{"IssuerBaseURL", "Run", "Field"} {
		if names[ambiguous] {
			t.Fatalf("ambiguous unqualified declaration %q was accepted: %v", ambiguous, names)
		}
	}
	if !names["Do"] {
		t.Fatalf("unique unqualified interface method was rejected: %v", names)
	}
}

// TestScenarioTSDeclarationsRecognizeExportForms pins scenarioTSDeclarationsIn
// against every declaration shape the RCA behind issue #157 specified, plus
// the two forms it names as known imprecision (destructured exports,
// re-exports), which must NOT be matched.
func TestScenarioTSDeclarationsRecognizeExportForms(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.tsx")
	const source = `import { useState } from "react";

export function Composer() {}
export async function loadModels() {}
export default function App() {}
function helperOnly() {}

export const ModelField = () => {};
const localValue = 1;

export class Widget {}
class Internal {}

export interface Props {}
interface Hidden {}

export type Alias = string;
type Private = number;

export enum Mode { Fast, Slow }
enum Quiet { On, Off }

export const { a, b } = destructure();
export { Reexported } from "./elsewhere";
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	names, err := scenarioTSDeclarationsIn(map[string]map[string]bool{}, path)
	if err != nil {
		t.Fatalf("scenarioTSDeclarationsIn: %v", err)
	}
	for _, want := range []string{
		"Composer", "loadModels", "App", "helperOnly",
		"ModelField", "localValue",
		"Widget", "Internal",
		"Props", "Hidden",
		"Alias", "Private",
		"Mode", "Quiet",
	} {
		if !names[want] {
			t.Fatalf("declaration missing %q: %v", want, names)
		}
	}
	for _, notMatched := range []string{"a", "b", "destructure", "Reexported", "elsewhere"} {
		if names[notMatched] {
			t.Fatalf("declaration wrongly matched %q (known imprecision, issue #157): %v", notMatched, names)
		}
	}
}

// TestScenarioTSDeclarationsIgnoreBlockComments pins the PR #271 review
// finding: a declaration-shaped line sitting inside a `/* ... */` (or JSDoc
// `/** ... */`) block comment must not register as declared, and a real
// declaration sharing a line with an inline block comment must still be
// found.
func TestScenarioTSDeclarationsIgnoreBlockComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "fixture.tsx")
	const source = `export function RealSymbol() {}

/*
 * TODO: someday resurrect this:
function BlockCommentedFake() {}
 */

/**
const JsDocFake = 1;
 */

export /* eslint-disable-next-line */ function InlineCommented() {}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	names, err := scenarioTSDeclarationsIn(map[string]map[string]bool{}, path)
	if err != nil {
		t.Fatalf("scenarioTSDeclarationsIn: %v", err)
	}
	for _, want := range []string{"RealSymbol", "InlineCommented"} {
		if !names[want] {
			t.Fatalf("declaration missing %q: %v", want, names)
		}
	}
	for _, notMatched := range []string{"BlockCommentedFake", "JsDocFake"} {
		if names[notMatched] {
			t.Fatalf("declaration wrongly matched %q — it exists only inside a block comment (PR #271 review): %v", notMatched, names)
		}
	}
}

func TestScenarioTrackedFilesByBaseIgnoresUntrackedFiles(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"tracked.go", "deleted.go", "untracked.go", "excluded.tsx"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte("package fixture\n"), 0o600); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	for _, args := range [][]string{{"init", "--quiet"}, {"add", "tracked.go", "deleted.go", "excluded.tsx"}} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, output)
		}
	}
	if err := os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatalf("remove deleted fixture: %v", err)
	}

	byBase, err := scenarioTrackedFilesByBase(root, map[string]bool{".go": true})
	if err != nil {
		t.Fatalf("scenarioTrackedFilesByBase: %v", err)
	}
	if got := byBase["tracked.go"]; len(got) != 1 || got[0] != "tracked.go" {
		t.Fatalf("tracked Go files = %v, want tracked.go only", got)
	}
	if _, ok := byBase["untracked.go"]; ok {
		t.Fatalf("untracked Go file appeared in tracked index: %v", byBase["untracked.go"])
	}
	if _, ok := byBase["deleted.go"]; ok {
		t.Fatalf("deleted Go file appeared in checkout index: %v", byBase["deleted.go"])
	}
	if _, ok := byBase["excluded.tsx"]; ok {
		t.Fatalf("file outside the requested extensions appeared in the index: %v", byBase["excluded.tsx"])
	}
	withTSX, err := scenarioTrackedFilesByBase(root, map[string]bool{".go": true, ".tsx": true})
	if err != nil {
		t.Fatalf("scenarioTrackedFilesByBase with .tsx: %v", err)
	}
	if got := withTSX["excluded.tsx"]; len(got) != 1 || got[0] != "excluded.tsx" {
		t.Fatalf("requested .tsx files = %v, want excluded.tsx", got)
	}
}

// TestScenarioBareLineAnchorResolutionPinsTheRule pins
// scenarioBareLineAnchorResolutions itself: which preceding path a bare anchor
// binds to, and that it binds to none when there is nothing before it in the
// paragraph.
func TestScenarioBareLineAnchorResolutionPinsTheRule(t *testing.T) {
	const card = "`Composer.tsx` introduces the table below.\n" +
		"\n" +
		"| Hook | What it is |\n" +
		"|---|---|\n" +
		"| steer | only while busy (`:382`) |\n" +
		"\n" +
		"`Rail.tsx:509` names a row, and a bare `:113-138` follows.\n" +
		"\n" +
		"A trailing paragraph keeps its own `:999`.\n"
	want := []scenarioBareLineAnchorResolution{
		{line: 5, anchor: ":382", cited: "Composer.tsx"},
		{line: 7, anchor: ":113-138", cited: "Rail.tsx"},
		{line: 9, anchor: ":999", cited: ""},
	}
	got := scenarioBareLineAnchorResolutions(card)
	if len(got) != len(want) {
		t.Fatalf("resolved %d bare anchors, want %d: %+v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("bare anchor %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}
