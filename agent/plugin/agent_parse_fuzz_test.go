package plugin

import (
	"testing"

	"primeradiant.com/serf/agent/internal/frontmatter"
	"primeradiant.com/serf/fuzz/edgeseeds"
)

// FuzzParseAgent drives ParseAgent — the markdown-with-YAML-frontmatter parser
// that turns a plugin agent file into an Agent (name/description validation,
// model/color defaulting, the tools scalar-vs-list discrimination with the
// "all" special case, skills and tasks coercion). Only unit tests with fixed
// fixtures touched it; the agent-file decode path is otherwise unfuzzed.
//
// Oracles (never bare no-panic):
//   - on success: Name and Description are non-empty; Model and Color are non-empty
//     (defaulting to "inherit"/"blue"); PluginName is threaded through verbatim;
//     SystemPrompt equals the frontmatter body.
//   - tools discrimination: AllTools and an explicit Tools list are mutually
//     exclusive (AllTools implies no per-tool list).
//   - determinism: parsing the same bytes twice yields the same Name, Description,
//     AllTools flag, and tool/skill/task counts.
//
// SAFETY: pure parse — no I/O, no network, no spawn.
func FuzzParseAgent(f *testing.F) {
	seeds := []string{
		"---\nname: helper\ndescription: helps out\n---\nbody text",
		"---\nname: a\ndescription: d\nmodel: opus\ncolor: red\ntools: all\n---\n",
		"---\nname: a\ndescription: d\ntools:\n  - read_file\n  - shell\nskills:\n  - x\n---\nprompt",
		"---\nname: a\ndescription: d\ntools:\n  - \"*\"\n---\n",
		"---\nname: a\ndescription: d\ntasks:\n  - title: t\n    prompt: p\n    type: research\n---\n",
		"---\nname: a\n---\nmissing description",
		"---\ndescription: d\n---\nmissing name",
		"no frontmatter at all",
		"",
	}
	for _, s := range seeds {
		f.Add([]byte(s))
	}
	for _, s := range edgeseeds.FrontmatterYAML() {
		f.Add([]byte(s))
	}

	f.Fuzz(func(t *testing.T, data []byte) {
		agent, err := ParseAgent(data, "myplugin")
		if err != nil {
			return // rejected inputs prove only the no-panic floor
		}

		if agent.Name == "" {
			t.Fatalf("accepted agent has empty Name")
		}
		if agent.Description == "" {
			t.Fatalf("accepted agent has empty Description")
		}
		if agent.Model == "" {
			t.Fatalf("accepted agent has empty Model (should default to inherit)")
		}
		if agent.Color == "" {
			t.Fatalf("accepted agent has empty Color (should default to blue)")
		}
		if agent.PluginName != "myplugin" {
			t.Fatalf("PluginName = %q, want myplugin", agent.PluginName)
		}
		if agent.AllTools && len(agent.Tools) != 0 {
			t.Fatalf("AllTools set but Tools list non-empty: %v", agent.Tools)
		}

		// SystemPrompt must equal the frontmatter body. A successful ParseAgent
		// implies frontmatter.Parse succeeded, so re-parsing to recover the body is
		// safe and lets us assert the body passed through verbatim.
		if doc, perr := frontmatter.Parse(string(data)); perr == nil && agent.SystemPrompt != doc.Body {
			t.Fatalf("SystemPrompt != frontmatter body:\n prompt=%q\n body=%q", agent.SystemPrompt, doc.Body)
		}

		// Determinism.
		again, err2 := ParseAgent(data, "myplugin")
		if err2 != nil {
			t.Fatalf("second parse errored after first succeeded: %v", err2)
		}
		if again.Name != agent.Name || again.Description != agent.Description ||
			again.AllTools != agent.AllTools || len(again.Tools) != len(agent.Tools) ||
			len(again.Skills) != len(agent.Skills) || len(again.Tasks) != len(agent.Tasks) {
			t.Fatalf("ParseAgent not deterministic for the same input")
		}
	})
}
