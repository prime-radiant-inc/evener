(function () {
  "use strict";

  const SerfRenderer = {
    init(conversationEl) {
      if (!conversationEl) return;
      // Idempotent: if we've already initialized this exact element for this
      // session, don't double-connect. Switching sessions = different element
      // node (htmx swapped innerHTML), so the marker won't be there.
      if (conversationEl.__serfInitialized) return;
      conversationEl.__serfInitialized = true;

      // Close any previous EventSource so switching sessions doesn't leak
      // connections or replay duplicate events.
      if (this.eventSource) {
        try { this.eventSource.close(); } catch (e) {}
        this.eventSource = null;
      }

      this.conversation = conversationEl;
      this.sessionId = conversationEl.dataset.sessionId;
      this.replayUrl = conversationEl.dataset.replayUrl || "";
      this.eventsUrl = conversationEl.dataset.eventsUrl || "";
      this.state = conversationEl.dataset.state || "ended";

      this.activeMessages = new Map();   // messageId -> {el, textBuf, markdownTimer}
      this.activeTools = new Map();      // callId -> {el, outputBuf}
      this.activeSubagents = new Map();  // agent_id -> subagent reference el
      this.suppressedToolCalls = new Set();
      this.currentMessageId = null;
      this.userTurnIndex = 0;            // counts user turns rendered (for fork divergence)
      this.entryIndex = 0;               // counts ALL entries rendered (matches transcript entry index)
      this.cheapToolCluster = null;      // current cluster div for batching cheap reads

      this.conversation.innerHTML = "";

      const url = this.replayUrl || this.eventsUrl;
      if (url) this.connect(url);

      this.bindInputForm();
      this.bindKeyboard();
    },

    connect(url) {
      this.eventSource = new EventSource(url);
      const kinds = [
        "SESSION_START", "SESSION_END", "USER_INPUT",
        "ASSISTANT_TEXT_START", "ASSISTANT_TEXT_DELTA", "ASSISTANT_TEXT_END",
        "TOOL_CALL_START", "TOOL_CALL_OUTPUT_DELTA", "TOOL_CALL_END",
        "STEERING_INJECTED", "WARNING", "ERROR",
        "SUBAGENT_START", "SUBAGENT_END", "COMMUNICATE",
      ];
      kinds.forEach((k) => this.eventSource.addEventListener(k, (ev) => this.handle(k, ev)));
      // Replay endpoints emit REPLAY_DONE after the last entry; close the
      // EventSource so the browser doesn't auto-reconnect and replay again.
      this.eventSource.addEventListener("REPLAY_DONE", () => {
        if (this.eventSource) { this.eventSource.close(); this.eventSource = null; }
      });
      this.eventSource.onerror = () => { /* browser auto-reconnects for live streams */ };
    },

    handle(kind, ev) {
      let data = {};
      try { data = JSON.parse(ev.data); } catch (e) {}
      switch (kind) {
        case "SESSION_START":
          if (data.session_id && data.session_id !== this.sessionId) {
            this.sessionId = data.session_id;
            history.replaceState(null, "", "/s/" + encodeURIComponent(data.session_id));
            this.conversation.innerHTML = "";
            this.activeMessages.clear();
            this.activeTools.clear();
            this.activeSubagents.clear();
            this.userTurnIndex = 0;
            this.entryIndex = 0;
          }
          break;
        case "USER_INPUT":
          this.userTurnIndex++;
          this.entryIndex++;
          this.appendUserMessage(data.text || "", this.entryIndex);
          break;
        case "ASSISTANT_TEXT_START":
          this.entryIndex++;
          this.beginAssistantMessage();
          break;
        case "ASSISTANT_TEXT_DELTA":
          this.appendAssistantDelta(data.delta || "");
          break;
        case "ASSISTANT_TEXT_END":
          this.finalizeAssistantMessage(data);
          break;
        case "TOOL_CALL_START":
          if (data.tool_name === "communicate") {
            // The agent talking to the user. Extract the message from the
            // tool's arguments and render as a plain assistant block.
            this.suppressedToolCalls.add(data.call_id);
            try {
              const args = JSON.parse(data.arguments_json || "{}");
              if (args.message) this.appendAssistantBlock(args.message);
            } catch (e) { /* ignore */ }
            break;
          }
          this.entryIndex++;
          this.beginToolCall(data);
          break;
        case "TOOL_CALL_OUTPUT_DELTA":
          if (this.suppressedToolCalls.has(data.call_id)) break;
          this.appendToolDelta(data);
          break;
        case "TOOL_CALL_END":
          if (this.suppressedToolCalls.has(data.call_id)) {
            this.suppressedToolCalls.delete(data.call_id);
            break;
          }
          this.finalizeToolCall(data);
          break;
        case "WARNING":
          this.appendBanner("warning", data.message || "");
          break;
        case "ERROR":
          this.appendBanner("error", data.error || "");
          break;
        case "STEERING_INJECTED":
          this.appendBanner("note", "[steered] " + (data.text || ""));
          break;
        case "SESSION_END":
          this.appendBanner("note", "[session ended] " + (data.reason || ""));
          break;
        case "SUBAGENT_START":
          this.beginSubagentRef(data);
          break;
        case "SUBAGENT_END":
          this.finalizeSubagentRef(data);
          break;
        case "COMMUNICATE":
          // Already rendered via TOOL_CALL_START's arguments_json.message.
          // The daemon emits both events for the same content; drop this one
          // to avoid duplicates.
          break;
      }
      this.scrollToBottom();
    },

    appendUserMessage(text, entryIdx) {
      this.cheapToolCluster = null;
      const wrap = document.createElement("div");
      wrap.className = "user-message";
      wrap.dataset.entryIdx = String(entryIdx || "");
      wrap.dataset.userTurn = String(this.userTurnIndex || "");
      const pill = document.createElement("div");
      pill.className = "pill";
      pill.textContent = text;
      const actions = document.createElement("div");
      actions.className = "user-message-actions";
      const copy = document.createElement("span");
      copy.className = "action copy"; copy.textContent = "copy";
      copy.onclick = () => navigator.clipboard.writeText(text);
      const edit = document.createElement("span");
      edit.className = "action edit"; edit.textContent = "✎ edit";
      edit.onclick = () => this.startEdit(wrap, pill, text);
      actions.appendChild(copy); actions.appendChild(edit);
      wrap.appendChild(pill); wrap.appendChild(actions);
      this.conversation.appendChild(wrap);
    },

    startEdit(wrap, pill, originalText) {
      pill.contentEditable = "true";
      pill.focus();
      const range = document.createRange();
      range.selectNodeContents(pill);
      const sel = window.getSelection();
      sel.removeAllRanges(); sel.addRange(range);
      const onKey = (e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          const newText = pill.textContent.trim();
          if (newText && newText !== originalText) {
            pill.removeEventListener("keydown", onKey);
            this.showForkDialog(wrap, originalText, newText);
          } else {
            pill.contentEditable = "false";
            pill.textContent = originalText;
            pill.removeEventListener("keydown", onKey);
          }
        } else if (e.key === "Escape") {
          pill.contentEditable = "false";
          pill.textContent = originalText;
          pill.removeEventListener("keydown", onKey);
        }
      };
      pill.addEventListener("keydown", onKey);
    },

    showForkDialog(userWrap, originalText, editedText) {
      const dialog = document.createElement("div");
      dialog.className = "fork-dialog";
      const title = document.createElement("div");
      title.className = "fork-dialog-title";
      title.textContent = "Editing this message will fork the conversation.";
      const body = document.createElement("div");
      body.className = "fork-dialog-body";
      body.textContent = "The current branch continues with your edited message. The original is preserved as a sibling fork.";
      const labelRow = document.createElement("div");
      labelRow.className = "fork-dialog-label";
      labelRow.innerHTML = "label the original ";
      const input = document.createElement("input");
      input.className = "fork-dialog-input";
      input.value = autoLabel(originalText);
      labelRow.appendChild(input);
      const actions = document.createElement("div");
      actions.className = "fork-dialog-actions";
      const cancel = document.createElement("button");
      cancel.className = "fork-cancel"; cancel.textContent = "cancel"; cancel.type = "button";
      const confirm = document.createElement("button");
      confirm.className = "fork-confirm"; confirm.type = "button";
      confirm.innerHTML = "fork <kbd>⌘↩</kbd>";
      actions.appendChild(cancel); actions.appendChild(confirm);
      dialog.appendChild(title); dialog.appendChild(body); dialog.appendChild(labelRow); dialog.appendChild(actions);
      userWrap.parentNode.insertBefore(dialog, userWrap.nextSibling);

      const cleanup = () => {
        dialog.remove();
        const pill = userWrap.querySelector(".pill");
        pill.contentEditable = "false";
      };
      cancel.onclick = () => {
        cleanup();
        const pill = userWrap.querySelector(".pill");
        pill.textContent = originalText;
      };
      confirm.onclick = async () => {
        const turn = parseInt(userWrap.dataset.entryIdx || "1", 10);
        try {
          const resp = await fetch("/s/" + encodeURIComponent(this.sessionId) + "/fork", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ turn, edited_message: editedText, label: input.value || autoLabel(originalText) }),
          });
          if (!resp.ok) {
            cleanup();
            this.appendBanner("error", "fork failed: " + (await resp.text()));
            return;
          }
          const json = await resp.json();
          // Refresh sidebar so new fork shows up.
          if (window.htmx) htmx.trigger(document.body, "sidebar:refresh");
          // Navigate to the child session.
          window.location.href = "/s/" + encodeURIComponent(json.child_session_id);
        } catch (e) {
          cleanup();
          this.appendBanner("error", "fork failed: " + e.message);
        }
      };
      input.focus();
      input.select();
    },

    beginAssistantMessage() {
      this.cheapToolCluster = null;
      const id = "msg-" + Math.random().toString(36).slice(2, 9);
      this.currentMessageId = id;
      const el = document.createElement("div");
      el.className = "assistant-message";
      this.conversation.appendChild(el);
      this.activeMessages.set(id, { el, textBuf: "", markdownTimer: null });
    },

    // appendAssistantBlock renders a complete assistant message in one shot —
    // no begin/delta/end. Used by COMMUNICATE events.
    appendAssistantBlock(text) {
      this.cheapToolCluster = null;
      const el = document.createElement("div");
      el.className = "assistant-message";
      try { el.innerHTML = window.marked.parse(text); }
      catch (e) { el.textContent = text; }
      this.conversation.appendChild(el);
    },

    appendAssistantDelta(delta) {
      const m = this.activeMessages.get(this.currentMessageId);
      if (!m) return;
      m.textBuf += delta;
      m.el.textContent = m.textBuf;
      this.scheduleMarkdownRefresh(m);
    },

    scheduleMarkdownRefresh(m) {
      if (m.markdownTimer) return;
      m.markdownTimer = setTimeout(() => {
        m.markdownTimer = null;
        try { m.el.innerHTML = window.marked.parse(m.textBuf); }
        catch (e) { m.el.textContent = m.textBuf; }
      }, 500);
    },

    finalizeAssistantMessage(data) {
      const id = this.currentMessageId;
      const m = this.activeMessages.get(id);
      if (!m) return;
      const finalText = (data && data.text) || m.textBuf;
      try { m.el.innerHTML = window.marked.parse(finalText); }
      catch (e) { m.el.textContent = finalText; }
      if (m.markdownTimer) { clearTimeout(m.markdownTimer); m.markdownTimer = null; }
      this.activeMessages.delete(id);
      this.currentMessageId = null;
    },

    beginToolCall(data) {
      const callId = data.call_id || ("tool-" + Math.random().toString(36).slice(2, 9));
      const tool = data.tool_name || "?";
      const renderer = toolRendererFor(tool);
      const args = parseArgs(data.arguments_json);
      const mode = renderer.mode || "default";

      let parent;
      if (mode === "cheap") {
        if (!this.cheapToolCluster) {
          this.cheapToolCluster = document.createElement("div");
          this.cheapToolCluster.className = "tool-call-cluster";
          this.conversation.appendChild(this.cheapToolCluster);
        }
        parent = this.cheapToolCluster;
      } else {
        this.cheapToolCluster = null;
        parent = this.conversation;
      }

      const el = document.createElement("div");
      el.className = "tool-call " + tool;
      const verb = document.createElement("span");
      verb.className = "verb";
      verb.textContent = renderer.friendly || tool;
      el.appendChild(verb);
      const target = document.createElement("span");
      target.className = "target";
      target.textContent = renderer.target ? renderer.target(args, data) : "";
      el.appendChild(target);
      const sep = document.createElement("span"); sep.className = "sep"; sep.textContent = "·";
      const result = document.createElement("span");
      result.className = "result";
      result.textContent = "…";
      el.appendChild(sep); el.appendChild(result);
      parent.appendChild(el);

      const state = { el, resultEl: result, outputBuf: "", tool, args, renderer, body: null };
      if (renderer.body) state.body = renderer.body(args, this.conversation, data);
      this.activeTools.set(callId, state);
    },

    appendToolDelta(data) {
      const m = this.activeTools.get(data.call_id);
      if (!m) return;
      m.outputBuf += data.delta || "";
      if (m.renderer.bodyDelta) m.renderer.bodyDelta(m, m.outputBuf);
    },

    finalizeToolCall(data) {
      const m = this.activeTools.get(data.call_id);
      if (!m) return;
      const out = data.output || m.outputBuf || "";

      if (data.error) {
        m.resultEl.textContent = "error";
        m.resultEl.className = "result result-bad";
      } else {
        const text = m.renderer.result ? m.renderer.result(data, out, m) : "ok";
        m.resultEl.textContent = text;
        m.resultEl.className = "result " + (toolLooksGood(data) ? "result-good" : "result");
      }
      if (m.renderer.bodyEnd) m.renderer.bodyEnd(m, data, out);
      if (m.renderer.replace) {
        const replacement = m.renderer.replace(m, data);
        if (replacement && m.el.parentNode) m.el.parentNode.replaceChild(replacement, m.el);
      }
      this.activeTools.delete(data.call_id);
    },

    beginSubagentRef(data) {
      this.cheapToolCluster = null;
      const agentId = data.agent_id || ("sa-" + Math.random().toString(36).slice(2, 9));
      const ref = document.createElement("div");
      ref.className = "subagent-reference";
      ref.dataset.subagentId = agentId;
      const verb = document.createElement("span");
      verb.className = "verb"; verb.textContent = "subagent";
      const target = document.createElement("span");
      target.className = "target";
      target.textContent = (data.task || "").slice(0, 80);
      const sep = document.createElement("span"); sep.className = "sep"; sep.textContent = "·";
      const dot = document.createElement("span");
      dot.className = "status-indicator";
      dot.style.color = "var(--state-processing)";
      dot.textContent = "●";
      ref.appendChild(verb);
      ref.appendChild(document.createTextNode(" "));
      ref.appendChild(target);
      ref.appendChild(document.createTextNode(" "));
      ref.appendChild(sep);
      ref.appendChild(document.createTextNode(" "));
      ref.appendChild(dot);
      this.conversation.appendChild(ref);
      this.activeSubagents.set(agentId, ref);
    },

    finalizeSubagentRef(data) {
      const agentId = data.agent_id || "";
      const ref = this.activeSubagents.get(agentId);
      if (!ref) {
        // Fallback: no matching SUBAGENT_START — emit a banner
        this.appendBanner("note", "[subagent end] status=" + (data.status || "?"));
        return;
      }
      this.activeSubagents.delete(agentId);
      const dot = ref.querySelector(".status-indicator");
      if (dot) {
        const s = data.status || "done";
        if (s === "done") {
          dot.style.color = "var(--state-idle)";
        } else if (s === "errored") {
          dot.style.color = "var(--state-awaiting)";
        } else {
          dot.style.color = "var(--state-processing)";
        }
        dot.textContent = "●";
      }
      // Append turns count
      if (data.turns_used != null) {
        const sep2 = document.createElement("span"); sep2.className = "sep"; sep2.textContent = "·";
        const turns = document.createElement("span"); turns.className = "result";
        turns.textContent = data.turns_used + " turns";
        ref.appendChild(document.createTextNode(" "));
        ref.appendChild(sep2);
        ref.appendChild(document.createTextNode(" "));
        ref.appendChild(turns);
      }
      // Make clickable if session_id is provided
      if (data.session_id) {
        ref.style.cursor = "pointer";
        ref.onclick = () => { window.location.href = "/s/" + encodeURIComponent(data.session_id); };
      }
    },

    appendBanner(kind, text) {
      this.cheapToolCluster = null;
      const el = document.createElement("div");
      el.className = "banner " + kind;
      el.textContent = "[" + kind + "] " + text;
      this.conversation.appendChild(el);
    },

    scrollToBottom() {
      this.conversation.scrollTop = this.conversation.scrollHeight;
    },

    bindInputForm() {
      const form = document.querySelector("form[data-input-form]");
      if (!form) return;
      const ta = form.querySelector(".message-input");
      const submit = async (e) => {
        e.preventDefault();
        const text = ta.value.trim();
        if (!text) return;
        const sendBtn = form.querySelector(".send-btn");
        if (sendBtn) sendBtn.disabled = true;
        try {
          const resp = await fetch("/s/" + encodeURIComponent(this.sessionId) + "/send", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify({ text }),
          });
          if (!resp.ok) {
            const detail = (await resp.text()).trim() || ("HTTP " + resp.status);
            this.appendBanner("error", "send failed: " + detail);
          } else {
            ta.value = "";
          }
        } catch (err) {
          this.appendBanner("error", "send failed: " + err.message);
        } finally {
          if (sendBtn) sendBtn.disabled = false;
        }
      };
      form.addEventListener("submit", submit);
    },

    bindKeyboard() {
      const ta = document.querySelector(".message-input");
      if (!ta) return;
      ta.addEventListener("keydown", (e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          const form = ta.closest("form");
          if (form) form.requestSubmit();
        }
      });
    },
  };

  function autoLabel(text) {
    return "before " + text.slice(0, 40).replace(/\s+/g, " ").trim();
  }

  function parseArgs(json) {
    if (!json) return {};
    try { return JSON.parse(json); } catch (e) { return {}; }
  }

  function parseToolState(s) {
    if (!s) return null;
    try { return typeof s === "string" ? JSON.parse(s) : s; } catch (e) { return null; }
  }

  function toolLooksGood(data) {
    if (data.error) return false;
    const st = parseToolState(data.tool_state);
    if (st && st.exit_code && st.exit_code !== 0) return false;
    return true;
  }

  function clip(s, n) {
    s = String(s || "");
    return s.length > n ? s.slice(0, n) + "…" : s;
  }

  // toolRendererFor returns the renderer descriptor for a given tool name.
  // Falls back to the default renderer when no entry is registered.
  function toolRendererFor(tool) {
    return toolRenderers[tool] || toolRenderers.__default__;
  }

  // Diff preview helper, used by edit/write/apply_patch.
  function renderDiff(el, content) {
    const max = 1600;
    const trimmed = content.length > max ? content.slice(0, max) + "\n…" : content;
    el.innerHTML = "";
    trimmed.split("\n").forEach(line => {
      const span = document.createElement("span");
      if (line.startsWith("+") && !line.startsWith("+++")) span.className = "add";
      else if (line.startsWith("-") && !line.startsWith("---")) span.className = "del";
      else if (line.startsWith("@@")) span.className = "hunk";
      span.textContent = line + "\n";
      el.appendChild(span);
    });
  }

  // Cheap renderers — read/grep/list_dir/glob — share a common shape.
  const readRenderer = {
    mode: "cheap", friendly: "read",
    target: (a) => a.file_path || a.path || "",
    result: (data, out) => {
      const lines = (out.match(/\n/g) || []).length;
      const total = parseToolState(data.tool_state);
      if (total && total.total_lines) return lines + " of " + total.total_lines + " lines";
      return lines + " lines";
    },
  };
  const grepRenderer = {
    mode: "cheap", friendly: "grep",
    target: (a) => '"' + clip(a.pattern || "", 50) + '" in ' + (a.path || "."),
    result: (data, out) => ((out.match(/\n/g) || []).length) + " hits",
  };
  const lsRenderer = {
    mode: "cheap", friendly: "ls",
    target: (a) => a.path || ".",
    result: (data, out) => ((out.match(/\n/g) || []).length) + " entries",
  };
  const globRenderer = {
    mode: "cheap", friendly: "find",
    target: (a) => a.pattern || a.glob || "",
    result: (data, out) => ((out.match(/\n/g) || []).length) + " matches",
  };

  // Card renderer for shell with collapsible stdout/stderr.
  const shellRenderer = {
    mode: "card", friendly: "shell",
    target: (a) => clip(a.command || a.cmd || "", 200),
    result: (data) => {
      const st = parseToolState(data.tool_state);
      if (st && st.exit_code != null) return "exit " + st.exit_code;
      return data.error ? "error" : "ok";
    },
    body: (args, conversation) => {
      const wrap = document.createElement("details");
      wrap.className = "tool-body shell-body";
      const summary = document.createElement("summary");
      summary.textContent = "output";
      const pre = document.createElement("pre");
      pre.className = "shell-output";
      wrap.appendChild(summary);
      wrap.appendChild(pre);
      conversation.appendChild(wrap);
      return { wrap, pre };
    },
    bodyDelta: (state, out) => {
      if (state.body && state.body.pre) {
        state.body.pre.textContent = clip(out, 8000);
      }
    },
    bodyEnd: (state, data, out) => {
      if (!state.body) return;
      const pre = state.body.pre;
      pre.textContent = clip(out, 8000);
      // Auto-open if non-empty and exit non-zero or output >2 lines.
      const st = parseToolState(data.tool_state);
      const lines = (out.match(/\n/g) || []).length;
      const failed = data.error || (st && st.exit_code && st.exit_code !== 0);
      if (out.trim() === "") {
        state.body.wrap.style.display = "none";
      } else if (failed || lines > 2) {
        state.body.wrap.open = true;
      }
    },
  };

  // Diff renderers for edit/write/apply_patch.
  function diffRenderer(friendly) {
    return {
      mode: "card", friendly,
      target: (a) => a.file_path || a.path || "",
      result: (data, out) => {
        const adds = (out.match(/^\+/gm) || []).filter(l => !l.startsWith("+++")).length;
        const dels = (out.match(/^-/gm) || []).filter(l => !l.startsWith("---")).length;
        if (adds === 0 && dels === 0) return "ok";
        return "+" + adds + " -" + dels;
      },
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

  // task_list renderer — renders the actual checklist from arguments.
  const taskListRenderer = {
    mode: "card", friendly: "tasks",
    target: (a) => {
      if (a.action) return a.action;
      if (Array.isArray(a.task_list)) return "update";
      if (Array.isArray(a.updates) || Array.isArray(a.update)) return "update";
      if (Array.isArray(a.add)) return "add " + a.add.length;
      return "view";
    },
    result: (data, out, state) => {
      const a = state.args || {};
      if (Array.isArray(a.task_list)) return a.task_list.length + " items";
      if (Array.isArray(a.add)) return "+" + a.add.length;
      const updates = a.updates || a.update;
      if (Array.isArray(updates)) return updates.length + " updated";
      return "ok";
    },
    body: (args, conversation) => {
      // Render task_list (full snapshot) or add/update batches as a checklist.
      const items = Array.isArray(args.task_list) ? args.task_list : (Array.isArray(args.add) ? args.add : []);
      if (items.length === 0) return null;
      const list = document.createElement("ul");
      list.className = "tool-body task-list-body";
      for (const t of items) {
        const li = document.createElement("li");
        li.className = "task-row task-status-" + (t.status || "open").replace(/_/g, "-");
        const icon = document.createElement("span");
        icon.className = "task-icon";
        icon.textContent = (t.status === "done" ? "✓" : t.status === "in_progress" ? "▶" : "○");
        const desc = document.createElement("span");
        desc.className = "task-desc";
        desc.textContent = t.description || t.prompt || "";
        li.appendChild(icon);
        li.appendChild(desc);
        list.appendChild(li);
      }
      conversation.appendChild(list);
      return { list };
    },
  };

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

  // spawn_agent renderer — replaces the tool-call line with a subagent
  // reference card on completion (clickable, navigates to subagent session).
  const spawnAgentRenderer = {
    mode: "default", friendly: "subagent",
    target: (a) => clip(a.task || "", 80),
    result: (data) => {
      const st = parseToolState(data.tool_state);
      if (st && st.status) return st.status;
      return "done";
    },
    replace: (state, data) => {
      const st = parseToolState(data.tool_state);
      if (!st || !st.session_id) return null;
      const ref = document.createElement("div");
      ref.className = "subagent-reference";
      ref.dataset.subagentId = st.session_id;
      ref.innerHTML = '<span class="verb">subagent</span> <span class="target"></span>' +
                      ' <span class="sep">·</span> <span class="result-good">●</span>' +
                      ' <span class="result">' + (st.status || "done") + '</span>' +
                      ' <span class="sep">·</span> <span class="result">' + (st.turns_used || 0) + ' turns</span>';
      ref.querySelector(".target").textContent = clip(st.task || state.args.task || "", 80);
      ref.onclick = () => { window.location.href = "/s/" + encodeURIComponent(st.session_id); };
      return ref;
    },
  };

  const subagentControlRenderer = (friendly) => ({
    mode: "cheap", friendly,
    target: (a) => clip(a.session_id || a.agent_id || a.id || "", 26),
    result: () => "ok",
  });

  const defaultRenderer = {
    mode: "default",
    friendly: undefined, // fallback to tool name
    target: (a) => Object.values(a || {}).map(v => typeof v === "string" ? v : "").filter(Boolean).slice(0, 2).join(" "),
    result: (data) => data.error ? "error" : "ok",
  };

  const toolRenderers = {
    __default__: defaultRenderer,
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
    "edit_file": diffRenderer("edit"),
    "write_file": diffRenderer("write"),
    "apply_patch": diffRenderer("patch"),
    "task_list": taskListRenderer,
    "web_fetch": webFetchRenderer,
    "web_search": webSearchRenderer,
    "spawn_agent": spawnAgentRenderer,
    "resume_agent": subagentControlRenderer("resume"),
    "wait": subagentControlRenderer("wait"),
    "close_agent": subagentControlRenderer("close"),
  };

  window.SerfRenderer = SerfRenderer;

  // Tab title — track sessions awaiting reply for the title-count notification
  // (off by default; opt-in via Settings → Notifications).
  function refreshTabTitle() {
    const prefs = readNotifPrefs();
    if (!prefs.title) {
      document.title = "serf hub";
      return;
    }
    fetch("/api/search?q=").then(r => r.json()).then(resp => {
      const awaiting = (resp.live || []).filter(s => s.state === "awaiting").length;
      const sessTitle = document.querySelector(".workspace-title .title")?.textContent?.trim() || "serf hub";
      document.title = (awaiting > 0 ? "(" + awaiting + ") " : "") + sessTitle;
    }).catch(() => { document.title = "serf hub"; });
  }
  function readNotifPrefs() {
    try { return JSON.parse(localStorage.getItem("serf-hub.notifications") || "{}"); }
    catch (e) { return {}; }
  }

  // Copy-session-ID button.
  document.body && document.body.addEventListener("click", (e) => {
    const t = e.target;
    if (!t || !t.matches) return;
    if (t.matches("[data-copy-id]")) {
      e.preventDefault();
      const id = t.getAttribute("data-copy-id");
      if (id && navigator.clipboard) {
        navigator.clipboard.writeText(id).then(() => {
          const orig = t.textContent;
          t.textContent = "✓";
          setTimeout(() => { t.textContent = orig; }, 1200);
        });
      }
    } else if (t.matches("[data-details-trigger]") || t.closest && t.closest("[data-details-trigger]")) {
      e.preventDefault();
      toggleDetailsPanel();
    } else if (t.matches("[data-tasks-trigger]") || t.closest && t.closest("[data-tasks-trigger]")) {
      e.preventDefault();
      toggleTasksPanel();
    }
  });

  function toggleTasksPanel() {
    const existing = document.getElementById("tasks-panel");
    if (existing) {
      if (existing.__pollTimer) clearInterval(existing.__pollTimer);
      existing.remove();
      return;
    }
    // Close details panel if open — they share the same slot.
    const details = document.getElementById("details-panel");
    if (details) details.remove();

    const header = document.querySelector(".workspace-header");
    if (!header) return;
    const id = header.dataset.sessionId;
    if (!id) return;

    const panel = document.createElement("aside");
    panel.id = "tasks-panel";
    panel.className = "details-panel";
    panel.innerHTML = "<div class='details-loading'>loading…</div>";
    document.body.appendChild(panel);

    const tasksURL = "/s/" + encodeURIComponent(id) + "/tasks";
    const refresh = () => {
      fetch(tasksURL).then(r => r.json()).then(tasks => {
        renderTasksInto(panel, tasks);
      }).catch(() => {
        panel.innerHTML = "<div class='details-loading'>failed to load</div>";
      });
    };
    refresh();
    panel.__pollTimer = setInterval(refresh, 2000);

    document.addEventListener("keydown", function escClose(ev) {
      if (ev.key === "Escape") {
        if (panel.__pollTimer) clearInterval(panel.__pollTimer);
        panel.remove();
        document.removeEventListener("keydown", escClose);
      }
    });
  }

  function renderTasksInto(panel, tasks) {
    const total = tasks.length;
    const done = tasks.filter(t => t.status === "done").length;
    const inProg = tasks.filter(t => t.status === "in_progress").length;
    const open = tasks.filter(t => t.status === "open").length;

    const parts = [];
    parts.push("<header class='details-panel-header'>");
    parts.push("<span>tasks · " + done + "/" + total + "</span>");
    parts.push("<span class='details-panel-close'>esc to close</span>");
    parts.push("</header>");

    if (total === 0) {
      parts.push("<div class='tasks-empty'>no tasks for this session</div>");
    } else {
      if (inProg > 0 || open > 0) {
        parts.push("<div class='tasks-summary'>" + inProg + " in progress · " + open + " open · " + done + " done</div>");
      }
      parts.push("<ul class='tasks-list'>");
      for (const t of tasks) {
        const cls = "task-row task-status-" + (t.status || "open").replace(/_/g, "-");
        const icon = taskStatusIcon(t.status);
        const desc = escapeHTML(t.description || "");
        const type = t.type ? "<span class='task-type'>" + escapeHTML(t.type) + "</span>" : "";
        let deps = "";
        if (Array.isArray(t.depends_on) && t.depends_on.length > 0) {
          deps = "<span class='task-deps'>← " + t.depends_on.join(", ") + "</span>";
        }
        parts.push(
          "<li class='" + cls + "'>" +
          "<span class='task-icon'>" + icon + "</span>" +
          "<span class='task-id'>#" + (t.id || "?") + "</span>" +
          type +
          "<span class='task-desc'>" + desc + "</span>" +
          deps +
          "</li>"
        );
      }
      parts.push("</ul>");
    }
    panel.innerHTML = parts.join("");
  }

  function taskStatusIcon(status) {
    switch (status) {
      case "done": return "✓";
      case "in_progress": return "▶";
      case "cancelled": return "✕";
      default: return "○";
    }
  }

  function escapeHTML(s) {
    return String(s)
      .replace(/&/g, "&amp;")
      .replace(/</g, "&lt;")
      .replace(/>/g, "&gt;")
      .replace(/"/g, "&quot;");
  }

  function toggleDetailsPanel() {
    const existing = document.getElementById("details-panel");
    if (existing) { existing.remove(); return; }
    // Close tasks panel if open — they share the same slot.
    const tasks = document.getElementById("tasks-panel");
    if (tasks) {
      if (tasks.__pollTimer) clearInterval(tasks.__pollTimer);
      tasks.remove();
    }
    const header = document.querySelector(".workspace-header");
    if (!header) return;
    const id = header.dataset.sessionId;
    if (!id) return;

    const panel = document.createElement("aside");
    panel.id = "details-panel";
    panel.className = "details-panel";
    panel.innerHTML = "<div class='details-loading'>loading…</div>";
    document.body.appendChild(panel);
    fetch("/s/" + encodeURIComponent(id) + "/details").then(r => r.text()).then(html => {
      panel.innerHTML = html;
    }).catch(() => { panel.innerHTML = "<div class='details-loading'>failed to load</div>"; });

    document.addEventListener("keydown", function escClose(ev) {
      if (ev.key === "Escape") {
        panel.remove();
        document.removeEventListener("keydown", escClose);
      }
    });
  }

  document.addEventListener("DOMContentLoaded", refreshTabTitle);
  document.addEventListener("DOMContentLoaded", () => {
    document.body.addEventListener("htmx:afterSwap", refreshTabTitle);
  });
  if (document.body) document.body.addEventListener("htmx:afterSwap", refreshTabTitle);

  // After every htmx swap, look for a fresh #conversation element and init
  // the renderer on it. Inline <script> blocks inside swapped partials don't
  // reliably execute in the right order across htmx versions, so we use the
  // semantic afterSwap hook instead.
  function autoInit() {
    const conv = document.getElementById("conversation");
    if (conv) SerfRenderer.init(conv);
  }
  document.addEventListener("DOMContentLoaded", autoInit);
  document.body && document.body.addEventListener("htmx:afterSwap", autoInit);
  // If body wasn't ready yet (script loaded in <head>), bind on load.
  document.addEventListener("DOMContentLoaded", () => {
    document.body.addEventListener("htmx:afterSwap", autoInit);
  });
})();
