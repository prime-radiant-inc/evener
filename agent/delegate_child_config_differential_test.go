package agent

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"testing"

	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/agent/schema"
	"primeradiant.com/evener/identifier"
	"primeradiant.com/evener/llm"
)

// A delegate's child config is built twice: from the live parent when the
// delegate is created, and from the frozen descriptor plus the live parent when
// a cold restore rebuilds it. Every data field of the restored child's
// SessionConfig must equal the fresh child's, except the ones listed here.
// Funcs, channels and pointers to non-scalars are live wiring: only their
// presence is compared.
var freshVersusRestoredChildConfigExemptions = []string{
	// A restore announces itself to plugins as a resume; a fresh child does not.
	"cfg.SessionStartKind",
	// Only the child-ID input for NewSession; the restored child's ID comes
	// from its saved metadata instead.
	"cfg.spawn.sessionID",
	// A restored child does not carry the tree's turn counters, so the tree's
	// concurrency budget does not cover its own delegates. Removed with the fix.
	"cfg.spawn.treeCounter",
	"cfg.spawn.driveCounter",
}

// Pins that a cold-restored delegate runs with the same configuration it had
// when it was created, for every data field of SessionConfig and spawnConfig.
// A field the fresh path inherits from the parent but the restore path forgets
// shows up here as a difference.
func TestRestoredDelegateChildConfigMatchesFreshChild(t *testing.T) {
	t.Parallel()
	for _, shareTasks := range []bool{false, true} {
		t.Run(fmt.Sprintf("shareTasks=%v", shareTasks), func(t *testing.T) {
			t.Parallel()
			workspace := t.TempDir()
			stateDir := realTempDirForTest(t)
			agentsDoc := filepath.Join(t.TempDir(), "AGENTS.md")
			if err := os.WriteFile(agentsDoc, []byte("personal doc"), 0o644); err != nil {
				t.Fatal(err)
			}
			memoryRoot := t.TempDir()
			project := identifier.Project{ID: "proj-differential", CanonicalPath: workspace}
			adapter := &fakeAdapter{name: "openai", steps: []func(llm.Request) llm.Response{
				func(llm.Request) llm.Response { return agenttest.FinalResponse("child done") },
			}}
			client := llm.NewClient()
			client.Register(adapter)
			profile := withTestSessionNamer(client, NewOpenAIProfile("gpt-5.2"))
			testOnly := testConfig{skipGitSnapshot: true, minimalSystemPrompt: true, sandboxProber: bwrapCapableProber(workspace)}
			root, err := NewSession(client, profile, execenv.NewLocalExecutionEnvironment(workspace), SessionConfig{
				StateDir:                stateDir,
				MaxSubagentDepth:        2,
				ForceRealIO:             true,
				MemoryStateRoot:         memoryRoot,
				MemoryProjectID:         "memory-project-differential",
				Project:                 project,
				AgentsDocPath:           agentsDoc,
				TurnEndsProcess:         true,
				ShareTasksWithChildren:  shareTasks,
				MaxToolRoundsPerInput:   7,
				UserInstructionOverride: "override",
				SystemPromptAppend:      []string{"append"},
				ModelFallbacks:          []string{"openai/gpt-5.2"},
				EnableLoopDetection:     new(true),
				SandboxNet:              new(true),
				testOnly:                testOnly,
			})
			if err != nil {
				t.Fatalf("NewSession: %v", err)
			}
			created := root.createDelegate(context.Background(), delegateArgs{
				Task:                "differential",
				DelegationAllowance: new(0),
				ResultSchema:        map[string]any{"type": "object", "properties": map[string]any{"answer": map[string]any{"type": "string"}}},
			})
			if created.Err != nil {
				t.Fatalf("createDelegate: %v", created.Err)
			}
			adapter.waitFirstRequest(t)
			children := root.subagents.sessions()
			if len(children) != 1 {
				t.Fatalf("tracked children = %d, want 1", len(children))
			}
			children[0].mu.Lock()
			freshCfg := children[0].cfg
			children[0].mu.Unlock()
			meta := root.Meta()
			if err := schema.SaveSessionMeta(stateDir, meta); err != nil {
				t.Fatal(err)
			}
			root.Close()

			restoredRoot, err := RestoreSessionFromMetaWithConfig(client, profile, execenv.NewLocalExecutionEnvironment(workspace), meta, RestoreSessionConfig{
				StateDir:        stateDir,
				ForceRealIO:     true,
				MemoryStateRoot: memoryRoot,
				Project:         project,
				AgentsDocPath:   agentsDoc,
				TurnEndsProcess: true,
				testOnly:        testOnly,
			})
			if err != nil {
				t.Fatalf("restore root: %v", err)
			}
			defer restoredRoot.Close()
			restored := restoreFixtureDelegate(t, restoredRoot, created.DelegateID)
			restored.sess.mu.Lock()
			restoredCfg := restored.sess.cfg
			restored.sess.mu.Unlock()

			var diffs []string
			diffConfigData(reflect.ValueOf(freshCfg), reflect.ValueOf(restoredCfg), "cfg", &diffs)
			diffs = slices.DeleteFunc(diffs, func(path string) bool {
				return slices.Contains(freshVersusRestoredChildConfigExemptions, path)
			})
			sort.Strings(diffs)
			if len(diffs) != 0 {
				t.Fatalf("restored child config differs from the fresh child's at:\n%v", diffs)
			}
		})
	}
}

// diffConfigData appends the path of every data field that differs between a
// and b. Live wiring (funcs, channels, non-scalar pointers) is skipped.
func diffConfigData(a, b reflect.Value, path string, out *[]string) {
	switch a.Kind() {
	case reflect.Func, reflect.Chan, reflect.UnsafePointer:
		return
	case reflect.Pointer:
		// A pointer to a scalar is optional configuration; any other pointer is
		// live wiring and only its presence is compared.
		if a.IsNil() != b.IsNil() {
			*out = append(*out, path)
		} else if !a.IsNil() && a.Elem().Kind() <= reflect.Float64 {
			diffConfigData(a.Elem(), b.Elem(), path, out)
		}
	case reflect.Interface:
		// An interface holds either wiring (which recursion skips) or data such
		// as a decoded JSON schema, whose contents are compared.
		if a.IsNil() != b.IsNil() || (!a.IsNil() && a.Elem().Type() != b.Elem().Type()) {
			*out = append(*out, path)
		} else if !a.IsNil() {
			diffConfigData(a.Elem(), b.Elem(), path, out)
		}
	case reflect.Struct:
		for i := 0; i < a.NumField(); i++ {
			diffConfigData(a.Field(i), b.Field(i), path+"."+a.Type().Field(i).Name, out)
		}
	case reflect.Slice, reflect.Array:
		if a.Len() != b.Len() {
			*out = append(*out, path)
			return
		}
		for i := 0; i < a.Len(); i++ {
			diffConfigData(a.Index(i), b.Index(i), fmt.Sprintf("%s[%d]", path, i), out)
		}
	case reflect.Map:
		if a.Len() != b.Len() {
			*out = append(*out, path)
			return
		}
		for _, k := range a.MapKeys() {
			bv := b.MapIndex(k)
			if !bv.IsValid() {
				*out = append(*out, fmt.Sprintf("%s[%v]", path, k))
				continue
			}
			diffConfigData(a.MapIndex(k), bv, fmt.Sprintf("%s[%v]", path, k), out)
		}
	default:
		if fmt.Sprint(a) != fmt.Sprint(b) {
			*out = append(*out, path)
		}
	}
}
