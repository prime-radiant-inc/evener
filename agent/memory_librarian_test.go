package agent

import (
	"context"
	"strings"
	"testing"

	"primeradiant.com/evener/llm"
)

// The runtime-started memory librarian is the one delegate that keeps the
// memory save tools, and its descriptor records the role that grants them.
func TestMemoryLibrarianDelegateKeepsSaveTools(t *testing.T) {
	t.Parallel()
	workspace, project := memoryGitFixture(t)
	root := t.TempDir()
	var childTools []llm.ToolDefinition
	s := newSession(t, withDir(workspace), withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: root, MemoryProjectID: project.ID, Project: project, MemoryLibrarian: "turn", testOnly: testConfig{sandboxProber: bwrapCapableProber(workspace), disableDelegateIdleRelease: true}}), withSteps(func(req llm.Request) llm.Response {
		childTools = req.Tools
		return finalResponse("no changes")
	}))
	res := s.createDelegate(context.Background(), delegateArgs{Task: "librarian pass", DelegationAllowance: new(0), runtimeRole: memoryLibrarianRole})
	if res.Err != nil {
		t.Fatal(res.Err)
	}
	child := memoryWaitChild(t, s, res.ChildSessionID)
	for _, name := range nativeMemoryToolNames {
		if child.sess.reg.Get(name) == nil {
			t.Errorf("librarian lacks %s", name)
		}
	}
	offered := map[string]bool{}
	for _, def := range childTools {
		offered[def.Name] = true
	}
	for _, name := range memorySaveToolNames {
		if !offered[name] {
			t.Errorf("librarian's model request does not offer %s", name)
		}
	}
	data, _ := child.sess.buildPromptData(child.sess.currentEnv())
	if !data.MemoryLibrarian || !data.MemorySaves || data.MemoryByLibrarian {
		t.Errorf("librarian prompt data: MemoryLibrarian=%t MemorySaves=%t MemoryByLibrarian=%t", data.MemoryLibrarian, data.MemorySaves, data.MemoryByLibrarian)
	}
	if !strings.Contains(data.Role, "memory librarian") {
		t.Errorf("librarian role prompt not frozen in: %.120q", data.Role)
	}
	if got := memoryExec(t, child.sess, "memory_write", map[string]any{"scope": "project", "file_path": "cents.md", "content": "---\ndescription: prices are integer cents\n---\nPrices are integer cents.\n"}); got.IsError {
		t.Fatalf("librarian write refused: %s", got.Output)
	}
	s.delegateController.mu.Lock()
	role := s.delegateController.durable[res.DelegateID].Descriptor.RuntimeRole
	s.delegateController.mu.Unlock()
	if role != memoryLibrarianRole {
		t.Fatalf("descriptor runtime role = %q", role)
	}
}

// No model can start the librarian: the role is missing from the catalog a
// delegate call resolves against, and the delegate tool's decoder never sets
// a runtime role.
func TestMemoryLibrarianRoleIsNotModelSelectable(t *testing.T) {
	t.Parallel()
	s := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: t.TempDir()}))
	if _, listed := s.pluginAgents[memoryLibrarianRole]; listed {
		t.Fatal("memory-librarian is in the model-visible agent catalog")
	}
	decoded, err := decodeDelegateArgs(map[string]any{"prompt": "save things", "agent_type": memoryLibrarianRole})
	if err != nil {
		t.Fatal(err)
	}
	if decoded.runtimeRole != "" {
		t.Fatalf("decoder set runtimeRole %q", decoded.runtimeRole)
	}
	res := s.createDelegate(context.Background(), decoded)
	if res.Err == nil || !strings.Contains(res.Err.Error(), "unknown plugin agent type") {
		t.Fatalf("model-requested librarian: err = %v", res.Err)
	}
}

// Under either librarian arm the root reads memory but loses the save tools,
// and its prompt says the librarian saves. Off leaves the root as it was.
func TestLibrarianModeRootReadsButDoesNotSave(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"off", "turn", "sidecar"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := newSession(t, withDir(t.TempDir()), withConfig(SessionConfig{StateDir: t.TempDir(), MemoryStateRoot: t.TempDir(), MemoryLibrarian: mode}))
			librarian := mode != "off"
			for _, name := range memorySaveToolNames {
				if (s.reg.Get(name) != nil) == librarian {
					t.Errorf("%s: has %s = %t", mode, name, !librarian)
				}
			}
			for _, name := range memoryReadToolNames {
				if s.reg.Get(name) == nil {
					t.Errorf("%s: root lost %s", mode, name)
				}
			}
			prompt, warning := s.renderSystemPrompt(s.currentEnv())
			if warning != "" {
				t.Fatal(warning)
			}
			if got := strings.Contains(prompt, "A memory librarian reads this conversation"); got != librarian {
				t.Errorf("%s: librarian line present = %t", mode, got)
			}
			if got := strings.Contains(prompt, "Write each page with memory_write"); got == librarian {
				t.Errorf("%s: save guidance present = %t", mode, got)
			}
		})
	}
}

// An unknown arm name is off; so is any delegate, whatever the switch says.
func TestLibrarianModeParsing(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]memoryLibrarianMode{"": librarianOff, "bogus": librarianOff, "TURN": librarianTurn, " sidecar ": librarianSidecar} {
		s := &Session{cfg: SessionConfig{MemoryStateRoot: "/m", MemoryLibrarian: value}}
		if value == "" {
			s.cfg.MemoryLibrarian = "off"
		}
		if got := s.librarianMode(); got != want {
			t.Errorf("mode %q = %q, want %q", value, got, want)
		}
	}
	child := &Session{cfg: SessionConfig{MemoryStateRoot: "/m", MemoryLibrarian: "turn"}}
	child.cfg.spawn.parentSessionID = "root"
	if child.librarianMode() != librarianOff {
		t.Error("a delegate runs a librarian arm")
	}
}
