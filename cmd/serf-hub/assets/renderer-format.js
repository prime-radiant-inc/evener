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

  // openImageLightbox shows a full-size image overlay. Click backdrop or press
  // Esc to dismiss. One overlay at a time.
  function openImageLightbox(src, name) {
    const existing = document.getElementById("image-lightbox");
    if (existing) existing.remove();
    const overlay = document.createElement("div");
    overlay.id = "image-lightbox";
    overlay.className = "image-lightbox";
    const img = document.createElement("img");
    img.src = src;
    if (name) img.alt = name;
    overlay.appendChild(img);
    if (name) {
      const cap = document.createElement("div");
      cap.className = "image-lightbox-caption";
      cap.textContent = name;
      overlay.appendChild(cap);
    }
    const close = () => {
      overlay.remove();
      document.removeEventListener("keydown", onKey);
    };
    const onKey = (e) => { if (e.key === "Escape") close(); };
    overlay.addEventListener("click", close);
    document.addEventListener("keydown", onKey);
    document.body.appendChild(overlay);
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
  function taskDesc(id) {
    const d = taskDescriptions.get(id);
    return d ? '"' + d + '"' : "#" + id;
  }
  function taskDetailFor(id) {
    const n = Number(id);
    return Number.isFinite(n) ? taskDetails.get(n) : null;
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
    return { kind: "unknown", label: "steering injected", detail: "", cleanText: stripped };
  }

  function appendTaskIcon(line, kind) {
    const icon = document.createElement("span");
    icon.className = "task-system-icon task-system-icon-" + (kind || "open");
    icon.textContent = kind === "done" ? "✓" : "□";
    line.appendChild(icon);
  }

  function taskDetailRows(t) {
    if (!t) return [];
    const rows = [];
    if (t.type) rows.push(["type", t.type]);
    if (t.status) rows.push(["status", t.status]);
    if (Array.isArray(t.depends_on) && t.depends_on.length) rows.push(["depends on", t.depends_on.map(x => "#" + x).join(", ")]);
    if (t.reasoning_effort) rows.push(["reasoning", t.reasoning_effort]);
    if (t.prompt) rows.push(["prompt", t.prompt]);
    if (t.notes) rows.push(["notes", Array.isArray(t.notes) ? t.notes.join("\n") : t.notes]);
    return rows;
  }

  function appendTaskDetailDisclosure(parent, task) {
    const rows = taskDetailRows(task);
    if (!rows.length) return;
    const details = document.createElement("details");
    details.className = "task-system-details";
    const summary = document.createElement("summary");
    summary.textContent = "task details";
    details.appendChild(summary);
    const dl = document.createElement("dl");
    dl.className = "task-system-detail";
    for (const row of rows) {
      const dt = document.createElement("dt");
      dt.textContent = row[0];
      const dd = document.createElement("dd");
      dd.textContent = row[1];
      dl.appendChild(dt);
      dl.appendChild(dd);
    }
    details.appendChild(dl);
    parent.appendChild(details);
  }

  function appendTaskListDetails(parent, args) {
    if (!args) return;
    const tasks = [];
    if (args.action === "append" && Array.isArray(args.tasks)) {
      for (const t of args.tasks) { rememberTask(t); if (t) tasks.push(t); }
    } else if (args.action === "update" && Array.isArray(args.updates)) {
      for (const u of args.updates) {
        const cached = taskDetailFor(u && u.id) || {};
        const merged = Object.assign({}, cached, u || {});
        if (u && u.id != null) rememberTask(merged);
        tasks.push(merged);
      }
    }
    const useful = tasks.filter(t => taskDetailRows(t).length);
    if (!useful.length) return;
    const details = document.createElement("details");
    details.className = "task-system-details";
    const summary = document.createElement("summary");
    summary.textContent = useful.length === 1 ? "task details" : useful.length + " task details";
    details.appendChild(summary);
    for (const t of useful) {
      const section = document.createElement("div");
      section.className = "task-system-detail-item";
      const title = document.createElement("div");
      title.className = "task-system-detail-title";
      title.textContent = "#" + (t.id || "?") + (t.description ? ": " + t.description : "");
      section.appendChild(title);
      const dl = document.createElement("dl");
      dl.className = "task-system-detail";
      for (const row of taskDetailRows(t)) {
        const dt = document.createElement("dt");
        dt.textContent = row[0];
        const dd = document.createElement("dd");
        dd.textContent = row[1];
        dl.appendChild(dt);
        dl.appendChild(dd);
      }
      section.appendChild(dl);
      details.appendChild(section);
    }
    parent.appendChild(details);
  }

  function taskListIconKind(args) {
    if (!args) return "open";
    if (args.action === "append") return "open";
    if (args.action === "update" && Array.isArray(args.updates)) {
      if (args.updates.some(u => u && u.status === "done")) return "done";
      if (args.updates.some(u => u && u.status === "in_progress")) return "in_progress";
    }
    return "open";
  }

  // formatTaskListAction renders task_list tool args as a single line of
  // English prose. Returns null/empty for no-op cases (view, empty updates).
  // Multiple updates in one call collapse into a comma-joined sentence
  // capped at 4 clauses to prevent runaway lines.
  function formatTaskListAction(args) {
    if (!args || !args.action) return "";
    if (args.action === "view") return "";

    if (args.action === "append") {
      if (!Array.isArray(args.tasks) || args.tasks.length === 0) return "";
      // Show the FULL plan on creation — the one moment the entire list
      // matters to the reader. No "+N more" truncation.
      const descs = args.tasks.map(t => '"' + (t.description || "?") + '"');
      return "added " + descs.length + " task" + (descs.length === 1 ? "" : "s") + ": " + descs.join(" · ");
    }

    if (args.action === "update") {
      if (!Array.isArray(args.updates) || args.updates.length === 0) return "";
      // Group same-status updates together so 3 dones in one call read as
      //   marked "A", "B", "C" done
      // instead of
      //   marked "A" done, marked "B" done, marked "C" done.
      // Notes are emitted only when there's exactly one update with notes
      // for that status — otherwise the prose gets unwieldy.
      const byStatus = new Map(); // status -> [{id, notes}, …]
      const order = [];
      for (const u of args.updates) {
        const s = u.status || "?";
        if (!byStatus.has(s)) { byStatus.set(s, []); order.push(s); }
        byStatus.get(s).push(u);
      }
      const clauses = order.map(s => formatStatusClause(s, byStatus.get(s)));
      return clauses.filter(Boolean).join(", ");
    }
    return "";
  }

  // formatStatusClause renders one verb's worth of updates: "marked A done",
  // "marked A, B, C done", "started X", etc. Notes attach when exactly one
  // task got that status this turn.
  function formatStatusClause(status, items) {
    if (!items || items.length === 0) return "";
    const subjects = items.map(u => taskDesc(u.id)).join(", ");
    let clause;
    switch (status) {
      case "done":        clause = "marked " + subjects + " done"; break;
      case "in_progress": clause = "started " + subjects; break;
      case "cancelled":   clause = "cancelled " + subjects; break;
      case "open":        clause = "reopened " + subjects; break;
      default:            clause = "updated " + subjects; break;
    }
    if (items.length === 1 && items[0].notes) {
      const note = clip(items[0].notes, 100).replace(/[.!?]+$/, "");
      clause += " (" + note + ")";
    }
    return clause;
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

  window.SerfRendererInternal = Object.assign(window.SerfRendererInternal || {}, {
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
    taskDescriptions,
    taskDetails,
    rememberTask,
    taskDesc,
    taskDetailFor,
    parseArgs,
    toolIntent,
    classifySteering,
    appendTaskIcon,
    taskDetailRows,
    appendTaskDetailDisclosure,
    appendTaskListDetails,
    taskListIconKind,
    formatTaskListAction,
    formatStatusClause,
    parseToolState,
    parseToolJSON,
    formatBytes,
    compactParts,
    toolLooksGood,
    toolEventTime,
    toolDuration,
    formatToolClock,
    formatToolDuration,
    clip,
  });
})();
