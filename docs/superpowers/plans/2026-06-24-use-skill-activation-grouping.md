# `use_skill` Activation Grouping Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render a skill activation caused by `use_skill` as part of the `use_skill` tool invocation instead of a separate `Skill activated` transcript row, while verifying the TUI does not duplicate it.

**Architecture:** Correlate `EventSkillActivated` in the appwire projector with the nearest same-turn `use_skill` call for the same skill. Store the grouped activation in `ThreadItem.Raw` under `skill_activation`; web and TUI clients consume the grouped command-execution item and continue rendering unmatched activations as system messages.

**Tech Stack:** Go appwire projector and TUI reducer/renderers; browser JavaScript renderer tests under `cmd/serf-hub/jstest`; deterministic Go/JS tests only, no provider/network tests.

## Global Constraints

- Default tests must be deterministic and must not depend on provider credentials, network access, quota, current model behavior, or ambient developer machine state.
- A matching activation is grouped only when the tool name is `use_skill`, the arguments identify the same skill name, the tool call is in the same turn, and the activation is the next non-tool-output event after that `use_skill` completion.
- Preserve standalone `Skill activated` system-message rendering when the activation cannot be correlated.
- Preserve both current argument keys: `skill_name` and legacy `name`.
- Do not redesign general system-message coalescing.
- Verify both web and TUI rendering paths.

---

## File Structure

- Modify `internal/appprojector/appwire_projection.go`: track recent `use_skill` candidate data and project correlated `EventSkillActivated` as an update to the existing command-execution item with `Raw.skill_activation`.
- Modify `internal/appprojector/appwire_projection_test.go`: add projector tests for grouped, fallback, legacy-key, and ambiguity cases.
- Modify `cmd/serf-hub/assets/renderer-tools.js`: teach the `use_skill` renderer to display grouped activation detail from `tool_state`.
- Modify `cmd/serf-hub/jstest/test-tool-renderers.js`: add grouped and standalone web renderer regression tests.
- Modify `cmd/serf-tui/internal/transcript/types.go`: add a raw metadata field to `ToolCallInfo` so TUI tests can assert grouped activation metadata is carried without creating a system-message duplicate.
- Modify `cmd/serf-tui/internal/transcript/item.go`: carry `ThreadItem.Raw` into `ToolCallInfo` so grouped metadata is available to the renderer.
- Modify `cmd/serf-tui/internal/transcript/reducer_test.go`: verify grouped `use_skill` command-execution item creates one tool message and no system-message duplicate.
- Modify `cmd/serf-tui/internal/msgrender/message.go` or `tool_renderers.go` only if the TUI should display expanded activation text; otherwise keep the concise `✓ skill <name>` row.
- Modify `cmd/serf-tui/internal/msgrender/tool_renderers_test.go` only if renderer output changes.

---

### Task 1: Projector Correlation Tests

**Files:**
- Modify: `internal/appprojector/appwire_projection_test.go`

**Interfaces:**
- Consumes: existing `NewAppEventProjector`, `Project`, `notificationThreadItem`, `notificationTurnID`, `hasAppNotification` helpers.
- Produces: failing tests that define the projector contract for grouped skill activation.

- [ ] **Step 1: Add grouped activation test**

Append this test near existing tool and agent-only announcement tests:

```go
func TestAppEventProjectorGroupsSkillActivationWithUseSkill(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	started := projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	turnID := notificationTurnID(t, started, appwire.NotifyTurnStarted)

	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "use_skill",
		CallID:        "call_skill",
		ArgumentsJSON: `{"skill_name":"superpowers:using-superpowers"}`,
	}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{
		ToolName: "use_skill",
		CallID:   "call_skill",
		Output:   "Skill loaded",
	}})

	out := projector.Project(events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "superpowers:using-superpowers"}})
	if len(out) != 1 || out[0].Method != appwire.NotifyItemCompleted {
		t.Fatalf("notifications=%+v", out)
	}
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "commandExecution" || item.TurnID != turnID || item.ToolName != "use_skill" || item.CallID != "call_skill" {
		t.Fatalf("grouped item has wrong identity: %+v", item)
	}
	if item.Description == "Skill activated" {
		t.Fatalf("skill activation should not be projected as system message: %+v", item)
	}
	var raw struct {
		SkillActivation *struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"skill_activation"`
	}
	if err := json.Unmarshal(item.Raw, &raw); err != nil {
		t.Fatalf("Raw is not valid JSON: %v (%s)", err, item.Raw)
	}
	if raw.SkillActivation == nil || raw.SkillActivation.Name != "superpowers:using-superpowers" || raw.SkillActivation.Text != "Activated skill: superpowers:using-superpowers" {
		t.Fatalf("wrong skill activation raw: %+v raw=%s", raw.SkillActivation, item.Raw)
	}
}
```

- [ ] **Step 2: Add fallback test for unmatched activation**

```go
func TestAppEventProjectorLeavesUnmatchedSkillActivationStandalone(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "use_skill",
		CallID:        "call_skill",
		ArgumentsJSON: `{"skill_name":"superpowers:other"}`,
	}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{ToolName: "use_skill", CallID: "call_skill", Output: "Skill loaded"}})

	out := projector.Project(events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "superpowers:using-superpowers"}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "systemMessage" || item.Description != "Skill activated" || !strings.Contains(item.Text, "superpowers:using-superpowers") {
		t.Fatalf("unmatched activation should remain standalone system message: %+v", item)
	}
}
```

- [ ] **Step 3: Add legacy-key and ambiguity tests**

```go
func TestAppEventProjectorGroupsSkillActivationWithLegacyUseSkillNameArg(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "use_skill",
		CallID:        "call_skill",
		ArgumentsJSON: `{"name":"legacy-skill"}`,
	}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{ToolName: "use_skill", CallID: "call_skill", Output: "Skill loaded"}})

	out := projector.Project(events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "legacy-skill"}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "commandExecution" || item.CallID != "call_skill" || len(item.Raw) == 0 {
		t.Fatalf("legacy use_skill name arg should correlate: %+v", item)
	}
}

func TestAppEventProjectorDoesNotInferSkillActivationAcrossAssistantText(t *testing.T) {
	projector := NewAppEventProjector("th_1", "local:th_1")
	projector.Project(events.SessionEvent{Kind: events.EventUserInput, SessionID: "th_1", Data: events.UserInputData{Text: "hello"}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallStart, SessionID: "th_1", Data: events.ToolCallStartData{
		ToolName:      "use_skill",
		CallID:        "call_skill",
		ArgumentsJSON: `{"skill_name":"superpowers:using-superpowers"}`,
	}})
	projector.Project(events.SessionEvent{Kind: events.EventToolCallEnd, SessionID: "th_1", Data: events.ToolCallEndData{ToolName: "use_skill", CallID: "call_skill", Output: "Skill loaded"}})
	projector.Project(events.SessionEvent{Kind: events.EventAssistantTextEnd, SessionID: "th_1", Data: events.AssistantTextEndData{Text: "I will continue."}})

	out := projector.Project(events.SessionEvent{Kind: events.EventSkillActivated, SessionID: "th_1", Data: events.SkillActivatedData{Name: "superpowers:using-superpowers"}})
	item := notificationThreadItem(t, out, appwire.NotifyItemCompleted)
	if item.Type != "systemMessage" || item.Description != "Skill activated" {
		t.Fatalf("activation after intervening assistant text should remain standalone: %+v", item)
	}
}
```

- [ ] **Step 4: Run tests and verify failure**

Run:

```bash
go test ./internal/appprojector -run 'TestAppEventProjector.*SkillActivation' -count=1
```

Expected: the new grouped tests fail because `EventSkillActivated` still projects as `systemMessage`.

- [ ] **Step 5: Commit failing tests**

Do not commit failing tests on `main`. Instead, keep them unstaged if executing inline, or commit only if working in a disposable feature branch/worktree where red commits are acceptable by the task runner. Preferred command after implementation passes is in Task 2.

---

### Task 2: Implement Projector Correlation

**Files:**
- Modify: `internal/appprojector/appwire_projection.go`
- Test: `internal/appprojector/appwire_projection_test.go`

**Interfaces:**
- Consumes: tests from Task 1.
- Produces: `ThreadItem.Raw` containing `skill_activation` for correlated `use_skill` activations.

- [ ] **Step 1: Add projector state type and field**

In `AppEventProjector`, add a candidate field. If the struct is near the top of `appwire_projection.go`, add:

```go
type skillActivationCandidate struct {
	turnID string
	itemID string
	callID string
	skill  string
	valid  bool
}
```

and in `AppEventProjector`:

```go
skillCandidate skillActivationCandidate
```

- [ ] **Step 2: Add helper functions**

Add helpers in `appwire_projection.go`:

```go
func useSkillNameFromArgs(raw string) string {
	var args map[string]any
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return ""
	}
	for _, key := range []string{"skill_name", "name"} {
		if v, ok := args[key].(string); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func skillActivationRaw(name string) json.RawMessage {
	payload := struct {
		SkillActivation struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"skill_activation"`
	}{}
	payload.SkillActivation.Name = name
	payload.SkillActivation.Text = "Activated skill: " + name
	raw, _ := json.Marshal(payload)
	return raw
}
```

`appwire_projection.go` already imports `encoding/json` and `strings`; if either is missing in the actual file, add it.

- [ ] **Step 3: Record candidate on successful `use_skill` completion**

In the `EventToolCallEnd` case, after building `item` and before deleting the call ID, set:

```go
if data.ToolName == "use_skill" && data.Error == "" {
	p.skillCandidate = skillActivationCandidate{
		turnID: p.activeTurnID,
		itemID: item.ID,
		callID: data.CallID,
		skill:  useSkillNameFromArgs(data.ArgumentsJSON),
		valid:  useSkillNameFromArgs(data.ArgumentsJSON) != "",
	}
} else {
	p.skillCandidate = skillActivationCandidate{}
}
```

If `ToolCallEndData` does not carry `ArgumentsJSON`, use stored arguments from the start event by extending the tracked tool state to keep arguments by call ID. Do not parse `Description` for new code.

- [ ] **Step 4: Clear candidate across intervening non-output events**

Clear `p.skillCandidate` before projecting assistant text, user input, steering, system/lifecycle announcements other than `EventSkillActivated`, and any non-`use_skill` tool call start/end. Do not clear it for tool output deltas from the same call before completion.

The acceptance behavior is: after a completed matching `use_skill`, the very next non-tool-output event may be `EventSkillActivated`; anything else makes the candidate invalid.

- [ ] **Step 5: Project correlated `EventSkillActivated` as command execution**

Replace the current `EventSkillActivated` branch with:

```go
case events.EventSkillActivated:
	data := eventData[events.SkillActivatedData](event.Data)
	name := strings.TrimSpace(data.Name)
	if p.skillCandidate.valid && p.skillCandidate.turnID == p.activeTurnID && p.skillCandidate.skill == name {
		candidate := p.skillCandidate
		p.skillCandidate = skillActivationCandidate{}
		item := appwire.ThreadItem{
			Type:     "commandExecution",
			ID:       candidate.itemID,
			TurnID:   candidate.turnID,
			ToolName: "use_skill",
			CallID:   candidate.callID,
			Status:   appwire.TurnStatusCompleted,
			Raw:      skillActivationRaw(name),
		}
		return []AppNotification{p.notification(appwire.NotifyItemCompleted, map[string]any{
			"threadId": p.threadID,
			"ref":      p.ref,
			"turnId":   candidate.turnID,
			"item":     item,
		})}
	}
	p.skillCandidate = skillActivationCandidate{}
	return p.systemAnnouncement("skill", "Skill activated", "Activated skill: "+data.Name)
```

If `Status` type does not accept `appwire.TurnStatusCompleted`, use the existing item status string used in the file for completed command executions (`"completed"`).

- [ ] **Step 6: Run projector tests**

Run:

```bash
go test ./internal/appprojector -run 'TestAppEventProjector.*SkillActivation|TestAppEventProjectorProjectsAgentOnlyEventsAsSystemAnnouncements|TestAppEventProjectorProjectsAgentOnlyAnnouncementInActiveTurn' -count=1
```

Expected: PASS.

- [ ] **Step 7: Commit projector change**

```bash
git add internal/appprojector/appwire_projection.go internal/appprojector/appwire_projection_test.go
git commit -m "fix(appwire): group use_skill activation events"
```

---

### Task 3: Web Renderer Grouped Detail

**Files:**
- Modify: `cmd/serf-hub/assets/renderer-tools.js`
- Modify: `cmd/serf-hub/jstest/test-tool-renderers.js`

**Interfaces:**
- Consumes: `TOOL_CALL_END.tool_state` JSON with `skill_activation.name` and `skill_activation.text`.
- Produces: `.tool-call.use_skill` with optional body detail and no duplicate `.system-message` in grouped cases.

- [ ] **Step 1: Add failing grouped web test**

In `test-tool-renderers.js`, after the existing `use_skill target omits purpose already shown as intent` scenario, add:

```js
await scenario("use_skill grouped activation renders inside tool card", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["TOOL_CALL_START", { call_id: "s1", tool_name: "use_skill", arguments_json: JSON.stringify({ skill_name: "superpowers:using-superpowers" }) }],
  ["TOOL_CALL_END", { call_id: "s1", tool_name: "use_skill", output: "Skill loaded", tool_state: JSON.stringify({ skill_activation: { name: "superpowers:using-superpowers", text: "Activated skill: superpowers:using-superpowers" } }) }],
], ({ conv }) => {
  const card = conv.querySelector(".tool-call.use_skill");
  if (!card) return { ok: false, detail: "no use_skill card" };
  if (conv.querySelector(".system-message")) return { ok: false, detail: "grouped activation rendered standalone system message" };
  if (!card.textContent.includes("superpowers:using-superpowers")) return { ok: false, detail: "skill name missing from card" };
  card.dataset.expanded = "true";
  const body = card.querySelector(".tool-body");
  if (!body || !body.textContent.includes("Activated skill: superpowers:using-superpowers")) return { ok: false, detail: "activation detail missing from body" };
  return { ok: true };
});
```

- [ ] **Step 2: Add standalone system-message web test**

```js
await scenario("standalone skill activation system message still renders", [
  ["SESSION_START", { session_id: "01TEST" }],
  ["SYSTEM_MESSAGE", { title: "Skill activated", text: "Activated skill: standalone" }],
], ({ conv }) => {
  const msg = conv.querySelector(".system-message");
  if (!msg) return { ok: false, detail: "missing standalone system message" };
  if (!msg.textContent.includes("Skill activated")) return { ok: false, detail: "missing title" };
  if (!msg.textContent.includes("Activated skill: standalone")) return { ok: false, detail: "missing activation text" };
  return { ok: true };
});
```

- [ ] **Step 3: Run JS renderer test to verify grouped body failure**

Run the hub tool-renderer JS test directly:

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected before implementation: grouped test fails because `use_skill` has no body for `skill_activation`.

- [ ] **Step 4: Implement `use_skill` body renderer**

In `renderer-tools.js`, replace the current `useSkillRenderer` object with:

```js
const useSkillRenderer = Object.assign({}, defaultRenderer, {
  target: (a) => a.skill_name || a.name || "",
  body: (args, conversation) => {
    const div = document.createElement("div");
    div.className = "tool-body use-skill-body";
    conversation.appendChild(div);
    div.style.display = "none";
    return { div };
  },
  bodyEnd: (state, data) => {
    if (!state.body || !state.body.div) return;
    const st = parseToolState(data.tool_state);
    const activation = st && (st.skill_activation || st.skillActivation);
    const text = activation && (activation.text || activation.name && ("Activated skill: " + activation.name));
    if (!text) {
      state.body.div.textContent = "";
      state.body.div.style.display = "none";
      return;
    }
    state.body.div.style.display = "";
    state.body.div.textContent = text;
  },
});
```

- [ ] **Step 5: Run JS renderer tests**

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: PASS.

- [ ] **Step 6: Commit web renderer change**

```bash
git add cmd/serf-hub/assets/renderer-tools.js cmd/serf-hub/jstest/test-tool-renderers.js
git commit -m "fix(web): show skill activation inside use_skill"
```

---

### Task 4: TUI Duplicate Check and Raw Carrying

**Files:**
- Modify: `cmd/serf-tui/internal/transcript/types.go`
- Modify: `cmd/serf-tui/internal/transcript/item.go`
- Modify: `cmd/serf-tui/internal/transcript/reducer_test.go`
- Modify: `cmd/serf-tui/internal/msgrender/message.go`

**Interfaces:**
- Consumes: grouped `commandExecution` item with `Raw.skill_activation`.
- Produces: TUI message list with exactly one `MsgTool` and zero duplicate `MsgSystem` entries for grouped activation.

- [ ] **Step 1: Add failing TUI reducer test**

In `cmd/serf-tui/internal/transcript/reducer_test.go`, add:

```go
func TestReducerGroupedUseSkillActivationDoesNotCreateSystemDuplicate(t *testing.T) {
	thread := appwire.Thread{Turns: []appwire.Turn{{
		ID:     "turn_1",
		Status: appwire.TurnStatusCompleted,
		Items: []appwire.ThreadItem{{
			Type:          "commandExecution",
			ID:            "tool_1",
			TurnID:        "turn_1",
			ToolName:      "use_skill",
			CallID:        "call_skill",
			ArgumentsJSON: `{"skill_name":"superpowers:using-superpowers"}`,
			Output:        "Skill loaded",
			Status:        appwire.TurnStatusCompleted,
			Raw:           json.RawMessage(`{"skill_activation":{"name":"superpowers:using-superpowers","text":"Activated skill: superpowers:using-superpowers"}}`),
		}},
	}}}

	messages := MessagesFromThread(thread)
	if len(messages) != 1 {
		t.Fatalf("messages len=%d, want 1: %+v", len(messages), messages)
	}
	if messages[0].Kind != MsgTool || messages[0].Tool == nil || messages[0].Tool.Name != "use_skill" {
		t.Fatalf("message should be one use_skill tool: %+v", messages[0])
	}
	if messages[0].Tool.Raw == "" {
		t.Fatalf("grouped raw metadata should be carried for TUI renderers")
	}
}
```

If `reducer_test.go` does not already import `encoding/json`, add it.

- [ ] **Step 2: Add `Raw` field to `ToolCallInfo`**

In `types.go`, add:

```go
Raw string // raw JSON metadata from appwire ThreadItem.Raw
```

next to `RawArgs`.

- [ ] **Step 3: Carry `ThreadItem.Raw` into tool info**

In `item.go`, update `toolInfoFromThreadItem`:

```go
Raw: string(item.Raw),
```

Update `mergeThreadItemIntoToolInfo`:

```go
if len(item.Raw) > 0 {
	info.Raw = string(item.Raw)
}
```

- [ ] **Step 4: Decide whether TUI should display expanded activation body**

If the test owner wants only no duplicate, no renderer change is required. If displaying activation text is desired, add this in `RenderToolCall` after purpose body lines:

```go
if tc.Name == "use_skill" && strings.TrimSpace(tc.Raw) != "" {
	var raw struct {
		SkillActivation *struct {
			Name string `json:"name"`
			Text string `json:"text"`
		} `json:"skill_activation"`
	}
	if json.Unmarshal([]byte(tc.Raw), &raw) == nil && raw.SkillActivation != nil && strings.TrimSpace(raw.SkillActivation.Text) != "" {
		bodyLines = append(bodyLines, indentBlock(raw.SkillActivation.Text, th.IndentToolBody))
	}
}
```

If adding this, add `encoding/json` to `message.go` imports.

- [ ] **Step 5: Run TUI transcript tests**

```bash
go test ./cmd/serf-tui/internal/transcript -run 'TestReducerGroupedUseSkillActivationDoesNotCreateSystemDuplicate|Test.*SystemMessage|Test.*Tool' -count=1
```

Expected: PASS.

- [ ] **Step 6: Run TUI renderer tests**

```bash
go test ./cmd/serf-tui/internal/msgrender -run 'TestUseSkillRenderer|TestRenderToolCall' -count=1
```

Expected: PASS. If Task 4 Step 4 added renderer behavior, add or update a renderer test to assert the activation text appears once when expanded.

- [ ] **Step 7: Commit TUI verification/carrying change**

```bash
git add cmd/serf-tui/internal/transcript/types.go cmd/serf-tui/internal/transcript/item.go cmd/serf-tui/internal/transcript/reducer_test.go cmd/serf-tui/internal/msgrender/message.go cmd/serf-tui/internal/msgrender/message_test.go
git commit -m "test(tui): cover grouped use_skill activation"
```

Only include `message.go` / `message_test.go` if changed.

---

### Task 5: AppWire Replay/Conversion Regression

**Files:**
- Modify: `cmd/serf-hub/assets/appwire.js` if replay drops `item.raw`.
- Test: existing hub JS and TUI reducer tests.

**Interfaces:**
- Consumes: historical `ThreadItem.Raw` on `commandExecution` items.
- Produces: live and replay events both expose `TOOL_CALL_END.tool_state` to renderers.

- [ ] **Step 1: Verify appwire conversion already forwards raw**

Confirm `cmd/serf-hub/assets/appwire.js` maps command-execution `item.raw` into `TOOL_CALL_END.tool_state` in all thread replay and item-completed paths. The expected code shape is:

```js
tool_state: item.raw || ""
```

- [ ] **Step 2: Add a replay test only if a gap is found**

If any path drops `item.raw`, add a JS test fixture that feeds a thread/turn/item with `raw: { skill_activation: ... }` or a JSON string equivalent and assert the renderer sees `tool_state`.

- [ ] **Step 3: Run hub JS tests**

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: PASS.

- [ ] **Step 4: Commit only if code/test changed**

```bash
git add cmd/serf-hub/assets/appwire.js cmd/serf-hub/jstest/test-tool-renderers.js
git commit -m "test(appwire): preserve grouped tool raw metadata"
```

Skip this commit if Step 1 confirms no changes are needed.

---

### Task 6: Final Verification

**Files:**
- No production files unless a verification failure reveals a root cause.

**Interfaces:**
- Consumes: all previous task commits.
- Produces: verified branch ready for handoff.

- [ ] **Step 1: Run focused Go tests**

```bash
go test ./internal/appprojector ./cmd/serf-tui/internal/transcript ./cmd/serf-tui/internal/msgrender -count=1
```

Expected: PASS.

- [ ] **Step 2: Run focused web renderer tests**

```bash
node cmd/serf-hub/jstest/test-tool-renderers.js
```

Expected: PASS.

- [ ] **Step 3: Run broader deterministic package tests**

```bash
go test ./cmd/serf-tui/... ./internal/appprojector -count=1
```

Expected: PASS.

- [ ] **Step 4: Inspect final diff and status**

```bash
git status --short
git log --oneline -6
```

Expected: clean worktree except for intentional uncommitted files, and recent commits for projector, web, and TUI changes.

- [ ] **Step 5: Report result**

Report:

- files changed,
- tests run with pass/fail status,
- whether TUI had duplicate rendering before the fix,
- whether TUI now consumes grouped metadata or simply avoids duplicate system rows through the projector-level fix.
