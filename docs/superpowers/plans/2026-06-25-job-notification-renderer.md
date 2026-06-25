# Job Notification Renderer Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Render job/watch/observer notification steering as coherent, readable web UI cards while preserving raw evidence.

**Architecture:** Keep the existing `STEERING_INJECTED` flow. Add notification parsing helpers to `renderer-format.js`, route parsed notification summaries through `appendSteeringMessage()` in `renderer.js`, and add compact card styles in `style.css`. Cover behavior with deterministic JSDOM renderer tests.

**Tech Stack:** Plain JavaScript browser bundle, JSDOM test harness, CSS, Go package tests for embedded assets.

## Global Constraints

- Do not change daemon notification formats, transcript storage, appwire delivery, job/watch semantics, or model-facing steering content.
- Do not add transcript-fetching behavior, transcript action buttons, or transcript links; render `transcript_ref` as readable text metadata only.
- Do not redesign unrelated steering messages.
- Notification parsing is client-side, deterministic, and best-effort.
- Notification content must be rendered with DOM text APIs, not injected as HTML.
- Raw notification content must remain inspectable.
- Tests must be deterministic and must not require network, provider credentials, model behavior, quota, or ambient developer machine state.

---

## File Structure

- Modify `cmd/serf-hub/assets/renderer-format.js`
  - Add notification parsing helpers.
  - Extend `classifySteering(text)` to return `kind: "notification"` for recognized notification-shaped steering.
  - Export any helper needed by tests only if existing test style requires it; otherwise keep helpers private.

- Modify `cmd/serf-hub/assets/renderer.js`
  - Add `appendNotificationCard(summary)`.
  - Route `summary.kind === "notification"` before generic steering fallback.
  - Keep existing task/system/unknown steering behavior unchanged.

- Modify `cmd/serf-hub/assets/style.css`
  - Add `.notification-card` styles near `.steering`/`.system-line` styles.
  - Add tone classes and metadata chip styles.

- Create `cmd/serf-hub/jstest/test-renderer-notifications.js`
  - Dedicated JSDOM scenarios for terminal delegate, shell failure, watch event, watch-send, observer callback, malformed fallback, and text escaping.

- Modify `cmd/serf-hub/jstest/run-all.sh`
  - Add the new JS test to the local JS test runner if the file enumerates tests explicitly.

---

### Task 1: Parse notification-shaped steering

**Files:**
- Modify: `cmd/serf-hub/assets/renderer-format.js`
- Test: `cmd/serf-hub/jstest/test-renderer-notifications.js`

**Interfaces:**
- Consumes: existing `classifySteering(text)` in `renderer-format.js`.
- Produces: `classifySteering(text)` returns this object for recognized notifications:

```js
{
  kind: "notification",
  notification: {
    type: "job" | "watch" | "watch-send" | "observer-callback",
    title: string,
    tone: "success" | "warning" | "error" | "neutral",
    attrs: Object,
    bodyText: string,
    prose: string,
    excerpt: string,
    rawText: string,
    communicate: null | {
      message: string,
      status: string,
      commitHashes: string[],
      testSummary: string,
      concerns: string[],
      artifacts: string[]
    }
  },
  cleanText: string
}
```

- Later tasks rely on `summary.notification` having the field names above.

- [ ] **Step 1: Create the failing notification parser/rendering test scaffold**

Create `cmd/serf-hub/jstest/test-renderer-notifications.js` with this initial content. This deliberately asserts parser-visible rendering classes before implementation exists, so it should fail.

```js
const { JSDOM } = require("jsdom");

function newHarness() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="01TEST"></header>
    <div id="conversation" data-session-id="01TEST" data-state="ended"></div>
    <form data-input-form data-session-id="01TEST">
      <textarea class="message-input"></textarea>
      <button class="send-btn" type="submit"></button>
    </form>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "https://test.local/" });

  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve([]), text: () => Promise.resolve("") });
  require("./load-renderer").evalRenderer(window);
  const conv = window.document.getElementById("conversation");
  window.SerfRenderer.init(conv);
  return { window, conv };
}

async function renderSteering(text) {
  const { window, conv } = newHarness();
  await new Promise(r => setTimeout(r, 30));
  window.SerfRenderer.handleData("STEERING_INJECTED", { text });
  await new Promise(r => setTimeout(r, 10));
  return { window, conv };
}

let allPass = true;
async function scenario(name, text, check) {
  const { conv } = await renderSteering(text);
  const result = check(conv);
  console.log((result.ok ? "PASS" : "FAIL") + " — " + name);
  if (!result.ok) {
    allPass = false;
    console.log("  detail: " + result.detail);
    console.log("  HTML: " + conv.innerHTML);
  }
}

function expectText(el, needle) {
  return el && el.textContent.includes(needle);
}

(async () => {
  const delegateEnvelope = JSON.stringify({
    message: "Status: DONE\nCommit hash(es): 3fbe7256\nOne-line test summary: node test passed; go test passed.\nConcerns: None",
    data: {
      status: "DONE",
      commit_hashes: ["3fbe7256"],
      test_summary: "node test passed; go test passed.",
      concerns: []
    },
    artifacts: []
  });

  await scenario("delegate completion notification parses", `<job-notification job_id="job_delegate" event="completed" job_type="delegate" status="completed" reason="" output_bytes="402" transcript_ref="local:delegate">
Job job_delegate completed. Output is available through read_transcript(transcript_ref="local:delegate") if needed.
excerpt:
${delegateEnvelope}
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card");
    if (!card) return { ok: false, detail: "missing notification card" };
    if (conv.querySelector(".steering .steering-verb") && conv.textContent.includes("steering injected")) return { ok: false, detail: "rendered generic steering" };
    if (!card.classList.contains("notification-card-success")) return { ok: false, detail: "missing success tone" };
    for (const needle of ["Job completed", "job_delegate", "delegate", "local:delegate", "DONE", "3fbe7256", "node test passed; go test passed."]) {
      if (!expectText(card, needle)) return { ok: false, detail: "missing " + needle };
    }
    return { ok: true };
  });

  if (!allPass) process.exit(1);
  console.log("PASS: notification renderer assertions");
})();
```

- [ ] **Step 2: Run the new test and verify it fails**

Run:

```bash
node cmd/serf-hub/jstest/test-renderer-notifications.js
```

Expected: FAIL with `missing notification card` or equivalent because `.notification-card` does not exist yet.

- [ ] **Step 3: Add parser helpers in `renderer-format.js`**

Add these helpers above `classifySteering(text)`:

```js
  function parseQuotedAttrs(src) {
    const attrs = {};
    String(src || "").replace(/([A-Za-z0-9_:-]+)="([^"]*)"/g, (_, k, v) => {
      attrs[k] = v;
      return "";
    });
    return attrs;
  }

  function splitNotificationExcerpt(body) {
    const text = String(body || "").trim();
    const marker = "\nexcerpt:\n";
    const idx = text.indexOf(marker);
    if (idx === -1) return { prose: text, excerpt: "" };
    return {
      prose: text.slice(0, idx).trim(),
      excerpt: text.slice(idx + marker.length).trim(),
    };
  }

  function compactStringArray(value) {
    if (!Array.isArray(value)) return [];
    return value.map(v => String(v || "").trim()).filter(Boolean);
  }

  function parseCommunicateEnvelope(text) {
    const raw = String(text || "").trim();
    if (!raw || raw[0] !== "{") return null;
    try {
      const parsed = JSON.parse(raw);
      const data = parsed && typeof parsed.data === "object" && parsed.data ? parsed.data : {};
      return {
        message: String(parsed && parsed.message || "").trim(),
        status: String(data.status || "").trim(),
        commitHashes: compactStringArray(data.commit_hashes),
        testSummary: String(data.test_summary || "").trim(),
        concerns: compactStringArray(data.concerns),
        artifacts: compactStringArray(parsed && parsed.artifacts),
      };
    } catch (e) {
      return null;
    }
  }

  function notificationTone(attrs, communicate) {
    const status = String((communicate && communicate.status) || attrs.status || attrs.event || "").toLowerCase();
    const exitCode = String(attrs.exit_code || "").trim();
    const concerns = communicate && communicate.concerns && communicate.concerns.length;
    if (status.includes("fail") || status === "error" || (exitCode && exitCode !== "0")) return "error";
    if (concerns || status === "cancelled" || status === "stopped" || attrs.event === "watch_send" || attrs.event === "watch") return "warning";
    if (status === "completed" || status === "done") return "success";
    return "neutral";
  }

  function titleForJobNotification(attrs, type) {
    if (type === "watch-send") return "Watch delivered";
    if (type === "watch") return "Watch triggered";
    const status = String(attrs.status || attrs.event || "notification").trim();
    if (!status) return "Job notification";
    return "Job " + status;
  }

  function parseJobNotification(stripped) {
    const m = String(stripped || "").match(/^<job-notification\s+([^>]*)>([\s\S]*)<\/job-notification>$/);
    if (!m) return null;
    const attrs = parseQuotedAttrs(m[1]);
    const bodyText = (m[2] || "").trim();
    const parts = splitNotificationExcerpt(bodyText);
    const communicate = parseCommunicateEnvelope(parts.excerpt);
    let type = "job";
    if ((attrs.event === "watch" || attrs.status === "watch") && !attrs.job_id) type = "watch";
    if (attrs.event === "watch_send") type = "watch-send";
    return {
      type,
      title: titleForJobNotification(attrs, type),
      tone: notificationTone(attrs, communicate),
      attrs,
      bodyText,
      prose: parts.prose,
      excerpt: parts.excerpt,
      rawText: stripped,
      communicate,
    };
  }

  function parseObserverCallback(stripped) {
    const text = String(stripped || "").trim();
    if (!/^Observer callback:\n/.test(text)) return null;
    const withoutHeader = text.replace(/^Observer callback:\n/, "");
    const outputMarker = "\noutput: ";
    const idx = withoutHeader.indexOf(outputMarker);
    let message = withoutHeader;
    let output = "";
    if (idx !== -1) {
      message = withoutHeader.slice(0, idx);
      output = withoutHeader.slice(idx + outputMarker.length);
    }
    message = message.replace(/^message: /, "").trim();
    output = output.trim();
    const communicate = parseCommunicateEnvelope(output);
    return {
      type: "observer-callback",
      title: "Observer callback",
      tone: notificationTone({ event: "observer_callback" }, communicate) === "success" ? "warning" : notificationTone({ event: "observer_callback" }, communicate),
      attrs: {},
      bodyText: withoutHeader.trim(),
      prose: message,
      excerpt: output,
      rawText: stripped,
      communicate,
    };
  }
```

Then update `classifySteering(text)` before the task-list/current-task checks or immediately before the final unknown return. Prefer before the final unknown return so existing task steering keeps priority:

```js
    const jobNotification = parseJobNotification(stripped);
    if (jobNotification) {
      return { kind: "notification", label: jobNotification.title, detail: "", cleanText: stripped, notification: jobNotification };
    }
    const observerNotification = parseObserverCallback(stripped);
    if (observerNotification) {
      return { kind: "notification", label: observerNotification.title, detail: "", cleanText: stripped, notification: observerNotification };
    }
```

- [ ] **Step 4: Add temporary minimal renderer branch to satisfy parser test**

In `cmd/serf-hub/assets/renderer.js`, add this branch in `appendSteeringMessage(text)` after the `full-list` branch and before the default generic `.steering` block:

```js
      if (summary.kind === "notification") {
        const n = summary.notification || {};
        const card = document.createElement("div");
        card.className = "notification-card notification-card-" + (n.tone || "neutral");
        const title = document.createElement("div");
        title.className = "notification-title";
        title.textContent = n.title || "Notification";
        card.appendChild(title);
        const textParts = [n.prose || ""];
        if (n.attrs) textParts.push(Object.values(n.attrs).join(" "));
        if (n.communicate) {
          textParts.push(n.communicate.status || "");
          textParts.push((n.communicate.commitHashes || []).join(" "));
          textParts.push(n.communicate.testSummary || "");
          textParts.push((n.communicate.concerns || []).join(" "));
        }
        const body = document.createElement("div");
        body.className = "notification-body";
        body.textContent = textParts.filter(Boolean).join(" ");
        card.appendChild(body);
        this.conversation.appendChild(card);
        return;
      }
```

This is intentionally minimal; Task 2 replaces it with the final structured card.

- [ ] **Step 5: Run the focused test and verify it passes**

Run:

```bash
node cmd/serf-hub/jstest/test-renderer-notifications.js
```

Expected: `PASS: notification renderer assertions`.

- [ ] **Step 6: Commit Task 1**

```bash
git add cmd/serf-hub/assets/renderer-format.js cmd/serf-hub/assets/renderer.js cmd/serf-hub/jstest/test-renderer-notifications.js
git commit -m "feat(web): parse notification steering"
```

---

### Task 2: Render structured notification cards and styles

**Files:**
- Modify: `cmd/serf-hub/assets/renderer.js`
- Modify: `cmd/serf-hub/assets/style.css`
- Modify: `cmd/serf-hub/jstest/test-renderer-notifications.js`

**Interfaces:**
- Consumes: `summary.notification` object from Task 1.
- Produces: `appendNotificationCard(summary)` in `renderer.js`, called by `appendSteeringMessage`.
- DOM contract:
  - `.notification-card.notification-card-<tone>` root
  - `.notification-card-header`
  - `.notification-card-title`
  - `.notification-card-meta`
  - `.notification-card-chip`
  - `.notification-card-summary`
  - `.notification-card-raw` details element

- [ ] **Step 1: Extend tests for all required notification variants**

Append these scenarios inside the async IIFE in `cmd/serf-hub/jstest/test-renderer-notifications.js`, before the final `if (!allPass)` block:

```js
  await scenario("shell failure notification shows failure metadata", `<job-notification job_id="job_shell" event="failed" job_type="shell" status="failed" reason="exit_nonzero" output_bytes="128" exit_code="2" transcript_ref="job:job_shell">
Job job_shell failed. Output is available through read_transcript(transcript_ref="job:job_shell") if needed.
excerpt:
command failed on line 1
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card-error");
    if (!card) return { ok: false, detail: "missing error card" };
    for (const needle of ["Job failed", "job_shell", "shell", "exit_nonzero", "exit 2", "128 B", "job:job_shell", "command failed on line 1"]) {
      if (!expectText(card, needle)) return { ok: false, detail: "missing " + needle };
    }
    return { ok: true };
  });

  await scenario("watch event notification renders as watch card", `<job-notification job_id="" event="watch" job_type="watch" status="watch" reason="event: ASSISTANT_TEXT_END" output_bytes="0">
Watch event triggered: event: ASSISTANT_TEXT_END.
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card-warning");
    if (!card) return { ok: false, detail: "missing warning card" };
    if (!expectText(card, "Watch triggered")) return { ok: false, detail: "missing watch title" };
    if (!expectText(card, "event: ASSISTANT_TEXT_END")) return { ok: false, detail: "missing trigger reason" };
    if (expectText(card, "read_transcript")) return { ok: false, detail: "watch event must not suggest read_transcript" };
    return { ok: true };
  });

  await scenario("watch-send notification renders delivery metadata", `<job-notification job_id="job_w" event="watch_send" delivery_id="dlv_1" trigger="output_match: ready">
frame text from watcher
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card-warning");
    if (!card) return { ok: false, detail: "missing watch-send card" };
    for (const needle of ["Watch delivered", "job_w", "dlv_1", "output_match: ready", "frame text from watcher"]) {
      if (!expectText(card, needle)) return { ok: false, detail: "missing " + needle };
    }
    return { ok: true };
  });

  await scenario("observer callback renders in notification family", `Observer callback:
message: Child finished
output: {"message":"Status: DONE","data":{"status":"DONE","concerns":[]},"artifacts":["report.md"]}`, (conv) => {
    const card = conv.querySelector(".notification-card");
    if (!card) return { ok: false, detail: "missing observer card" };
    for (const needle of ["Observer callback", "Child finished", "DONE", "report.md"]) {
      if (!expectText(card, needle)) return { ok: false, detail: "missing " + needle };
    }
    return { ok: true };
  });

  await scenario("malformed notification falls back with raw evidence", `<job-notification job_id="broken">
missing closing tag`, (conv) => {
    const card = conv.querySelector(".notification-card");
    if (card) return { ok: false, detail: "malformed block should not render notification card" };
    const steering = conv.querySelector(".steering");
    if (!steering) return { ok: false, detail: "malformed block should fall back to steering" };
    if (!expectText(steering, "missing closing tag")) return { ok: false, detail: "raw fallback missing" };
    return { ok: true };
  });

  await scenario("notification text is escaped", `<job-notification job_id="job_html" event="completed" job_type="shell" status="completed" reason="" output_bytes="0">
Job job_html completed. Complete output below.
excerpt:
<img src=x onerror="window.__xss=1"> literal
</job-notification>`, (conv) => {
    const card = conv.querySelector(".notification-card");
    if (!card) return { ok: false, detail: "missing card" };
    if (card.querySelector("img")) return { ok: false, detail: "notification inserted HTML" };
    if (!expectText(card, "<img src=x")) return { ok: false, detail: "escaped text not visible" };
    return { ok: true };
  });
```

- [ ] **Step 2: Run the expanded test and verify it fails**

Run:

```bash
node cmd/serf-hub/jstest/test-renderer-notifications.js
```

Expected: FAIL on at least metadata formatting, raw details, or missing structured classes because Task 1 used a minimal renderer.

- [ ] **Step 3: Replace the minimal branch with `appendNotificationCard`**

In `cmd/serf-hub/assets/renderer.js`, add helper methods near `appendSteeringMessage(text)`:

```js
    notificationMetaRows(n) {
      const attrs = n && n.attrs || {};
      const rows = [];
      const push = (label, value) => {
        value = String(value || "").trim();
        if (value) rows.push([label, value]);
      };
      push("job", attrs.job_id);
      push("type", attrs.job_type);
      push("status", attrs.status || attrs.event);
      push("reason", attrs.reason);
      if (attrs.exit_code) push("exit", attrs.exit_code);
      if (attrs.output_bytes && attrs.output_bytes !== "0") push("output", formatBytes(parseInt(attrs.output_bytes, 10)) || attrs.output_bytes + " B");
      push("transcript", attrs.transcript_ref);
      push("delivery", attrs.delivery_id);
      push("trigger", attrs.trigger);
      return rows;
    },

    appendNotificationText(parent, className, text) {
      text = String(text || "").trim();
      if (!text) return null;
      const el = document.createElement("div");
      el.className = className;
      el.textContent = text;
      parent.appendChild(el);
      return el;
    },

    appendNotificationCard(summary) {
      const n = summary.notification || {};
      const card = document.createElement("div");
      card.className = "notification-card notification-card-" + (n.tone || "neutral") + " notification-card-" + (n.type || "unknown");

      const header = document.createElement("div");
      header.className = "notification-card-header";
      const glyph = document.createElement("span");
      glyph.className = "notification-card-glyph";
      glyph.setAttribute("aria-hidden", "true");
      glyph.textContent = n.type === "watch" || n.type === "watch-send" ? "◌" : (n.type === "observer-callback" ? "↩" : "●");
      header.appendChild(glyph);
      const title = document.createElement("span");
      title.className = "notification-card-title";
      title.textContent = n.title || "Notification";
      header.appendChild(title);
      card.appendChild(header);

      const rows = this.notificationMetaRows(n);
      if (rows.length) {
        const meta = document.createElement("div");
        meta.className = "notification-card-meta";
        for (const [label, value] of rows) {
          const chip = document.createElement("span");
          chip.className = "notification-card-chip";
          chip.textContent = label + " " + value;
          meta.appendChild(chip);
        }
        card.appendChild(meta);
      }

      const summaryEl = document.createElement("div");
      summaryEl.className = "notification-card-summary";
      this.appendNotificationText(summaryEl, "notification-card-prose", n.prose);
      if (n.communicate) {
        this.appendNotificationText(summaryEl, "notification-card-message", n.communicate.message);
        this.appendNotificationText(summaryEl, "notification-card-status", n.communicate.status ? "status " + n.communicate.status : "");
        this.appendNotificationText(summaryEl, "notification-card-commits", (n.communicate.commitHashes || []).length ? "commits " + n.communicate.commitHashes.join(", ") : "");
        this.appendNotificationText(summaryEl, "notification-card-tests", n.communicate.testSummary ? "tests " + n.communicate.testSummary : "");
        this.appendNotificationText(summaryEl, "notification-card-concerns", (n.communicate.concerns || []).length ? "concerns " + n.communicate.concerns.join("; ") : "concerns none");
        this.appendNotificationText(summaryEl, "notification-card-artifacts", (n.communicate.artifacts || []).length ? "artifacts " + n.communicate.artifacts.join(", ") : "");
      } else {
        this.appendNotificationText(summaryEl, "notification-card-excerpt", n.excerpt);
      }
      if (summaryEl.childNodes.length) card.appendChild(summaryEl);

      const raw = document.createElement("details");
      raw.className = "notification-card-raw";
      const rawSummary = document.createElement("summary");
      rawSummary.textContent = "raw notification";
      raw.appendChild(rawSummary);
      const pre = document.createElement("pre");
      pre.textContent = n.rawText || summary.cleanText || "";
      raw.appendChild(pre);
      card.appendChild(raw);

      this.conversation.appendChild(card);
    },
```

Then replace the minimal `summary.kind === "notification"` branch with:

```js
      if (summary.kind === "notification") {
        this.appendNotificationCard(summary);
        return;
      }
```

- [ ] **Step 4: Add CSS for notification cards**

Add this block in `cmd/serf-hub/assets/style.css` near the existing `.steering` styles:

```css
.notification-card {
  margin: var(--space-4) 0 var(--space-4) var(--space-7);
  padding: var(--space-4);
  border: 1px solid var(--rule);
  border-left: 3px solid var(--text-muted);
  border-radius: var(--radius-md);
  background: var(--bg-raised);
  color: var(--text);
  font-size: var(--text-sm);
}
.notification-card-success { border-left-color: var(--state-success); }
.notification-card-warning { border-left-color: var(--state-warning); }
.notification-card-error { border-left-color: var(--error); }
.notification-card-neutral { border-left-color: var(--text-muted); }
.notification-card-header { display: flex; align-items: center; gap: var(--space-2); margin-bottom: var(--space-2); }
.notification-card-glyph { color: var(--text-muted); font-family: var(--font-mono); }
.notification-card-title { font-weight: 600; color: var(--text); }
.notification-card-meta { display: flex; flex-wrap: wrap; gap: var(--space-2); margin: var(--space-2) 0; }
.notification-card-chip { font-family: var(--font-mono); font-size: var(--text-xs); color: var(--text-muted); border: 1px solid var(--rule); border-radius: var(--radius-pill); padding: var(--space-1) var(--space-2); background: var(--bg); overflow-wrap: anywhere; }
.notification-card-summary { display: grid; gap: var(--space-1); color: var(--text-muted); line-height: var(--leading-snug); }
.notification-card-message,
.notification-card-tests,
.notification-card-excerpt { white-space: pre-wrap; overflow-wrap: anywhere; }
.notification-card-status,
.notification-card-commits,
.notification-card-concerns,
.notification-card-artifacts { font-family: var(--font-mono); font-size: var(--text-xs); }
.notification-card-raw { margin-top: var(--space-3); color: var(--text-muted); }
.notification-card-raw > summary { cursor: pointer; font-size: var(--text-xs); }
.notification-card-raw pre { margin: var(--space-2) 0 0; padding: var(--space-3); border-radius: var(--radius-md); background: var(--bg); color: var(--text-muted); font-family: var(--font-mono); font-size: var(--text-xs); white-space: pre-wrap; overflow-wrap: anywhere; }
```

If any CSS variable name is not present in `style.css`, use an existing equivalent from the file instead of introducing a new variable.

- [ ] **Step 5: Run the focused notification renderer test**

Run:

```bash
node cmd/serf-hub/jstest/test-renderer-notifications.js
```

Expected: `PASS: notification renderer assertions`.

- [ ] **Step 6: Commit Task 2**

```bash
git add cmd/serf-hub/assets/renderer.js cmd/serf-hub/assets/style.css cmd/serf-hub/jstest/test-renderer-notifications.js
git commit -m "feat(web): render notification steering cards"
```

---

### Task 3: Integrate notification tests and verify regressions

**Files:**
- Test: `cmd/serf-hub/jstest/test-renderer-notifications.js`
- Test: existing renderer/CSS tests

**Interfaces:**
- Consumes: final notification parser and renderer from Tasks 1-2.
- Produces: verification evidence. `cmd/serf-hub/jstest/run-all.sh` already auto-discovers `test-*.js`, so creating `test-renderer-notifications.js` is sufficient for suite integration and this task does not modify the runner.

- [ ] **Step 1: Confirm JS test auto-discovery**

Run:

```bash
sed -n '1,80p' cmd/serf-hub/jstest/run-all.sh
```

Expected: output includes this loop, proving the new `test-renderer-notifications.js` file is included automatically:

```sh
for test in test-*.js; do
  out=$(timeout "$TIMEOUT" node "$test" 2>&1)
```

Do not modify `run-all.sh` for this feature.

- [ ] **Step 2: Run focused tests**

Run:

```bash
node cmd/serf-hub/jstest/test-renderer-notifications.js
node cmd/serf-hub/jstest/test-renderer-advanced.js
node cmd/serf-hub/jstest/test-pane-and-sidebar-css.js
```

Expected:

```text
PASS: notification renderer assertions
```

and existing PASS output from the other two scripts. If an existing test fails, inspect the failure and fix the regression rather than weakening assertions.

- [ ] **Step 3: Run hub package tests**

Run:

```bash
go test ./cmd/serf-hub -count=1
```

Expected:

```text
ok  primeradiant.com/serf/cmd/serf-hub
```

The elapsed time may differ.

- [ ] **Step 4: Commit test integration if any file changed**

If `run-all.sh` changed:

```bash
git add cmd/serf-hub/jstest/run-all.sh
git commit -m "test(web): include notification renderer coverage"
```

If no file changed, do not create an empty commit.

- [ ] **Step 5: Report final task evidence**

Record the exact commands run and their PASS/ok output in the subagent final report. Include the commit hash(es) produced by Tasks 1-3.
