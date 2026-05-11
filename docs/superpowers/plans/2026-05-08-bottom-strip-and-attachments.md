# Bottom strip redesign + end-to-end attachments

Date: 2026-05-08
Scope: matches the `workspace-bottom.html` mockup. End-to-end image
attachments. Click-outside dismissal of tasks/details panels.

## Outcome

The workspace bottom area matches the mockup:

```
┌──────────── bordered card (border, rounded, padded) ───────────┐
│  message the agent…                                            │
│  (auto-grows; cap ~50vh)                                       │
└────────────────────────────────────────────────────────────────┘

[＋] [⚠ full access] [openai · gpt-5 ▾]            [steer] [send ⌘↵]
─────────────────────────────────────────────────────────────────
~/git/serf/.worktrees/serf-hub · serf-hub@7c8450c    context [▓░░] 68k/200k · $0.84
```

Users can attach images by clicking `＋`, dragging onto the textarea, or pasting from the clipboard. Selected files render as small chips above the textarea with an `×` to remove. On send, attachments go through the daemon and end up in the user message as `llm.ContentImage` parts.

The tasks and details slide-over panels close on click outside (in addition to the existing `Esc` key).

## Out of scope

- Non-image file attachments (text/PDF/etc). Schema is extensible but only image/* lands now.
- `steer` button wiring — render the affordance, but its click handler can be a TODO until the steer-in-mid-turn UX is designed. (Daemon already has `/steer`.)
- `mode chip` (full access vs read-only) wiring — render the affordance, default to "full access", non-clickable until profile-switching support lands.
- `cost` display — wire if present in /status, hide the slot if not. Computing per-session cost is its own task.

## Step 1 — Backend: daemon `/input` accepts image attachments

### Files

- `server/server.go` — `InputRequest`, `inputCh`, `handleInput`, `InputCh`
- `cmd/serf/serve.go` — input loop calls `ProcessInput`
- `agent/session.go` — `ProcessInput` signature and message construction

### Changes

1. New shared type, exported from `server/server.go`:
   ```go
   type ImageAttachment struct {
       MediaType string `json:"media_type"` // "image/png" | "image/jpeg" | "image/webp" | "image/gif"
       Data      []byte `json:"data"`        // raw bytes; JSON un/marshals as base64
       Name      string `json:"name,omitempty"` // optional original filename
   }
   ```
2. Extend `InputRequest`:
   ```go
   type InputRequest struct {
       Text   string            `json:"text"`
       Images []ImageAttachment `json:"images,omitempty"`
   }
   ```
3. Replace `inputCh chan string` with a struct channel:
   ```go
   type InputMessage struct {
       Text   string
       Images []ImageAttachment
   }
   ```
   `Server.InputCh() <-chan InputMessage`. `handleInput` enqueues `InputMessage{Text: req.Text, Images: req.Images}`. Validation: at least one of Text or Images must be non-empty (currently text is required; relax to allow text-only OR image-with-empty-caption).
4. `cmd/serf/serve.go`: read `InputMessage` from `srv.InputCh()`. Call `sess.ProcessInput(ctx, msg.Text, msg.Images)` (new signature).
5. `agent/session.go::ProcessInput` signature change:
   ```go
   func (s *Session) ProcessInput(ctx context.Context, input string, images []server.ImageAttachment) (string, error)
   ```
   Inside, when constructing the user message, build a multi-part `llm.Message{Role: User, Content: [...]}`:
   - One `ContentText` part for `input` (skip if empty + images present)
   - One `ContentImage` part per image with `ImageData{Data, MediaType}`
   The current `appendTurn(TurnUserInput, llm.User(input))` becomes `appendTurn(TurnUserInput, msg)` where msg is built locally.
6. Backwards compatibility: keep callers that pass only text working. The simplest way is making `images` an optional variadic OR keeping `ProcessInput(ctx, text)` and adding `ProcessInputWithAttachments(ctx, text, images)`. **Decision: extend the signature to `(ctx, text, images)`. Update all call sites including tests.** Cleaner long-term than two methods.

### Tests

- `server/server_test.go`: POST `/input` with `{text:"caption", images:[{media_type:"image/png", data:"<base64>"}]}` produces `InputMessage{Text:"caption", Images:[{MediaType:"image/png", Data:[bytes]}]}` on `InputCh()`.
- `server/server_test.go`: POST with empty text + 1 image is accepted (200/202).
- `server/server_test.go`: POST with empty text + zero images returns 400.
- `agent/session_test.go`: `ProcessInput(ctx, "caption", []ImageAttachment{img})` produces a transcript turn whose user message contains both a text part and an image part with the right media type.

## Step 2 — Hub: forward attachments through `/s/<id>/send`

### Files

- `cmd/serf-hub/web.go` — `handleSend`

### Changes

The hub's `/s/<id>/send` accepts JSON. Extend it to accept either:
- `application/json` with `{text, images:[{media_type, data_base64, name}]}` — preferred.
- `multipart/form-data` with `text` + N `image` parts — easier for browsers if we end up needing it.

**Decision: JSON-only for now**. Browser does `FileReader.readAsDataURL`, strips the `data:image/png;base64,` prefix, and posts JSON. Avoids multipart parsing on the hub. We can add multipart later if 5MB+ attachments cause memory pressure.

`handleSend` reads `{text, images}`, marshals to daemon's `InputRequest`, POSTs to daemon `/input` (or via the resume-then-input retry path, unchanged). Same retry-with-resume logic on dead daemon.

### Tests

- `cmd/serf-hub/web_test.go`: POST `/s/<id>/send` with images forwards them to a stub daemon.
- `cmd/serf-hub/web_test.go`: empty text + 1 image is accepted.
- `cmd/serf-hub/web_test.go`: empty text + zero images returns 400.

## Step 3 — Frontend: bottom strip visual restructure

### Files

- `cmd/serf-hub/templates/partials/workspace.html` — restructure
- `cmd/serf-hub/templates/partials/input_strip.html` — restructure
- `cmd/serf-hub/assets/style.css` — new classes
- `cmd/serf-hub/assets/renderer.js` — auto-grow hook

### New HTML structure (workspace.html, bottom)

```html
<form class="workspace-input" data-input-form data-session-id="{{.ID}}">
  <div class="input-attachments" data-attachments></div>
  <div class="input-card" data-drop-zone>
    <textarea class="message-input" placeholder="message the agent…" autofocus rows="1"></textarea>
  </div>
  <div class="input-controls">
    <button type="button" class="input-btn" data-attach-trigger title="attach image">＋</button>
    <span class="mode-chip" data-mode="full">⚠ full access</span>
    <button type="button" class="input-chip model-chip" data-model-trigger>{{.Model}} <span class="chip-caret">▾</span></button>
    <span class="controls-spacer"></span>
    <button type="button" class="input-btn input-btn-ghost" data-steer-trigger>steer</button>
    <button type="submit" class="input-btn input-btn-primary send-btn">send <kbd>⌘↵</kbd></button>
  </div>
  <div id="input-status"
       class="input-status"
       hx-get="/s/{{.ID}}/state"
       hx-trigger="load, every 2s"
       hx-swap="innerHTML">
    {{template "input_status" .}}
  </div>
  <input type="file" data-file-picker accept="image/*" multiple hidden>
</form>
```

`input_strip.html` becomes `input_status.html` (or rename block) with the status row content:

```html
{{define "input_status"}}
{{if .WorkingDir}}<span class="cwd" title="{{.WorkingDir}}">{{.WorkingDir}}</span>{{end}}
{{if and .WorkingDir .Branch}}<span class="rule-dot">·</span>{{end}}
{{if .Branch}}<span class="branch">{{.Branch}}</span>{{end}}
<span class="status-spacer"></span>
{{if .ContextWindow}}<span class="context"><span>context</span><span class="context-bar"><span class="context-fill" style="width:{{.ContextPercent}}%"></span></span><span class="context-numbers">{{.ContextNumbers}}</span></span>{{end}}
{{if .Cost}}<span class="rule-dot">·</span><span class="cost">{{.Cost}}</span>{{end}}
{{end}}
```

Note: `.WorkingDir` and `.Branch` need to be added to `WorkspaceData` and populated from daemon `/status` (`StatusInfo.WorkingDir`, plus a new `git_branch` if not present — verify; otherwise pull from `SessionMeta.EnvInfo.GitBranch`).

### CSS additions (style.css)

```css
.workspace-input { padding: 12px 24px 14px; border-top: 1px solid var(--rule); }

.input-attachments { display: flex; gap: 6px; flex-wrap: wrap; padding-bottom: 8px; }
.input-attachments:empty { display: none; }
.attachment-chip { display: inline-flex; align-items: center; gap: 6px; padding: 4px 8px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; font-size: 11px; }
.attachment-chip .att-thumb { width: 18px; height: 18px; object-fit: cover; border-radius: 2px; }
.attachment-chip .att-remove { cursor: pointer; color: var(--text-muted); padding: 0 2px; border: none; background: transparent; }
.attachment-chip .att-remove:hover { color: var(--text); }

.input-card { background: var(--bg-raised); border: 1px solid var(--rule); border-radius: 6px; padding: 12px 14px; min-height: 80px; transition: border-color 0.15s; }
.input-card.drag-over { border-color: var(--state-processing); background: var(--bg); }
.message-input { width: 100%; min-height: 36px; max-height: 50vh; background: transparent; border: none; resize: none; color: var(--text); font: inherit; outline: none; line-height: 1.5; overflow-y: auto; }

.input-controls { display: flex; align-items: center; gap: 8px; padding: 8px 0 0; flex-wrap: wrap; }
.controls-spacer { flex: 1; }
.input-btn { display: inline-flex; align-items: center; gap: 5px; padding: 4px 12px; background: var(--bg); border: 1px solid var(--rule); border-radius: 4px; color: var(--text); font: inherit; font-size: 11.5px; cursor: pointer; }
.input-btn:hover { background: var(--bg-raised); }
.input-btn-ghost { color: var(--text-muted); }
.input-btn-primary { background: var(--state-processing); color: var(--bg); border-color: transparent; font-weight: 500; }
.input-btn-primary:hover { background: var(--state-processing); filter: brightness(1.1); }
.input-btn-primary kbd { background: rgba(0,0,0,0.2); border: 1px solid rgba(0,0,0,0.3); color: inherit; }

.input-chip { font-family: ui-monospace, "SFMono-Regular", monospace; font-size: 11px; }
.mode-chip { display: inline-flex; align-items: center; padding: 3px 9px; background: rgba(224,175,104,0.12); color: var(--state-warning); border-radius: 10px; font-size: 11px; }
.chip-caret { color: var(--text-muted); margin-left: 2px; }

.input-status { display: flex; align-items: center; gap: 14px; padding: 10px 0 0; margin-top: 6px; border-top: 1px solid var(--rule); font-size: 11px; color: var(--text-muted); flex-wrap: wrap; }
.input-status .cwd, .input-status .branch { font-family: ui-monospace, "SFMono-Regular", monospace; }
.input-status .cwd { max-width: 260px; overflow: hidden; text-overflow: ellipsis; white-space: nowrap; }
.input-status .status-spacer { flex: 1; }
.input-status .context { display: inline-flex; align-items: center; gap: 6px; }
.input-status .context-bar { width: 80px; height: 3px; background: var(--bg); border-radius: 2px; overflow: hidden; }
.input-status .context-fill { display: block; height: 100%; background: var(--state-processing); }
.input-status .context-numbers { font-family: ui-monospace, "SFMono-Regular", monospace; }
```

### Auto-grow JS (renderer.js, `bindInputForm`)

```js
const ta = form.querySelector(".message-input");
const grow = () => {
  ta.style.height = "auto";
  ta.style.height = Math.min(ta.scrollHeight, window.innerHeight * 0.5) + "px";
};
ta.addEventListener("input", grow);
grow();
```

Reset `ta.style.height = ""` after successful send so it collapses back.

### Tests

- `cmd/serf-hub/web_test.go`: workspace partial includes `data-attach-trigger`, `data-drop-zone`, `mode-chip`, `controls-spacer`, `input-status`.
- JSDOM `test-input-area.js` (new): autocompose grow on input, clamp at 50vh, reset on send.

## Step 4 — Frontend: attachment plumbing (file picker, drag-drop, paste)

### Files

- `cmd/serf-hub/assets/renderer.js` — attachment queue + send pipeline

### State

A `pendingAttachments: ImageAttachment[]` on SerfRenderer where each item is `{name, mediaType, dataBase64, thumbnail}` (thumbnail = data URL for the chip image).

### Triggers

1. `＋` button (`[data-attach-trigger]`): `<input type=file>` `.click()`. On change, read each file via FileReader.readAsDataURL, derive base64 from the data URL, push to queue.
2. Drag-and-drop on `.input-card`: `dragenter`/`dragleave`/`drop` events. `e.preventDefault()` everywhere. On `drop`, iterate `e.dataTransfer.files`, same upload pipeline.
3. Paste image from clipboard: textarea `paste` event. If `e.clipboardData.items` has any with `kind === "file"` and `type.startsWith("image/")`, capture and add. (Bonus.)

### Limits

- Max 10 attachments per send.
- Max 8 MB per file.
- Allowed media types: `image/png`, `image/jpeg`, `image/webp`, `image/gif`. Reject others with an inline error chip.

### Render

- Each pending attachment renders as `.attachment-chip` inside `.input-attachments` with: `<img src=thumbnail class=att-thumb>`, name, `×` remove button.
- Container shows nothing when empty (`:empty` display rule).

### Send

`bindInputForm.submit` builds:

```js
const body = JSON.stringify({
  text: ta.value.trim(),
  images: this.pendingAttachments.map(a => ({
    media_type: a.mediaType,
    data: a.dataBase64,
    name: a.name,
  })),
});
```

Send rules:
- Disallow empty (no text AND no images): keep send button disabled.
- Allow image-only.
- Reset `pendingAttachments` and re-render `.input-attachments` after a 2xx.

### Tests

- JSDOM `test-input-area.js`:
  - dispatching a `change` event on the file input with a stubbed FileList adds chips.
  - `drop` event with a stubbed dataTransfer adds chips.
  - clicking `×` on a chip removes it.
  - submitting with text + images posts a JSON body containing both.
  - submitting empty + empty does nothing.
  - submitting image-only is allowed.

## Step 5 — Click-outside dismissal of tasks/details panels

### Files

- `cmd/serf-hub/assets/renderer.js` — `toggleTasksPanel`, `toggleDetailsPanel`

### Approach

When opening a panel, register a `mousedown` capture-phase listener on `document`. If the click target is not inside the panel and not the trigger button, dismiss the panel and remove the listener. Also remove on `Esc` (existing).

```js
function bindClickOutside(panel, triggerSelector) {
  const onDown = (ev) => {
    if (panel.contains(ev.target)) return;
    if (ev.target.closest && ev.target.closest(triggerSelector)) return;
    panel.__pollTimer && clearInterval(panel.__pollTimer);
    panel.remove();
    setPanelToggleActive(triggerSelector, false);
    document.removeEventListener("mousedown", onDown, true);
  };
  document.addEventListener("mousedown", onDown, true);
  // also listen for the matching escClose path below.
}
```

Apply to both `toggleTasksPanel` and `toggleDetailsPanel`. Take care that opening the OTHER panel (which removes this one explicitly) doesn't double-fire — guard by checking `if (!panel.parentNode) return` inside the handler.

### Tests

- JSDOM `test-panels.js` (new):
  - clicking the tasks trigger opens the panel; clicking outside (somewhere in the conversation) closes it.
  - clicking inside the panel does NOT close it.
  - clicking the same trigger again still toggles closed.
  - opening details while tasks is open closes tasks AND opens details.
  - Esc still closes (regression).

## Step 6 — Glue + browser verification

1. Wire `WorkspaceData.WorkingDir` and `WorkspaceData.Branch` from daemon `/status` (verify `git_branch` exists; if not, expose it on `StatusInfo` and have the daemon populate from `s.envInfo.GitBranch`).
2. Run all Go tests and JSDOM tests.
3. Rebuild `/tmp/serf-hub`, restart, screenshot two scenarios via chrome MCP:
   - Empty conversation showing the bottom strip per mockup
   - Drag-and-drop or attach demo with 1 chip pending
   - Click-outside dismissal

## Subagent dispatch plan

Tasks that can run in parallel as Agent dispatches, each landing one numbered step:

1. **A — Daemon InputRequest + ProcessInput signature** (Step 1). Touches `server/server.go`, `agent/session.go`, `cmd/serf/serve.go`, related tests. Output: branch worktree with all tests passing.
2. **B — Hub /send forwards attachments** (Step 2). Depends on A's `InputRequest` shape. Touches `cmd/serf-hub/web.go`, `cmd/serf-hub/web_test.go`. Wait for A.
3. **C — Bottom strip CSS + HTML restructure** (Step 3). Independent of A/B for CSS; needs `WorkingDir`/`Branch` plumbed in `WorkspaceData` (small change, can do in C). Touches the templates + style.css + a small chunk of renderer.js (auto-grow only).
4. **D — Attachment UI + send pipeline** (Step 4). Depends on B (hub endpoint accepting images) AND on C (the `＋` button + chip area markup). Touches renderer.js + new JSDOM test.
5. **E — Click-outside dismissal** (Step 5). Independent. Touches renderer.js + new JSDOM test.

Run order:
- Round 1 (parallel): A, C, E.
- Round 2: B (after A merges).
- Round 3: D (after B + C).

I'll review each subagent's output, confirm tests + screenshots, then commit. Final round: my own browser verification with screenshots before declaring done.

## Acceptance

- Bottom area visually matches `workspace-bottom.html`.
- Textarea auto-grows on input; resets after send.
- Attaching a PNG and sending produces a user message in the transcript that the agent can describe (i.e., the LLM call includes the image content).
- Drag-and-drop, paste-from-clipboard, and `＋`-button all populate the queue.
- Click-outside closes both task and details panels; clicking inside does not.
- All Go tests + JSDOM tests pass.
