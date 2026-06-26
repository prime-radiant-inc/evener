(function () {
  "use strict";

  // Stateless transcript helpers shared by the renderer modules: byte/
  // duration/path formatting, JSON/arg parsing, steering + system-message
  // classification, the task-description cache, and small DOM builders for
  // task rows and the image lightbox. None of these touch SerfRenderer
  // instance state, so this file loads before renderer.js and publishes its
  // helpers on window.SerfRendererInternal for renderer.js to import.

  // itemDataToBase64 normalizes composer-attachment payloads for the legacy
  // /send and /queue REST shapes (used when SerfAppwire isn't installed —
  // e.g. JSDOM tests, replay-only configurations). The appwire path does
  // this encoding inline; this is the fallback. Mirrors appwire.js'
  // encodeAttachmentData duck-typing so cross-realm ArrayBuffer inputs
  // (test harnesses) still round-trip cleanly.
  function itemDataToBase64(data) {
    if (data == null) return "";
    if (typeof data === "string") return data;
    let bytes;
    if (ArrayBuffer.isView(data)) {
      bytes = data.buffer
        ? new Uint8Array(data.buffer, data.byteOffset || 0, data.byteLength)
        : data;
    } else if (typeof data === "object" && typeof data.byteLength === "number") {
      bytes = new Uint8Array(data);
    } else {
      return "";
    }
    const CHUNK = 0x8000;
    let binary = "";
    for (let i = 0; i < bytes.length; i += CHUNK) {
      const slice = bytes.subarray(i, i + CHUNK);
      binary += String.fromCharCode.apply(null, slice);
    }
    return (typeof btoa === "function")
      ? btoa(binary)
      : Buffer.from(binary, "binary").toString("base64");
  }

  function imagePlaceholderForCount(n) {
    if (n === 1) return "[image]";
    if (n > 1) return "[" + n + " images]";
    return "";
  }

  function normalizedJobRefData(data) {
    data = data || {};
    const outputBytes = data.outputBytes != null ? data.outputBytes : data.output_bytes;
    return {
      jobId: data.jobId || data.job_id || "",
      jobType: data.jobType || data.job_type || "",
      status: data.status || "",
      reason: data.reason || "",
      outputBytes,
      transcriptRef: data.transcriptRef || data.transcript_ref || "",
      label: data.label || data.task || data.description || "",
    };
  }

  const transcriptStatusPrefsKey = "serf-hub.transcript.systemStatus";
  const transcriptStatusDefaults = {
    roundTimings: false,
    hookExitsAll: false,
    hookExitsNormal: false,
    promptLoaded: false,
  };

  function readTranscriptStatusPrefs() {
    let raw = "";
    try { raw = window.localStorage && window.localStorage.getItem(transcriptStatusPrefsKey); } catch (e) {}
    let parsed = {};
    if (raw) {
      try { parsed = JSON.parse(raw) || {}; } catch (e) { parsed = {}; }
    }
    parsed = Object.assign({}, transcriptStatusDefaults, parsed);
    return {
      roundTimings: parsed.roundTimings === true,
      hookExitsAll: parsed.hookExitsAll === true,
      hookExitsNormal: parsed.hookExitsNormal === true,
      promptLoaded: parsed.promptLoaded === true,
    };
  }

  function canonicalSystemTitle(title) {
    return String(title || "").trim().toLowerCase();
  }

  function systemMessagePreferenceKey(data) {
    const title = canonicalSystemTitle(data && data.title);
    const text = String((data && data.text) || "");
    if (title === "system prompt" || title === "prompt loaded") return "promptLoaded";
    if (title.indexOf("round timing") >= 0 || /\bround[_ ]timings?\b/i.test(text)) return "roundTimings";
    return "";
  }

  function systemMessageDisplayTitle(title) {
    const canonical = canonicalSystemTitle(title);
    return canonical === "system prompt" || canonical === "prompt loaded" ? "Prompt Loaded" : String(title || "System");
  }

  function hookExitCode(text) {
    const s = String(text || "");
    const match = /\bhook\b.*\bexit\s+(-?\d+)\b/i.exec(s) || /\bexit\s+(-?\d+)\b.*\bhook\b/i.exec(s);
    if (!match) return null;
    const code = Number(match[1]);
    return Number.isFinite(code) ? code : null;
  }

  function shouldRenderSystemMessage(data) {
    const key = systemMessagePreferenceKey(data);
    if (!key) return true;
    return readTranscriptStatusPrefs()[key] === true;
  }

  function shouldRenderSystemLine(text) {
    if (/\bround[_ ]timings?\b/i.test(String(text || ""))) {
      return readTranscriptStatusPrefs().roundTimings === true;
    }
    const code = hookExitCode(text);
    if (code === null) return true;
    const prefs = readTranscriptStatusPrefs();
    return prefs.hookExitsAll === true || (code === 0 && prefs.hookExitsNormal === true);
  }

  function partialFetch(path, options) {
    options = options || {};
    const headers = new Headers(options.headers || {});
    headers.set("HX-Request", "true");
    options.headers = headers;
    return fetch(path, options);
  }

  function sessionPartialPath(id, name) {
    return "/_partials/s/" + encodeURIComponent(id) + "/" + name;
  }

  function autoLabel(text) {
    return "before " + text.slice(0, 40).replace(/\s+/g, " ").trim();
  }

  // openImageLightboxSet shows a full-size image overlay over a SET of images
  // and lets the reader page through it one at a time: Esc closes, ←/→ (and
  // the on-screen prev/next buttons) navigate, wrapping around. A single
  // shared overlay instance is reused — a fresh open replaces any prior one.
  // `items` is [{src, name, dims?}]; `startIndex` selects the first shown.
  function openImageLightboxSet(items, startIndex) {
    const set = (items || []).filter((it) => it && it.src);
    if (set.length === 0) return;
    let i = Math.max(0, Math.min(startIndex || 0, set.length - 1));
    const multi = set.length > 1;

    const existing = document.getElementById("image-lightbox");
    if (existing) existing.remove();
    const overlay = document.createElement("div");
    overlay.id = "image-lightbox";
    overlay.className = "image-lightbox";

    const img = document.createElement("img");
    overlay.appendChild(img);

    const caption = document.createElement("div");
    caption.className = "image-lightbox-caption";
    overlay.appendChild(caption);

    // Prev/next controls + position indicator only earn their place for a set.
    let pos = null, prevBtn = null, nextBtn = null;
    if (multi) {
      prevBtn = document.createElement("button");
      prevBtn.type = "button";
      prevBtn.className = "image-lightbox-nav image-lightbox-prev";
      prevBtn.setAttribute("aria-label", "Previous (←)");
      prevBtn.textContent = "‹";
      nextBtn = document.createElement("button");
      nextBtn.type = "button";
      nextBtn.className = "image-lightbox-nav image-lightbox-next";
      nextBtn.setAttribute("aria-label", "Next (→)");
      nextBtn.textContent = "›";
      pos = document.createElement("div");
      pos.className = "image-lightbox-pos";
      overlay.append(prevBtn, nextBtn, pos);
    }

    function render() {
      const m = set[i];
      img.src = m.src;
      img.alt = m.name || "";
      const bits = [];
      if (m.name) bits.push(m.name);
      if (m.dims) bits.push(m.dims);
      caption.textContent = bits.join(" · ");
      caption.hidden = bits.length === 0;
      if (pos) pos.textContent = (i + 1) + " / " + set.length;
    }
    function step(d) { i = (i + d + set.length) % set.length; render(); }

    const close = () => {
      overlay.remove();
      document.removeEventListener("keydown", onKey);
    };
    const onKey = (e) => {
      if (e.key === "Escape") { close(); return; }
      if (!multi) return;
      if (e.key === "ArrowLeft") { e.preventDefault(); step(-1); }
      else if (e.key === "ArrowRight") { e.preventDefault(); step(1); }
    };
    // Click the backdrop (not a nav button or the image) to dismiss.
    overlay.addEventListener("click", (e) => { if (e.target === overlay) close(); });
    if (prevBtn) prevBtn.addEventListener("click", (e) => { e.stopPropagation(); step(-1); });
    if (nextBtn) nextBtn.addEventListener("click", (e) => { e.stopPropagation(); step(1); });
    document.addEventListener("keydown", onKey);
    document.body.appendChild(overlay);
    render();
  }

  // openImageLightbox shows a full-size overlay for ONE image — the
  // single-image path. Backed by openImageLightboxSet with a one-item set.
  function openImageLightbox(src, name) {
    openImageLightboxSet([{ src, name }], 0);
  }

  // Client-side cache of task id → description, keyed by integer id. Seeded
  // from: task_list append calls (args.tasks), task_list update calls (no
  // descriptions in args; ignored), STEERING_INJECTED full-list parses, and
  // the session's /tasks endpoint when the side panel is open or the
  // background poller fires. Used by the system-line renderer to print
  // task transitions by description rather than by raw id.
  const taskDescriptions = new Map();
  const taskDetails = new Map();
  function rememberTask(t) {
    if (!t || t.id == null) return;
    const id = Number(t.id);
    if (!Number.isFinite(id)) return;
    const prev = taskDetails.get(id) || {};
    const next = Object.assign({}, prev, t, { id });
    if (!next.description && next.title) next.description = next.title;
    if (next.description) taskDescriptions.set(id, next.description);
    taskDetails.set(id, next);
  }
  function parseArgs(json) {
    if (!json) return {};
    try { return JSON.parse(json); } catch (e) { return {}; }
  }

  function toolIntent(data, args) {
    data = data || {};
    args = args || {};
    for (const value of [data.description, data.intent, args.intent, args.purpose, args.description]) {
      const text = String(value || "").trim();
      if (text) return text;
    }
    return "";
  }

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
    const outerStatus = String(attrs.status || "").toLowerCase();
    const outerEvent = String(attrs.event || "").toLowerCase();
    const communicateStatus = String(communicate && communicate.status || "").toLowerCase();
    const exitCode = String(attrs.exit_code || "").trim();
    const concerns = communicate && communicate.concerns && communicate.concerns.length;
    if (outerStatus.includes("fail") || outerEvent.includes("fail") || outerStatus === "error" || outerEvent === "error" || (exitCode && exitCode !== "0")) return "error";
    const status = communicateStatus || outerStatus || outerEvent;
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
    const observerTone = notificationTone({ event: "observer_callback" }, communicate);
    return {
      type: "observer-callback",
      title: "Observer callback",
      tone: observerTone === "success" ? "warning" : observerTone,
      attrs: {},
      bodyText: withoutHeader.trim(),
      prose: message,
      excerpt: output,
      rawText: stripped,
      communicate,
    };
  }

  // classifySteering inspects steering text and returns:
  //   { kind, label, detail, cleanText, taskID?, taskTitle?, tasks? }
  // Recognized kinds:
  //   - "current-task" (suppressed inline; seeds task description cache)
  //   - "full-list"    (rendered as compact pointer; parses Task list lines)
  //   - "tasks-done"   (kept as steering divider)
  //   - "task-nudge"   (kept as steering divider)
  //   - "loop"         (kept)
  //   - "read-only"    (kept)
  //   - "transcript"   (kept)
  //   - "notification" (rendered as a structured notification card)
  //   - "unknown"      (kept)
  function classifySteering(text) {
    const stripped = text
      .replace(/^\s*<SYSTEM-REMINDER>\s*/i, "")
      .replace(/\s*<\/SYSTEM-REMINDER>\s*$/i, "")
      .trim();

    const taskMatch = stripped.match(/<CURRENT-TASK\s+id="(\d+)">([\s\S]*?)<\/CURRENT-TASK>/);
    if (taskMatch) {
      const body = taskMatch[2] || "";
      const titleMatch = body.match(/<TITLE>([\s\S]*?)<\/TITLE>/);
      const instrMatch = body.match(/<INSTRUCTIONS>([\s\S]*?)<\/INSTRUCTIONS>/);
      const title = titleMatch ? titleMatch[1].trim() : "";
      const prompt = instrMatch ? instrMatch[1].trim() : "";
      return {
        kind: "current-task",
        label: "current task",
        detail: "#" + taskMatch[1] + " " + title,
        cleanText: stripped,
        taskID: taskMatch[1],
        taskTitle: title,
        taskPrompt: prompt,
      };
    }
    if (/^Task list:/m.test(stripped)) {
      const lines = stripped.split("\n").filter(l => /^\s*\[/.test(l));
      const total = lines.length;
      const pending = lines.filter(l => /\[(open|in_progress)\]/.test(l)).length;
      const tasks = lines.map(line => {
        // Greedy capture for description so a trailing "[WIP]" inside the
        // title doesn't get clipped. Strip optional " [reasoning]" or
        // " (depends_on: …)" suffixes only when they're at end-of-line.
        const m = line.match(/\[(\w+)\]\s*#(\d+):\s*(.+)$/);
        if (!m) return null;
        let description = m[3].trim();
        description = description.replace(/\s*\(depends_on:[^)]*\)\s*$/, "");
        // Only strip a trailing reasoning-effort token.
        description = description.replace(/\s*\[(minimal|low|medium|high|xhigh|max)\]\s*$/, "");
        return { status: m[1], id: parseInt(m[2], 10), description: description.trim() };
      }).filter(Boolean);
      return {
        kind: "full-list",
        label: "task list",
        detail: total + " items · " + pending + " pending",
        cleanText: stripped,
        tasks,
      };
    }
    if (/completed all tasks/.test(stripped)) {
      return { kind: "tasks-done", label: "tasks done", detail: "", cleanText: stripped };
    }
    if (/task_list tool available/.test(stripped)) {
      return { kind: "task-nudge", label: "task_list nudge", detail: "", cleanText: stripped };
    }
    if (/stuck in a loop|still stuck|stuck for a long time/.test(stripped)) {
      return { kind: "loop", label: "loop detection", detail: "", cleanText: stripped };
    }
    if (/reading without writing|reading for \d+ turns/.test(stripped)) {
      return { kind: "read-only", label: "read-only nudge", detail: "", cleanText: stripped };
    }
    if (/pre-compaction transcript/.test(stripped)) {
      return { kind: "transcript", label: "transcript pointer", detail: "", cleanText: stripped };
    }
    const jobNotification = parseJobNotification(stripped);
    if (jobNotification) {
      return { kind: "notification", label: jobNotification.title, detail: "", cleanText: stripped, notification: jobNotification };
    }
    const observerNotification = parseObserverCallback(stripped);
    if (observerNotification) {
      return { kind: "notification", label: observerNotification.title, detail: "", cleanText: stripped, notification: observerNotification };
    }
    return { kind: "unknown", label: "steering injected", detail: "", cleanText: stripped };
  }

  // planStateClass maps a task status to the three-state plan grammar used by
  // the inline checklist block (mockup #18 alt C): done (neutral, recedes),
  // current (the live in_progress item, blue + breathing), pending (open, dim).
  // cancelled folds into a struck/dim neutral so it recedes like done.
  function planStateClass(status) {
    switch (status) {
      case "done": return "done";
      case "in_progress": return "current";
      case "cancelled": return "cancelled";
      default: return "pending";
    }
  }

  // planGlyphForStatus returns the glyph-paired status marker for a plan item.
  // Glyph and color are dual-channel so the state reads even without color.
  function planGlyphForStatus(status) {
    switch (status) {
      case "done": return "✓";
      case "in_progress": return "⟳";
      case "cancelled": return "✕";
      default: return "○";
    }
  }

  // touchKind classifies what a task_list update did to a task, so the card can
  // flag the row by kind (color/style) instead of narrating it in prose.
  function touchKind(status) {
    switch (status) {
      case "done": return "done";
      case "in_progress": return "started";
      case "cancelled": return "cancelled";
      case "open": return "reopened";
      default: return "changed";
    }
  }

  // buildTaskRowLine builds the canonical one-line task row shared by the inline
  // task-update card and the sidebar tasks panel: the status glyph, the
  // description, and (for a completed task) the checked-off time, carrying the
  // plan-state grammar class so done recedes / current breathes / pending dims.
  // The two surfaces add their own wrappers around this shared line.
  function buildTaskRowLine(task) {
    const row = document.createElement("div");
    row.className = "task-row-line plan-item " + planStateClass(task.status);
    const glyph = document.createElement("span");
    glyph.className = "plan-glyph";
    glyph.textContent = planGlyphForStatus(task.status);
    const step = document.createElement("span");
    step.className = "plan-step";
    step.textContent = task.description || task.title || ("#" + task.id);
    row.appendChild(glyph);
    row.appendChild(step);
    if (task.status === "done") {
      const when = taskTimeOf(task.completed_at);
      if (when) {
        const time = document.createElement("span");
        time.className = "task-time";
        time.textContent = formatClockShort(when);
        row.appendChild(time);
      }
    }
    return row;
  }

  function parseToolState(s) {
    if (!s) return null;
    try { return typeof s === "string" ? JSON.parse(s) : s; } catch (e) { return null; }
  }

  function parseToolJSON(out) {
    if (!out) return null;
    try { return JSON.parse(out); } catch (e) { return null; }
  }

  function formatBytes(n) {
    const value = Number(n);
    if (!Number.isFinite(value)) return "";
    return value + " " + (value === 1 ? "byte" : "bytes");
  }

  // formatTokenCount renders a token count compactly ("23k") to match the
  // server's web_format.formatTokenCount, so the JS-side compaction expand
  // reads the same way as the server-rendered context gauge.
  function formatTokenCount(n) {
    let value = Number(n);
    if (!Number.isFinite(value) || value < 0) value = 0;
    if (value < 1000) return String(Math.round(value));
    return Math.round(value / 1000) + "k";
  }

  function compactParts(parts) {
    return parts.map(p => String(p || "").trim()).filter(Boolean).join(" · ");
  }

  function toolLooksGood(data) {
    if (data.error) return false;
    const st = parseToolState(data.tool_state);
    if (st && st.exit_code && st.exit_code !== 0) return false;
    return true;
  }

  function toolEventTime(data) {
    if (!data) return null;
    const raw = data.timestamp || data.time || data.created_at || data.createdAt || data.started_at || data.startedAt || data.completed_at || data.completedAt || data.ended_at || data.endedAt;
    if (raw == null || raw === "") return null;
    const d = typeof raw === "number" ? new Date(raw > 1e12 ? raw : raw * 1000) : new Date(raw);
    return Number.isFinite(d.getTime()) ? d : null;
  }

  function toolDuration(data) {
    if (!data) return null;
    const raw = data.durationMs || data.duration_ms || data.durationMS;
    if (raw == null || raw === "") return null;
    const n = Number(raw);
    return Number.isFinite(n) && n >= 0 ? n : null;
  }

  function formatToolClock(d) {
    if (!d || !Number.isFinite(d.getTime())) return "";
    return d.toLocaleTimeString([], { hour: "2-digit", minute: "2-digit", second: "2-digit" });
  }

  // formatClockShort renders a wall-clock time of day (no seconds) — used for a
  // task's "checked off at" stamp where minute precision is plenty.
  function formatClockShort(d) {
    if (!d || !Number.isFinite(d.getTime())) return "";
    return d.toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  }

  // taskTimeOf parses one of a task's minted timestamps (ISO string) into a Date,
  // or null when absent/unparseable.
  function taskTimeOf(raw) {
    if (raw == null || raw === "") return null;
    const d = new Date(raw);
    return Number.isFinite(d.getTime()) ? d : null;
  }

  function formatToolDuration(ms) {
    if (!Number.isFinite(ms) || ms < 0) return "";
    if (ms < 1000) return Math.max(1, Math.round(ms)) + "ms";
    const seconds = ms / 1000;
    if (seconds < 10) return seconds.toFixed(1).replace(/\.0$/, "") + "s";
    return Math.round(seconds) + "s";
  }

  function clip(s, n) {
    s = String(s || "");
    return s.length > n ? s.slice(0, n) + "…" : s;
  }

  // reasoningGist distills a collapsed thought into a short noun-phrase gist
  // (mockup #5 alt D). The model usually states its conclusion in the last
  // sentence, so we prefer the final clause; we strip leading conversational
  // filler ("so", "okay", "let me") and clip to a single quiet line.
  function reasoningGist(text, n) {
    const flat = String(text || "").replace(/\s+/g, " ").trim();
    if (!flat) return "";
    n = n || 64;
    const sentences = flat.split(/(?<=[.!?])\s+/).filter(Boolean);
    let gist = sentences.length ? sentences[sentences.length - 1] : flat;
    gist = gist.replace(/^(so|okay|ok|now|well|alright|hmm|let me|let's|i'll|i should|i need to|i think)[ ,]+/i, "");
    gist = gist.replace(/[.!?]+$/, "");
    return clip(gist.trim(), n);
  }

  // reasoningTier ranks a collapsed thought by think effort so a stack is
  // scannable by where the model spent its time (mockup #5 alt D). Even the
  // "long" tier must stay quieter than the prose — the CSS enforces that.
  function reasoningTier(secs) {
    if (secs >= 15) return "long";
    if (secs >= 5) return "med";
    return "short";
  }

  // bindDisclosureToggle wires a collapse/expand <button> to a target element's
  // ".open" class and keeps aria-expanded in sync, so assistive tech announces
  // the collapsed/expanded state (the thinking block, tool clusters, and
  // coalesced system runs all use this). The target's ".open" class is the
  // single source of truth; the button reads back from it after each toggle.
  function bindDisclosureToggle(button, target) {
    button.setAttribute("aria-expanded", target.classList.contains("open") ? "true" : "false");
    button.addEventListener("click", () => {
      target.classList.toggle("open");
      button.setAttribute("aria-expanded", target.classList.contains("open") ? "true" : "false");
    });
  }

  window.SerfRendererInternal = Object.assign(window.SerfRendererInternal || {}, {
    bindDisclosureToggle,
    itemDataToBase64,
    imagePlaceholderForCount,
    normalizedJobRefData,
    transcriptStatusPrefsKey,
    transcriptStatusDefaults,
    readTranscriptStatusPrefs,
    canonicalSystemTitle,
    systemMessagePreferenceKey,
    systemMessageDisplayTitle,
    hookExitCode,
    shouldRenderSystemMessage,
    shouldRenderSystemLine,
    partialFetch,
    sessionPartialPath,
    autoLabel,
    openImageLightbox,
    openImageLightboxSet,
    taskDescriptions,
    taskDetails,
    rememberTask,
    parseArgs,
    toolIntent,
    classifySteering,
    planStateClass,
    planGlyphForStatus,
    touchKind,
    buildTaskRowLine,
    parseToolState,
    parseToolJSON,
    formatBytes,
    formatTokenCount,
    compactParts,
    toolLooksGood,
    toolEventTime,
    toolDuration,
    formatToolClock,
    formatClockShort,
    taskTimeOf,
    formatToolDuration,
    clip,
    reasoningGist,
    reasoningTier,
  });
})();
