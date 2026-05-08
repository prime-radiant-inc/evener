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
      const isCheap = ["read_file", "grep_files", "list_dir", "glob"].includes(tool);

      // Cluster cheap reads
      let parent = this.conversation;
      if (isCheap) {
        if (!this.cheapToolCluster) {
          this.cheapToolCluster = document.createElement("div");
          this.cheapToolCluster.className = "tool-call-cluster";
          this.conversation.appendChild(this.cheapToolCluster);
        }
        parent = this.cheapToolCluster;
      } else {
        this.cheapToolCluster = null;
      }

      // Tool call line
      const el = document.createElement("div");
      el.className = "tool-call " + tool;
      const verb = document.createElement("span");
      verb.className = "verb"; verb.textContent = friendlyToolName(tool);
      el.appendChild(verb);
      const target = document.createElement("span");
      target.className = "target";
      target.textContent = describeTarget(tool, data);
      el.appendChild(target);
      const sep = document.createElement("span"); sep.className = "sep"; sep.textContent = "·";
      const result = document.createElement("span");
      result.className = "result";
      result.textContent = "…";
      el.appendChild(sep); el.appendChild(result);
      parent.appendChild(el);

      // Diff body for mutating tools
      let diffEl = null;
      if (["edit_file", "write_file", "apply_patch"].includes(tool)) {
        diffEl = document.createElement("pre");
        diffEl.className = "diff-body";
        this.conversation.appendChild(diffEl);
      }
      this.activeTools.set(callId, { el, resultEl: result, diffEl, outputBuf: "", tool });
    },

    appendToolDelta(data) {
      const m = this.activeTools.get(data.call_id);
      if (!m) return;
      m.outputBuf += data.delta || "";
      if (m.diffEl) renderDiffPreview(m.diffEl, m.outputBuf);
    },

    finalizeToolCall(data) {
      const m = this.activeTools.get(data.call_id);
      if (!m) return;
      const tool = m.tool;
      const out = data.output || m.outputBuf || "";
      if (data.error) {
        m.resultEl.textContent = "error";
        m.resultEl.className = "result result-bad";
      } else {
        m.resultEl.textContent = friendlyResult(tool, data, out);
        m.resultEl.className = "result " + (looksGood(tool, data) ? "result-good" : "result");
      }
      if (m.diffEl) renderDiffPreview(m.diffEl, out);
      // Subagent: spawn_agent end → render as subagent reference
      if (tool === "spawn_agent" && data.tool_state) {
        try {
          const st = typeof data.tool_state === "string" ? JSON.parse(data.tool_state) : data.tool_state;
          if (st && st.session_id) {
            const ref = document.createElement("div");
            ref.className = "subagent-reference";
            ref.dataset.subagentId = st.session_id;
            ref.innerHTML = '<span class="verb">subagent</span> <span class="target"></span>' +
                            ' <span class="sep">·</span> <span class="result-good">●</span>' +
                            ' <span class="result">' + (st.status || "done") + '</span>' +
                            ' <span class="sep">·</span> <span class="result">' + (st.turns_used || 0) + ' turns</span>';
            ref.querySelector(".target").textContent = (st.task || "").slice(0, 80);
            ref.onclick = () => { window.location.href = "/s/" + encodeURIComponent(st.session_id); };
            m.el.parentNode.replaceChild(ref, m.el);
          }
        } catch (e) {}
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
  function friendlyToolName(tool) {
    return ({ "read_file": "read", "write_file": "write", "edit_file": "edit",
              "apply_patch": "patch", "grep_files": "grep", "list_dir": "ls",
              "shell": "shell", "exec_command": "shell", "glob": "find",
              "spawn_agent": "subagent", "web_fetch": "fetch" }[tool] || tool);
  }
  function describeTarget(tool, data) {
    const args = data.arguments_json || "{}";
    try {
      const a = JSON.parse(args);
      if (tool === "shell" || tool === "exec_command") return a.command || "";
      if (tool === "read_file" || tool === "write_file" || tool === "edit_file") return a.file_path || a.path || "";
      if (tool === "grep_files") return '"' + (a.pattern || "") + '" in ' + (a.path || ".");
      if (tool === "list_dir") return a.path || ".";
      if (tool === "web_fetch") return a.url || "";
      if (tool === "spawn_agent") return a.task || "";
      return Object.values(a).slice(0, 2).join(" ");
    } catch (e) { return args; }
  }
  function friendlyResult(tool, data, out) {
    if (tool === "shell" || tool === "exec_command") {
      if (data.tool_state) {
        try { const st = typeof data.tool_state === "string" ? JSON.parse(data.tool_state) : data.tool_state;
          return "exit " + (st.exit_code != null ? st.exit_code : "?");
        } catch (e) {}
      }
      return data.error ? "error" : "ok";
    }
    if (tool === "read_file") {
      const lines = (out.match(/\n/g) || []).length;
      return lines + " lines";
    }
    if (tool === "edit_file" || tool === "write_file" || tool === "apply_patch") {
      const adds = (out.match(/^\+/gm) || []).length;
      return "+" + adds;
    }
    if (tool === "grep_files") {
      const hits = (out.match(/\n/g) || []).length;
      return hits + " hits";
    }
    return "ok";
  }
  function looksGood(tool, data) {
    if (data.error) return false;
    if (data.tool_state) {
      try {
        const st = typeof data.tool_state === "string" ? JSON.parse(data.tool_state) : data.tool_state;
        if (st.exit_code && st.exit_code !== 0) return false;
      } catch (e) {}
    }
    return true;
  }
  function renderDiffPreview(el, content) {
    const max = 800;
    const trimmed = content.length > max ? content.slice(0, max) + "\n…" : content;
    el.innerHTML = "";
    const lines = trimmed.split("\n");
    lines.forEach(line => {
      const span = document.createElement("span");
      if (line.startsWith("+")) span.className = "add";
      else if (line.startsWith("-")) span.className = "del";
      span.textContent = line + "\n";
      el.appendChild(span);
    });
  }

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
