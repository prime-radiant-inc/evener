package agent

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/execenv"
	"primeradiant.com/evener/agent/internal/agenttest"
	"primeradiant.com/evener/llm"
)

func TestSkillDescriptionSessionJourney(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	root := t.TempDir()
	initGitRepo(t, root)
	pluginDir := makePluginDir(t, "recovery-plugin")
	body := skillSelectionFixtureBody("COMPLETE_RECOVERY_752")
	sources := map[string]string{
		"recovered": "---\nname: recovered\ndescription: LEGACY: fixture\nuser-invocable: false\n---\n" + body,
		"broken":    "---\nname: broken\ndescription: [\n---\nBROKEN_752\n",
		"invalid":   "---\nname: invalid\ndescription: fixture\nuser-invocable: 'false'\n---\nINVALID_752\n",
		"healthy":   "---\nname: healthy\ndescription: fixture\n---\nHEALTHY_752\n",
	}
	for name, source := range sources {
		writeSkillMD(t, pluginDir, name, source)
	}
	writeSkillMD(t, root, "shadowed", "---\nname: recovery-plugin:healthy\ndescription: shadowed fixture\n---\nSHADOWED_752\n")
	adapter := &fakeAdapter{name: "anthropic", steps: []func(llm.Request) llm.Response{
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(useSkillCall("skill-752", "recovery-plugin:recovered"))
		},
		func(llm.Request) llm.Response {
			return agenttest.ToolCallResponse(communicateCall("done-752", "DONE_752"))
		},
	}}
	client := llm.NewClient()
	client.Register(adapter)
	sess, err := NewSession(client, newAnthropicProfile("claude-test"), execenv.NewLocalExecutionEnvironment(root), SessionConfig{PluginDirs: []string{pluginDir}, testOnly: testConfig{skipGitSnapshot: true}})
	if err != nil {
		t.Fatal(err)
	}
	seen, stop := captureEvents(sess)
	t.Cleanup(stop)
	for _, name := range []string{"recovered", "healthy"} {
		d, err := sess.skills.ResolveExact("recovery-plugin:" + name)
		if err != nil || d.Unavailable || d.Meta.SkillFile != filepath.Join(pluginDir, "skills", name, "SKILL.md") {
			t.Fatalf("%s unavailable or wrong source: %+v err=%v", name, d, err)
		}
	}
	for _, name := range []string{"broken", "invalid"} {
		if d, err := sess.skills.ResolveExact("recovery-plugin:" + name); err == nil && !d.Unavailable {
			t.Fatalf("%s available: %+v", name, d)
		}
	}
	for _, d := range sess.skills.UserEntries() {
		if d.CatalogName == "recovery-plugin:recovered" {
			t.Fatal("user-invocable false advertised")
		}
	}
	categories := map[string]int{}
	for _, d := range sess.DetailedStatus().SkillDiagnostics {
		categories[d.Category]++
	}
	if want := map[string]int{"unquoted_description": 1, "invalid_frontmatter": 1, "invalid_control": 1, "collision": 1}; !reflect.DeepEqual(categories, want) {
		t.Fatalf("diagnostic categories=%v want=%v", categories, want)
	}
	input := "REQUEST_752: continue ordinary work"
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second) // TRIPWIRE: in-process scripted provider, only detects a hung session.
	defer cancel()
	if _, err := sess.ProcessInput(ctx, input, nil); err != nil {
		t.Fatal(err)
	}
	stop()
	requests := adapter.Requests()
	if len(requests) != 2 {
		t.Fatalf("provider requests=%d want=2", len(requests))
	}
	var userTexts []string
	for _, msg := range requests[0].Messages {
		if msg.Role == llm.RoleUser {
			userTexts = append(userTexts, msg.Text())
		}
	}
	if len(userTexts) == 0 || userTexts[len(userTexts)-1] != input {
		t.Fatalf("original input changed: %q", userTexts)
	}
	source := filepath.Join(pluginDir, "skills", "recovered", "SKILL.md")
	requireSingleEnvelope(t, requests[1], "recovery-plugin:recovered", body, source)
	if got := skillActivatedEventNames(*seen); !reflect.DeepEqual(got, []string{"recovery-plugin:recovered"}) {
		t.Fatalf("activation events=%v", got)
	}
	if hasEventKind(*seen, events.EventError) {
		t.Fatal("skill diagnostics emitted a session error")
	}
	counts := map[string]int{}
	var warnings []events.WarningData
	completed, preserved := false, false
	for _, ev := range *seen {
		switch d := ev.Data.(type) {
		case events.WarningData:
			warnings = append(warnings, d)
			counts[d.Code]++
			if d.Title == "" || d.Hint == "" {
				t.Errorf("warning lacks owner guidance: %+v", d)
			}
		case events.CommunicateData:
			completed = d.EndTurn && d.Message == "DONE_752"
		case events.UserInputData:
			preserved = d.Text == input
		}
	}
	if !completed || !preserved {
		t.Fatalf("completed=%v original input event=%v", completed, preserved)
	}
	if want := map[string]int{events.WarningCodePluginCompatibility: 1, "skill_diagnostic": 3, events.WarningCodeContextWindowAssumed: 1}; !reflect.DeepEqual(counts, want) {
		t.Errorf("warning codes=%v want=%v payloads=%+v", counts, want, warnings)
	}
	for name, want := range sources {
		got, err := os.ReadFile(filepath.Join(pluginDir, "skills", name, "SKILL.md"))
		if err != nil || string(got) != want {
			t.Errorf("%s source changed: err=%v", name, err)
		}
	}
}
