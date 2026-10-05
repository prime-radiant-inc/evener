package bundled

import (
	"errors"
	"io/fs"
	"slices"
	"testing"

	"primeradiant.com/evener/frontmatter"
)

func FuzzAssets(f *testing.F) {
	for _, seed := range []uint8{0, 1, 2} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, which uint8) {
		var filesystem fs.FS
		switch which % 3 {
		case 0:
			filesystem = Agents()
		case 1:
			filesystem = Skills()
		default:
			filesystem = Plugins()
		}
		entries, err := fs.ReadDir(filesystem, ".")
		if err != nil || len(entries) == 0 {
			t.Fatalf("embedded assets unavailable: entries=%d err=%v", len(entries), err)
		}
	})
}

func TestMustSubPanics(t *testing.T) {
	old := subFS
	subFS = func(fs.FS, string) (fs.FS, error) { return nil, errors.New("broken") }
	t.Cleanup(func() { subFS = old })
	defer func() {
		if recover() == nil {
			t.Fatal("mustSub did not panic")
		}
	}()
	_ = mustSub("missing")
}

func bundledMeta(t *testing.T, filesystem fs.FS, name string) map[string]any {
	t.Helper()
	raw, err := fs.ReadFile(filesystem, name)
	if err != nil {
		t.Fatalf("read bundled asset %s: %v", name, err)
	}
	doc, err := frontmatter.Parse(string(raw))
	if err != nil {
		t.Fatalf("parse bundled asset %s frontmatter: %v", name, err)
	}
	if len(doc.Meta) == 0 {
		t.Fatalf("bundled asset %s carries no frontmatter metadata", name)
	}
	return doc.Meta
}

// bundledMetaString returns a required string frontmatter field.
func bundledMetaString(t *testing.T, meta map[string]any, key string) string {
	t.Helper()
	s, ok := meta[key].(string)
	if !ok || s == "" {
		t.Fatalf("frontmatter field %q = %#v, want a non-empty string", key, meta[key])
	}
	return s
}

// Not parallel: Skills reads the package-wide subFS seam.
func TestMemoryGardeningBundled(t *testing.T) {
	meta := bundledMeta(t, Skills(), "gardening-memory/SKILL.md")
	if name := bundledMetaString(t, meta, "name"); name != "gardening-memory" {
		t.Fatalf("skill name=%q", name)
	}
	_ = bundledMetaString(t, meta, "description")
}

// bundledMetaStringList returns a required list-of-strings frontmatter field,
// as YAML decodes a flow or block sequence.
func bundledMetaStringList(t *testing.T, meta map[string]any, key string) []string {
	t.Helper()
	items, ok := meta[key].([]any)
	if !ok {
		t.Fatalf("frontmatter field %q = %#v, want a list", key, meta[key])
	}
	out := make([]string, 0, len(items))
	for _, item := range items {
		s, ok := item.(string)
		if !ok {
			t.Fatalf("frontmatter field %q entry %#v, want a string", key, item)
		}
		out = append(out, s)
	}
	return out
}

// TestBundledCoordinatorUsesStableDelegateAndShellIdentities pins the
// coordinator agent's declared capability surface, not the wording of its role
// prompt. The runtime consumes the frontmatter tools: list as an allowlist
// (baseSubagentToolPolicy hands it to Registry.RestrictKeepingResultTool), so
// the stable delegate/shell control tools must be named there. Prose cannot
// prove a capability; a tools: entry can, and the agent package's
// TestBundledAgentToolListsNameRegisteredTools additionally proves every entry
// resolves to a registered tool.
//
// Not parallel: Plugins() reads the package-wide subFS seam that
// TestMustSubPanics swaps out.
func TestBundledCoordinatorUsesStableDelegateAndShellIdentities(t *testing.T) {
	meta := bundledMeta(t, Plugins(), "coordinator-workflow/agents/coordinator.md")
	if name := bundledMetaString(t, meta, "name"); name != "coordinator" {
		t.Fatalf("coordinator agent name = %q, want %q", name, "coordinator")
	}
	tools := bundledMetaStringList(t, meta, "tools")
	for _, want := range []string{
		"delegate_send",   // steer a running delegate by its stable dlg_ identity
		"job_status",      // read a delegate's status, or a shell job's
		"job_list",        // enumerate delegates and shell jobs
		"job_stop",        // stop a delegate tree or a shell job
		"read_transcript", // read a delegate's transcript (job:<id> reads are shell-only)
	} {
		if !slices.Contains(tools, want) {
			t.Errorf("coordinator tools = %v, missing stable delegate/shell control tool %q", tools, want)
		}
	}
}

// TestBundledSubagentPreservesCallerRouteAndCommunicateFinal pins the
// subagent's declared capability surface, not the wording of its role prompt.
// delegate_send is the caller route (delegate_send(to="caller")), so it must be
// in the tools: allowlist. The communicate-final surface is the session result
// tool, injected for every session regardless of tools: — it is not an
// allowlist entry the shipped asset can or should declare, and the agent
// package's result-tool tests cover it.
//
// Not parallel: Agents() reads the package-wide subFS seam that TestMustSubPanics
// swaps out.
func TestBundledSubagentPreservesCallerRouteAndCommunicateFinal(t *testing.T) {
	meta := bundledMeta(t, Agents(), "subagent.md")
	if name := bundledMetaString(t, meta, "name"); name != "subagent" {
		t.Fatalf("subagent agent name = %q, want %q", name, "subagent")
	}
	tools := bundledMetaStringList(t, meta, "tools")
	if !slices.Contains(tools, "delegate_send") {
		t.Errorf("subagent tools = %v, missing caller-route tool %q", tools, "delegate_send")
	}
}
