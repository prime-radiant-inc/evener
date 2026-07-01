(function () {
  "use strict";

  // Tool-output renderers: one descriptor per tool (read/grep/ls/glob, the
  // job_* family, shell, web_fetch/search, delegate) plus the diff/expandable
  // output helpers they share and the toolRenderers registry. toolRendererFor
  // is the public entry point; everything else is private to this module.
  // Renderer methods invoke these with the SerfRenderer instance as `this`
  // (so this.upsertJobRef etc. resolve at call time). Loads after
  // renderer-format.js, before renderer.js.

  const { parseToolState, parseToolJSON, formatBytes, compactParts, clip } =
    window.SerfRendererInternal;

  // toolRendererFor returns the renderer descriptor for a given tool name.
  // Falls back to the default renderer when no entry is registered.
  function toolRendererFor(tool) {
    return toolRenderers[tool] || toolRenderers.__default__;
  }

  // Diff preview helper, used by edit/write/apply_patch.
  function renderDiff(el, content) {
    renderDiffLines(el, splitOutputLines(content), true);
  }

  function renderDiffLines(el, lines, truncate) {
    lines = Array.isArray(lines) ? lines : [];
    const max = 1600;
    let trimmed = lines.join("\n");
    if (truncate && trimmed.length > max) trimmed = trimmed.slice(0, max) + "\n…";
    el.innerHTML = "";
    trimmed.split("\n").forEach(line => {
      const span = document.createElement("span");
      let kind = "ctx";
      if (line.startsWith("+") && !line.startsWith("+++")) kind = "add";
      else if (line.startsWith("-") && !line.startsWith("---")) kind = "del";
      else if (line.startsWith("@@")) kind = "hunk";
      span.className = kind;
      span.dataset.lineKind = kind;
      span.textContent = line;
      el.appendChild(span);
    });
  }

  // Cheap renderers — read/grep/list_dir/glob — share a common shape.
  function cheapToolBody(args, el) {
    const wrap = document.createElement("details");
    wrap.className = "tool-body cheap-tool-body";
    const summary = document.createElement("summary");
    summary.textContent = "details";
    const argsPre = document.createElement("pre");
    argsPre.className = "cheap-tool-args";
    argsPre.textContent = JSON.stringify(args || {}, null, 2);
    const outputPre = document.createElement("pre");
    outputPre.className = "cheap-tool-output";
    wrap.appendChild(summary);
    wrap.appendChild(argsPre);
    wrap.appendChild(outputPre);
    el.appendChild(wrap);
    return { wrap, argsPre, outputPre };
  }

  function cheapToolBodyDelta(state, out) {
    if (state.body && state.body.outputPre) {
      state.body.outputPre.textContent = clip(out || "", 8000);
    }
  }

  function cheapToolBodyEnd(state, data, out) {
    if (!state.body) return;
    const text = data.error || out || "";
    state.body.outputPre.textContent = clip(text, 8000);
    if (data.error) state.body.wrap.open = true;
    if (!text.trim()) state.body.outputPre.style.display = "none";
  }


  function splitOutputLines(text) {
    text = String(text || "");
    if (!text) return [];
    const lines = text.split("\n");
    if (lines.length && lines[lines.length - 1] === "") lines.pop();
    return lines;
  }

  function outputPreviewBody(className, outputClassName, el) {
    const wrap = document.createElement("div");
    wrap.className = "tool-body output-preview-body " + className;
    const pre = document.createElement("pre");
    pre.className = outputClassName + " output-preview";
    wrap.appendChild(pre);
    el.appendChild(wrap);
    return { wrap, pre };
  }

  function updateExpandableSummary(summary, details, hiddenLineCount) {
    summary.textContent = details.open
      ? "hide " + hiddenLineCount + (hiddenLineCount === 1 ? " line" : " lines")
      : "expand · " + hiddenLineCount + " more " + (hiddenLineCount === 1 ? "line" : "lines");
  }

  // looksBinary flags output that is not human-readable text (NUL bytes or a
  // high ratio of replacement chars) so we can say so plainly (mockup #6 alt D
  // DROP case) instead of rendering mojibake or faking a line count.
  function looksBinary(text) {
    const s = String(text || "");
    if (!s) return false;
    const sample = s.slice(0, 1000);
    let bad = 0;
    for (let i = 0; i < sample.length; i++) {
      const code = sample.charCodeAt(i);
      if (code === 0 || code === 0xFFFD) bad++; // NUL or U+FFFD replacement char
    }
    return sample.length > 0 && bad / sample.length > 0.1;
  }

  // dropNote renders an honest "bytes were dropped at the source" line — the
  // server-dropped (DROP) state from mockup #6 alt D. Unlike the
  // client-collapsed "expand · N more" affordance (which reveals bytes that
  // ARE present), this admits the data is gone and is NOT a fake expand.
  function dropNote(label) {
    const note = document.createElement("div");
    note.className = "tool-output-dropped";
    note.textContent = "⚠ " + label;
    return note;
  }

  // setExpandableOutput shows the first 5 lines whole and folds the rest behind
  // a client-collapsed "expand · N more" affordance (honest: those bytes rode
  // into context and are present). When opts.dropped is set the output was
  // truncated/binary at the source, so instead of (or alongside) the present
  // tail we append an honest drop note rather than pretending an expand will
  // reveal the missing bytes.
  function setExpandableOutput(body, text, opts) {
    if (!body || !body.pre || !body.wrap) return;
    opts = opts || {};
    if (body.dropEl) { body.dropEl.remove(); body.dropEl = null; }
    if (opts.binary || looksBinary(text)) {
      if (body.moreWrap) { body.moreWrap.remove(); body.moreWrap = null; }
      body.pre.style.display = "none";
      body.dropEl = dropNote(opts.droppedLabel || "binary output — not shown as text");
      body.wrap.appendChild(body.dropEl);
      return;
    }
    const lines = splitOutputLines(text);
    body.pre.textContent = lines.slice(0, 5).join("\n");
    body.pre.style.display = lines.length ? "" : "none";
    if (body.moreWrap) {
      body.moreWrap.remove();
      body.moreWrap = null;
    }
    if (lines.length > 5) {
      const moreWrap = document.createElement("details");
      moreWrap.className = "tool-output-more " + (opts.moreClass || "");
      const morePre = document.createElement("pre");
      morePre.className = (opts.outputClassName || body.pre.className || "") + " tool-output-rest";
      morePre.textContent = lines.slice(5).join("\n");
      const summary = document.createElement("summary");
      const hiddenLineCount = lines.length - 5;
      moreWrap.addEventListener("toggle", () => updateExpandableSummary(summary, moreWrap, hiddenLineCount));
      updateExpandableSummary(summary, moreWrap, hiddenLineCount);
      moreWrap.appendChild(morePre);
      moreWrap.appendChild(summary);
      body.wrap.appendChild(moreWrap);
      body.moreWrap = moreWrap;
    }
    if (opts.dropped) {
      body.dropEl = dropNote(opts.droppedLabel || "output truncated at the source — the dropped bytes were never captured");
      body.wrap.appendChild(body.dropEl);
    }
  }

  function setExpandableDiff(body, text, opts) {
    if (!body || !body.pre || !body.wrap) return;
    opts = opts || {};
    const lines = splitOutputLines(text);
    renderDiffLines(body.pre, lines.slice(0, 5), false);
    body.pre.style.display = lines.length ? "" : "none";
    if (body.moreWrap) {
      body.moreWrap.remove();
      body.moreWrap = null;
    }
    if (lines.length <= 5) return;
    const moreWrap = document.createElement("details");
    moreWrap.className = "tool-output-more " + (opts.moreClass || "");
    const morePre = document.createElement("pre");
    morePre.className = (opts.outputClassName || body.pre.className || "diff-body") + " tool-output-rest";
    renderDiffLines(morePre, lines.slice(5), false);
    const summary = document.createElement("summary");
    const hiddenLineCount = lines.length - 5;
    moreWrap.addEventListener("toggle", () => updateExpandableSummary(summary, moreWrap, hiddenLineCount));
    updateExpandableSummary(summary, moreWrap, hiddenLineCount);
    moreWrap.appendChild(morePre);
    moreWrap.appendChild(summary);
    body.wrap.appendChild(moreWrap);
    body.moreWrap = moreWrap;
  }

  function readLineRange(args, out) {
    args = args || {};
    const start = Number.isFinite(Number(args.offset)) && Number(args.offset) > 0 ? Number(args.offset) : 1;
    let count = Number(args.limit);
    if (!Number.isFinite(count) || count <= 0) count = (String(out || "").match(/\n/g) || []).length;
    if (!Number.isFinite(count) || count <= 0) return "lines " + start;
    return "lines " + start + "-" + (start + count - 1);
  }

  function readToolBody(args, el) {
    const wrap = document.createElement("div");
    wrap.className = "tool-body cheap-tool-body read-tool-body";
    const outputPre = document.createElement("pre");
    outputPre.className = "cheap-tool-output read-tool-preview";
    wrap.appendChild(outputPre);
    el.appendChild(wrap);
    return { wrap, outputPre };
  }

  function setReadOutput(state, text) {
    if (!state.body || !state.body.outputPre) return;
    setExpandableOutput({ wrap: state.body.wrap, pre: state.body.outputPre, moreWrap: state.body.moreWrap }, text, { moreClass: "read-tool-more", outputClassName: "cheap-tool-output read-tool-rest" });
    state.body.moreWrap = state.body.wrap.querySelector(":scope > .read-tool-more");
  }

  function readToolBodyDelta(state, out) {
    setReadOutput(state, clip(out || "", 8000));
  }

  function readToolBodyEnd(state, data, out) {
    setReadOutput(state, clip(data.error || out || "", 8000));
  }

  function grepTarget(a) {
    const base = '"' + clip(a.pattern || "", 50) + '" in ' + (a.path || ".");
    return a.glob_filter ? base + " (" + a.glob_filter + ")" : base;
  }

  function lsTarget(a) {
    const base = a.path || ".";
    return a.pattern ? base + " (" + a.pattern + ")" : base;
  }

  const readRenderer = {
    mode: "cheap", friendly: "read",
    target: (a) => a.file_path || a.path || "",
    result: (data, out, state) => readLineRange(state && state.args, out),
    body: readToolBody,
    bodyDelta: readToolBodyDelta,
    bodyEnd: readToolBodyEnd,
  };
  const grepRenderer = {
    mode: "cheap", friendly: "grep",
    target: grepTarget,
    result: (data, out) => ((out.match(/\n/g) || []).length) + " hits",
    body: cheapToolBody,
    bodyDelta: cheapToolBodyDelta,
    bodyEnd: cheapToolBodyEnd,
  };
  const lsRenderer = {
    mode: "cheap", friendly: "ls",
    target: lsTarget,
    result: (data, out) => ((out.match(/\n/g) || []).length) + " entries",
    body: cheapToolBody,
    bodyDelta: cheapToolBodyDelta,
    bodyEnd: cheapToolBodyEnd,
  };
  const globRenderer = {
    mode: "cheap", friendly: "find",
    target: (a) => a.pattern || a.glob || "",
    result: (data, out) => ((out.match(/\n/g) || []).length) + " matches",
    body: cheapToolBody,
    bodyDelta: cheapToolBodyDelta,
    bodyEnd: cheapToolBodyEnd,
  };

  function jobReadOutputText(st, out) {
    if (st && Array.isArray(st.matches)) return st.matches.map(m => m && m.line || "").filter(Boolean).join("\n");
    if (st && typeof st.output === "string") return st.output;
    if (st && st.structured_result !== undefined) return JSON.stringify(st.structured_result, null, 2);
    return out || "";
  }

  function jobListBodyText(st, out) {
    if (!st) return out || "";
    const lines = [];
    if (Array.isArray(st.jobs)) lines.push(...st.jobs.map(jobListJobLine).filter(Boolean));
    if (Array.isArray(st.delegates)) lines.push(...st.delegates.map(jobListDelegateLine).filter(Boolean));
    if (Array.isArray(st.watches)) lines.push(...st.watches.map(jobListWatchLine).filter(Boolean));
    if (Array.isArray(st.recent_watches)) lines.push(...st.recent_watches.map(jobListRecentWatchLine).filter(Boolean));
    return lines.length ? lines.join("\n") : (out || "");
  }

  function jobListJobLine(job) {
    if (!job || !job.job_id) return "";
    const label = job.description || job.command || "";
    const detail = compactParts([
      job.delegate_id ? "delegate_id " + job.delegate_id : "",
      formatBytes(job.total_bytes),
    ]);
    const base = compactParts([job.job_id, job.type, job.status, label]);
    return detail ? base + " [" + detail + "]" : base;
  }

  function jobListDelegateLine(delegate) {
    if (!delegate || !delegate.delegate_id) return "";
    const detail = compactParts([
      delegate.current_job_id ? "current_job_id " + delegate.current_job_id : "",
      delegate.latest_job_id && delegate.latest_job_id !== delegate.current_job_id ? "latest_job_id " + delegate.latest_job_id : "",
      delegate.transcript_ref ? "transcript_ref " + delegate.transcript_ref : "",
      delegate.resumable ? "resumable" : delegate.not_resumable_reason,
      delegate.parent_delegate_id ? "parent_delegate_id " + delegate.parent_delegate_id : "",
    ]);
    const base = compactParts(["delegate " + delegate.delegate_id, delegate.status]);
    return detail ? base + " [" + detail + "]" : base;
  }

  function jobListWatchLine(watch) {
    if (!watch || !watch.id) return "";
    return compactParts([
      "watch " + watch.id,
      watch.target ? "-> " + watch.target : "",
      watch.condition ? "(" + watch.condition + ")" : "",
      watch.send_to ? "send_to " + watch.send_to : "",
      typeof watch.deliveries === "number" ? watch.deliveries + " delivered" : "",
    ]);
  }

  function jobListRecentWatchLine(watch) {
    if (!watch || !watch.id) return "";
    return compactParts([
      "recent watch " + watch.id,
      watch.target ? "-> " + watch.target : "",
      watch.condition ? "(" + watch.condition + ")" : "",
      watch.end_reason,
      typeof watch.deliveries === "number" ? watch.deliveries + " delivered" : "",
    ]);
  }

  const jobReadOutputRenderer = {
    mode: "card", friendly: "job output",
    target: (a) => a.job_id || "",
    result: (data, out) => {
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      if (!st) return out ? formatBytes(out.length) : "";
      return compactParts([
        st.status,
        formatBytes(st.total_bytes),
        st.truncated ? "truncated" : "",
        Array.isArray(st.matches) ? st.matches.length + " matches" : "",
      ]);
    },
    body: (args, conversation) => outputPreviewBody("job-output-body", "job-output", conversation),
    bodyEnd: (state, data, out) => {
      if (!state.body) return;
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      const text = data.error || jobReadOutputText(st, out);
      // Server-dropped (mockup #6 alt D DROP): the daemon truncated the output,
      // so the tail shown here is NOT all of it. Say so honestly instead of
      // implying an "expand" would reveal the rest.
      const opts = { moreClass: "job-output-more", outputClassName: "job-output" };
      if (st && st.truncated) {
        opts.dropped = true;
        opts.droppedLabel = "truncated at the source — kept " + clip(formatBytes(st.total_bytes) || "part", 40) + ", the rest was never captured";
      }
      setExpandableOutput(state.body, clip(text, 8000), opts);
      if (!String(text || "").trim()) state.body.wrap.style.display = "none";
    },
    // Reading a subagent's output is a completion signal even when no
    // JOB_FINISHED arrives: flip the matching row to the job's reported status
    // and surface a short result preview from its output.
    subagentReconcile: (state, data, out) => {
      if (data.error) return [];
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      if (!st || !st.job_id) return [];
      const content = String(jobReadOutputText(st, out) || "").replace(/\s+/g, " ").trim();
      return [{
        job_id: st.job_id,
        type: st.type,
        status: st.status || "",
        transcript_ref: st.transcript_ref,
        outputBytes: st.total_bytes,
        resultText: content ? clip(content, 120) : "",
      }];
    },
  };

  const jobSendMessageRenderer = {
    mode: "card", friendly: "message",
    target: (a) => clip(a.to || a.target || "", 26),
    result: (data, out) => {
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      if (!st) return out ? formatBytes(out.length) : "";
      return compactParts([
        st.action,
        st.status,
        st.job_id || st.started_job_id || st.current_job_id || st.latest_job_id,
        st.delivered === true ? "delivered" : "",
        st.reason,
      ]);
    },
    body: (args, conversation) => outputPreviewBody("job-message-body", "job-message-output", conversation),
    bodyEnd: (state, data, out) => {
      if (!state.body) return;
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      let text = "";
      if (data.error) text = data.error;
      else if (st && typeof st.output === "string") text = st.output;
      else if (st && st.structured_result !== undefined) text = JSON.stringify(st.structured_result, null, 2);
      setExpandableOutput(state.body, clip(text, 8000), { moreClass: "job-message-output-more", outputClassName: "job-message-output" });
      if (!String(text || "").trim()) state.body.wrap.style.display = "none";
    },
    // Messaging a subagent reports its current status (e.g. "completed" after a
    // resume), which reconciles a stale-running row.
    subagentReconcile: (state, data, out) => {
      if (data.error) return [];
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      const jobID = st && (st.job_id || st.current_job_id || st.latest_job_id || st.started_job_id);
      if (!jobID) return [];
      const reply = typeof st.output === "string" ? st.output.replace(/\s+/g, " ").trim() : "";
      return [{
        job_id: jobID,
        status: st.status || "",
        transcript_ref: st.transcript_ref,
        resultText: reply ? clip(reply, 120) : "",
      }];
    },
  };

  const jobListRenderer = {
    mode: "card", friendly: "jobs",
    target: (a) => Array.isArray(a.status) ? a.status.join(",") : (a.status || ""),
    result: (data, out) => {
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      if (!st) return out ? formatBytes(out.length) : "";
      const count = typeof st.count === "number" ? st.count : (Array.isArray(st.jobs) ? st.jobs.length : 0);
      return count + " " + (count === 1 ? "job" : "jobs");
    },
    body: (args, conversation) => outputPreviewBody("job-list-body", "job-list-output", conversation),
    bodyEnd: (state, data, out) => {
      if (!state.body) return;
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      const text = data.error || jobListBodyText(st, out);
      setExpandableOutput(state.body, clip(text, 8000), { moreClass: "job-list-output-more", outputClassName: "job-list-output" });
      if (!String(text || "").trim()) state.body.wrap.style.display = "none";
    },
    // Listing jobs reports each job's current status, reconciling several
    // stale-running subagent rows at once.
    subagentReconcile: (state, data, out) => {
      if (data.error) return [];
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      if (!st || !Array.isArray(st.jobs)) return [];
      return st.jobs
        .filter(job => job && job.job_id)
        .map(job => ({
          job_id: job.job_id,
          type: job.type,
          status: job.status || "",
          transcript_ref: job.transcript_ref,
          outputBytes: job.total_bytes,
        }));
    },
  };

  const jobStopRenderer = {
    mode: "cheap", friendly: "stop",
    target: (a) => clip(a.job_id || "", 26),
    result: (data, out) => {
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      if (!st) return out ? formatBytes(out.length) : "";
      return compactParts([st.status, st.reason]);
    },
  };

  function shellCommandText(args) {
    args = args || {};
    return String(args.command || args.cmd || "");
  }

  function shellTerminalBody(args, el) {
    const wrap = document.createElement("div");
    wrap.className = "tool-body shell-body tool-body--terminal";

    const commandEl = document.createElement("div");
    commandEl.className = "terminal-command";
    commandEl.textContent = "$ " + shellCommandText(args);
    wrap.appendChild(commandEl);

    const pre = document.createElement("pre");
    pre.className = "shell-output terminal-output";
    wrap.appendChild(pre);

    const footerEl = document.createElement("div");
    footerEl.className = "terminal-footer";
    footerEl.textContent = "running";
    wrap.appendChild(footerEl);

    el.appendChild(wrap);
    return { wrap, commandEl, pre, footerEl };
  }

  function shellFooterText(data, state) {
    const st = parseToolState(data && data.tool_state);
    const parts = [];
    if (st && st.exit_code != null) parts.push("exit " + st.exit_code);
    else if (data && data.error) parts.push("error");
    if (state && state.durationMs != null) parts.push(formatDurationForTerminal(state.durationMs));
    return parts.join(" · ");
  }

  function formatDurationForTerminal(ms) {
    const n = Number(ms);
    if (!Number.isFinite(n) || n < 0) return "";
    if (n < 1000) return Math.round(n) + "ms";
    if (n < 10000) return (n / 1000).toFixed(1).replace(/\.0$/, "") + "s";
    return Math.round(n / 1000) + "s";
  }

  // Card renderer for shell with collapsible stdout/stderr.
  const shellRenderer = {
    mode: "card", friendly: "$",
    target: (a) => clip(shellCommandText(a), 200),
    result: (data) => {
      const st = parseToolState(data.tool_state);
      if (st && st.exit_code != null) return st.exit_code === 0 ? "" : "exit " + st.exit_code;
      return data.error ? "error" : "";
    },
    body: (args, el) => shellTerminalBody(args, el),
    bodyDelta: (state, out) => {
      if (state.body && state.body.pre) {
        setExpandableOutput(state.body, clip(out, 8000), { moreClass: "shell-output-more", outputClassName: "shell-output terminal-output" });
      }
    },
    bodyEnd: (state, data, out) => {
      if (!state.body) return;
      const text = data.error || out || "";
      setExpandableOutput(state.body, clip(text, 8000), { moreClass: "shell-output-more", outputClassName: "shell-output terminal-output" });
      const st = parseToolState(data.tool_state);
      const failed = data.error || (st && st.exit_code && st.exit_code !== 0);
      if (state.body.footerEl) {
        state.body.footerEl.textContent = shellFooterText(data, state) || "done";
        state.body.footerEl.classList.toggle("terminal-footer-bad", !!failed);
      }
      if (failed && state.el) state.el.dataset.expanded = "true";
      if (failed && state.caretEl) {
        state.caretEl.textContent = "▾";
        state.caretEl.setAttribute("aria-label", "collapse tool details");
        state.caretEl.setAttribute("aria-expanded", "true");
      }
    },
  };

  // Diff renderers for edit/write/apply_patch. diffResult is the collapsed
  // headline (mockup #19 alt A): "+N −N", the change's shape at a glance.
  // Count whole diff lines so the +++/--- file headers never inflate the stat.
  function diffResult(data, out) {
    const lines = String(out || "").split("\n");
    const adds = lines.filter(l => l.startsWith("+") && !l.startsWith("+++")).length;
    const dels = lines.filter(l => l.startsWith("-") && !l.startsWith("---")).length;
    if (adds === 0 && dels === 0) return "ok";
    return "+" + adds + " -" + dels;
  }

  // Diffs collapse by default (mockup #19 alt A): the row shows the +N −N stat
  // and the full unified diff is one caret-click away. expand:false drives the
  // shared tool-call collapse/expand machinery in renderer.js.
  function diffRenderer(friendly) {
    return {
      mode: "card", friendly, expand: false, mutating: true,
      target: (a) => a.file_path || a.path || "",
      result: diffResult,
      body: (args, conversation) => {
        const pre = document.createElement("pre");
        pre.className = "diff-body";
        conversation.appendChild(pre);
        return { pre };
      },
      bodyDelta: (state, out) => { if (state.body) renderDiff(state.body.pre, out); },
      bodyEnd: (state, data, out) => { if (state.body) renderDiff(state.body.pre, out); },
    };
  }

  function editDiffText(args, out) {
    args = args || {};
    const oldText = typeof args.old_string === "string" ? args.old_string : "";
    const newText = typeof args.new_string === "string" ? args.new_string : "";
    if (oldText || newText) {
      const lines = [];
      const path = args.file_path || args.path || "";
      if (path) lines.push("--- " + path, "+++ " + path);
      for (const line of splitOutputLines(oldText)) lines.push("-" + line);
      for (const line of splitOutputLines(newText)) lines.push("+" + line);
      return lines.join("\n");
    }
    return out || "";
  }

  function editRenderer() {
    return {
      mode: "card", friendly: "edit", expand: false, mutating: true,
      target: (a) => a.file_path || a.path || "",
      result: (data, out, state) => diffResult(data, editDiffText(state && state.args, out)),
      body: (args, conversation) => {
        const wrap = document.createElement("div");
        wrap.className = "tool-body edit-body";
        const pre = document.createElement("pre");
        pre.className = "diff-body";
        wrap.appendChild(pre);
        conversation.appendChild(wrap);
        return { wrap, pre };
      },
      bodyDelta: (state, out) => { if (state.body) renderDiff(state.body.pre, editDiffText(state.args, out)); },
      bodyEnd: (state, data, out) => { if (state.body) renderDiff(state.body.pre, editDiffText(state.args, out)); },
    };
  }

  function patchRenderer() {
    return {
      mode: "card", friendly: "patch", expand: false, mutating: true,
      target: (a) => patchTargets(a.patch).join(", "),
      result: (data, out, state) => diffResult(data, state && state.args && state.args.patch || out || ""),
      body: (args, conversation) => {
        const wrap = document.createElement("div");
        wrap.className = "tool-body output-preview-body patch-body";
        const pre = document.createElement("pre");
        pre.className = "diff-body patch-preview";
        wrap.appendChild(pre);
        conversation.appendChild(wrap);
        const body = { wrap, pre };
        setExpandableDiff(body, (args && args.patch) || "", { moreClass: "patch-output-more", outputClassName: "diff-body patch-rest" });
        return body;
      },
      bodyDelta: (state) => {
        if (state.body) setExpandableDiff(state.body, (state.args && state.args.patch) || "", { moreClass: "patch-output-more", outputClassName: "diff-body patch-rest" });
      },
      bodyEnd: (state) => {
        if (state.body) setExpandableDiff(state.body, (state.args && state.args.patch) || "", { moreClass: "patch-output-more", outputClassName: "diff-body patch-rest" });
      },
    };
  }

  function patchTargets(patch) {
    const targets = [];
    const seen = new Set();
    for (const line of splitOutputLines(patch)) {
      const m = line.match(/^\*\*\* (?:Add|Update|Delete) File: (.+)$/);
      if (m && !seen.has(m[1])) {
        seen.add(m[1]);
        targets.push(m[1]);
      }
    }
    return targets;
  }

  // task_list is intentionally not in the tool-renderer registry — it's
  // intercepted in handle() (TOOL_CALL_START/END) and rendered as inline
  // system-line prose via appendTaskListSystemLine.

  // web_fetch renderer.
  const webFetchRenderer = {
    mode: "card", friendly: "fetch",
    target: (a) => a.url || "",
    result: (data, out) => data.error ? "error" : (out.length + " bytes"),
    body: (args, conversation) => {
      const div = document.createElement("div");
      div.className = "tool-body fetch-body";
      conversation.appendChild(div);
      return { div };
    },
    bodyEnd: (state, data, out) => {
      if (!state.body) return;
      state.body.div.textContent = clip((out || "").trim().split("\n").slice(0, 3).join(" / "), 240);
    },
  };

  // web_search renderer.
  const webSearchRenderer = {
    mode: "card", friendly: "search",
    target: (a) => clip(a.query || a.q || "", 120),
    result: (data, out) => {
      const lines = out.split("\n").filter(l => l.trim()).length;
      return lines + " results";
    },
    body: (args, conversation) => {
      const ul = document.createElement("ul");
      ul.className = "tool-body search-body";
      conversation.appendChild(ul);
      return { ul };
    },
    bodyEnd: (state, data, out) => {
      if (!state.body) return;
      const lines = out.split("\n").map(l => l.trim()).filter(Boolean).slice(0, 5);
      state.body.ul.innerHTML = "";
      for (const l of lines) {
        const li = document.createElement("li");
        li.textContent = clip(l, 200);
        state.body.ul.appendChild(li);
      }
    },
  };

  const delegateRenderer = {
    mode: "default", friendly: "delegate",
    target: (a) => clip(a.task || "", 80),
    result: (data, out) => {
      const st = parseToolJSON(out) || parseToolState(data.tool_state);
      if (st && st.status) return st.status;
      return "done";
    },
    // delegate drops its own tool card; the spawned subagent shows up as a row
    // in the aggregated "Subagents (N)" module instead.
    subagentSpawn(state, data) {
      const st = parseToolJSON(data.output || state.outputBuf || "") || parseToolState(data.tool_state);
      if (!st || !st.job_id) return null;
      return {
        jobId: st.job_id,
        jobType: st.type || "delegate",
        status: st.status || "running",
        transcriptRef: st.transcript_ref || "",
        label: st.task || (state.args && state.args.task) || "",
      };
    },
  };

  const defaultRenderer = {
    mode: "default",
    friendly: undefined, // fallback to tool name
    target: (a) => Object.values(a || {}).map(v => typeof v === "string" ? v : "").filter(Boolean).slice(0, 2).join(" "),
    result: (data) => data.error ? "error" : "ok",
  };

  const useSkillRenderer = Object.assign({}, defaultRenderer, {
    target: (a) => a.skill_name || a.name || "",
    body: (args, conversation) => {
      const div = document.createElement("div");
      div.className = "tool-body use-skill-body";
      div.style.display = "none";
      conversation.appendChild(div);
      return { div };
    },
    bodyEnd: (state, data) => {
      if (!state.body || !state.body.div) return;
      const st = parseToolState(data.tool_state);
      const activation = st && (st.skill_activation || st.skillActivation);
      const text = activation && (activation.text || (activation.name && ("Activated skill: " + activation.name)));
      if (!text) {
        state.body.div.textContent = "";
        state.body.div.style.display = "none";
        return;
      }
      state.body.div.style.display = "";
      state.body.div.textContent = text;
    },
  });

  const toolRenderers = {
    __default__: defaultRenderer,
    "use_skill": useSkillRenderer,
    "read_file": readRenderer,
    "grep_files": grepRenderer,
    "grep": grepRenderer,
    "grep_search": grepRenderer,
    "list_dir": lsRenderer,
    "list_directory": lsRenderer,
    "glob": globRenderer,
    "shell": shellRenderer,
    "exec_command": shellRenderer,
    "run_shell_command": shellRenderer,
    "edit_file": editRenderer(),
    "write_file": diffRenderer("write"),
    "apply_patch": patchRenderer(),
    "web_fetch": webFetchRenderer,
    "web_search": webSearchRenderer,
    "delegate": delegateRenderer,
    "delegate_send": jobSendMessageRenderer,
    "job_send_message": jobSendMessageRenderer,
    "job_read_output": jobReadOutputRenderer,
    "job_list": jobListRenderer,
    "job_stop": jobStopRenderer,
  };

  window.SerfRendererInternal = Object.assign(window.SerfRendererInternal || {}, {
    toolRendererFor,
  });
})();
