package agent

import (
	"fmt"
	"os"

	"primeradiant.com/serf/agent/execenv"
	"primeradiant.com/serf/agent/provider"
	"primeradiant.com/serf/llm"
)

// CoreToolNames returns the sorted names of the core tools that carry a compiled
// argument schema — the exact ordered set FuzzToolArgsValidate indexes its table
// by (nameIndex % len). It is the single source of truth a corpus harvester
// (cmd/serf-fuzz-harvest) uses to map a recorded tool-call name to the fuzz
// target's index, so harvested toolargs seeds address the right tool's schema.
//
// It stands up a throwaway session over a temp directory (no network: an empty
// client makes the live-model probe fall back immediately) to run the real
// registerCoreTools wiring, then returns the registry's schema-bearing names.
func CoreToolNames() ([]string, error) {
	dir, err := os.MkdirTemp("", "serf-coretools-")
	if err != nil {
		return nil, fmt.Errorf("temp dir: %w", err)
	}
	defer os.RemoveAll(dir) //nolint:errcheck

	sess, err := NewSession(llm.NewClient(), provider.NewOpenAIProfile("gpt-5"), execenv.NewLocalExecutionEnvironment(dir), SessionConfig{})
	if err != nil {
		return nil, fmt.Errorf("new session: %w", err)
	}
	defer sess.Close()

	names := sess.reg.Names()
	kept := make([]string, 0, len(names))
	for _, name := range names {
		rt := sess.reg.Get(name)
		if rt == nil || rt.Schema == nil {
			continue
		}
		kept = append(kept, name)
	}
	return kept, nil
}
