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
      this.pendingTaskCalls = new Map(); // callId -> args (for system-line rendering on END)
      this.lastCurrentTaskId = null;     // dedupe state for "now on X" system-line
      this.eventBuffer = [];             // queued until cold-load /tasks fetch resolves
      this.descriptionsReady = false;
      // Description cache is per-session: clear before entering this conversation
      // so a stale id→title mapping from a prior session can't bleed in.
      taskDescriptions.clear();
      this.currentMessageId = null;
      this.userTurnIndex = 0;            // counts user turns rendered (for fork divergence)
      this.entryIndex = 0;               // counts ALL entries rendered (matches transcript entry index)
      this.cheapToolCluster = null;      // current cluster div for batching cheap reads
      this.pendingAttachments = [];      // queued image attachments awaiting send

      this.conversation.innerHTML = "";

      const url = this.replayUrl || this.eventsUrl;
      if (url) this.connect(url);

      this.bindInputForm();
      this.bindKeyboard();
      // Cold-load hydration: fetch the task list ONCE before processing any
      // events so the first transition (system-line) renders with task
      // descriptions instead of a #N → title flash a few seconds later.
      // After this lands, descriptionsReady flips and any buffered events
      // get drained through handle().
      this.hydrateDescriptions().then(() => {
        this.descriptionsReady = true;
        const buffered = this.eventBuffer || [];
        this.eventBuffer = null;
        for (const [kind, ev] of buffered) this.handle(kind, ev);
      });
      this.startTaskBadgePoller();
    },

    hydrateDescriptions() {
      if (!this.sessionId) return Promise.resolve();
      return fetch("/s/" + encodeURIComponent(this.sessionId) + "/tasks")
        .then(r => r.ok ? r.json() : [])
        .then(tasks => {
          if (!Array.isArray(tasks)) return;
          for (const t of tasks) {
            if (t && t.id != null && t.description) {
              taskDescriptions.set(t.id, t.description);
            }
          }
          const done = tasks.filter(t => t.status === "done").length;
          updateTasksBadge(done, tasks.length);
        }).catch(() => {});
    },

    // Periodically pull /tasks to keep the workspace tasks-button badge
    // (e.g. "3/7") fresh when the panel is closed and to seed the
    // taskDescriptions cache so system-line transitions can name tasks.
    startTaskBadgePoller() {
      if (this.taskBadgeTimer) clearInterval(this.taskBadgeTimer);
      const tick = () => {
        if (!this.sessionId) return;
        fetch("/s/" + encodeURIComponent(this.sessionId) + "/tasks")
          .then(r => r.ok ? r.json() : [])
          .then(tasks => {
            if (!Array.isArray(tasks)) return;
            for (const t of tasks) {
              if (t && t.id != null && t.description) {
                taskDescriptions.set(t.id, t.description);
              }
            }
            const done = tasks.filter(t => t.status === "done").length;
            updateTasksBadge(done, tasks.length);
          })
          .catch(() => {});
      };
      tick();
      this.taskBadgeTimer = setInterval(tick, 5000);
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
      // Buffer events until the cold-load /tasks fetch resolves, so the
      // first batch of system-lines renders with task descriptions and
      // never shows the #N → title flash on resume.
      if (!this.descriptionsReady && this.eventBuffer) {
        this.eventBuffer.push([kind, ev]);
        return;
      }
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
            this.suppressedToolCalls.clear();
            this.pendingTaskCalls.clear();
            taskDescriptions.clear();
            this.lastCurrentTaskId = null;
            this.userTurnIndex = 0;
            this.entryIndex = 0;
            this.pendingAttachments = [];
            this.renderAttachments();
          }
          break;
        case "USER_INPUT":
          this.userTurnIndex++;
          this.entryIndex++;
          this.appendUserMessage(data.text || "", this.entryIndex, data.images || []);
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
          if (data.tool_name === "task_list") {
            // Render as inline system-line prose; suppress the normal
            // tool-call card. Seed the description cache on append calls.
            this.suppressedToolCalls.add(data.call_id);
            const args = parseArgs(data.arguments_json);
            if (args.action === "append" && Array.isArray(args.tasks)) {
              for (const t of args.tasks) {
                if (t && t.id != null && t.description) {
                  taskDescriptions.set(t.id, t.description);
                }
              }
            }
            this.pendingTaskCalls.set(data.call_id, args);
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
            const pending = this.pendingTaskCalls && this.pendingTaskCalls.get(data.call_id);
            if (pending !== undefined) {
              this.pendingTaskCalls.delete(data.call_id);
              this.appendTaskListSystemLine(pending);
            }
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
          this.appendSteeringMessage(data.text || "");
          break;
        case "SESSION_END":
          // input_complete = clean end-of-turn termination; not user-meaningful.
          // Past replays always end this way and the dim/idle dot already
          // conveys "this conversation is over." Only render for dramatic
          // reasons (errored, interrupted, killed).
          if (data.reason && data.reason !== "input_complete") {
            this.appendBanner("note", "session ended: " + data.reason);
          }
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

    appendUserMessage(text, entryIdx, images) {
      this.cheapToolCluster = null;
      const wrap = document.createElement("div");
      wrap.className = "user-message";
      wrap.dataset.entryIdx = String(entryIdx || "");
      wrap.dataset.userTurn = String(this.userTurnIndex || "");
      const pill = document.createElement("div");
      pill.className = "pill";
      // Thumbnails first so they sit above the prompt text inside the pill.
      if (Array.isArray(images) && images.length > 0) {
        const gallery = document.createElement("div");
        gallery.className = "user-message-images";
        for (const img of images) {
          if (!img || !img.data) continue;
          const el = document.createElement("img");
          el.className = "user-image-thumb";
          el.src = "data:" + (img.media_type || "image/png") + ";base64," + img.data;
          if (img.name) el.alt = img.name;
          el.title = img.name || "attached image";
          gallery.appendChild(el);
        }
        if (gallery.children.length > 0) pill.appendChild(gallery);
      }
      if (text) {
        const t = document.createElement("div");
        t.className = "user-message-text";
        t.textContent = text;
        pill.appendChild(t);
      }
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
      // When images are present the pill has structured children — edit the
      // text node in place so we don't clobber the gallery.
      const textEl = pill.querySelector(".user-message-text") || pill;
      textEl.contentEditable = "true";
      textEl.focus();
      const range = document.createRange();
      range.selectNodeContents(textEl);
      const sel = window.getSelection();
      sel.removeAllRanges(); sel.addRange(range);
      const restore = () => {
        textEl.contentEditable = "false";
        textEl.textContent = originalText;
      };
      const onKey = (e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          const newText = textEl.textContent.trim();
          if (newText && newText !== originalText) {
            textEl.removeEventListener("keydown", onKey);
            this.showForkDialog(wrap, originalText, newText);
          } else {
            restore();
            textEl.removeEventListener("keydown", onKey);
          }
        } else if (e.key === "Escape") {
          restore();
          textEl.removeEventListener("keydown", onKey);
        }
      };
      textEl.addEventListener("keydown", onKey);
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
        const tEl = pill.querySelector(".user-message-text") || pill;
        tEl.contentEditable = "false";
      };
      cancel.onclick = () => {
        cleanup();
        const pill = userWrap.querySelector(".pill");
        const tEl = pill.querySelector(".user-message-text") || pill;
        tEl.textContent = originalText;
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

    // appendSteeringMessage classifies the injected steering text and routes
    // it to one of four treatments:
    //   - current-task: SUPPRESSED entirely (the task panel + system-line
    //     transitions already convey state; this nudge fires every turn).
    //   - full-list: parse it to seed taskDescriptions, then emit a single
    //     compact "task list reloaded · N items" pointer that opens the panel.
    //   - loop / read-only / all-done / unknown: keep the current
    //     full-width steering divider with click-to-expand body.
    appendSteeringMessage(text) {
      this.cheapToolCluster = null;
      const summary = classifySteering(text);

      if (summary.kind === "current-task") {
        // Seed description from the title attribute. The daemon repeats this
        // steering before nearly every model call, so we only emit a "now on
        // X" system-line when the active task ID actually changes — and we
        // skip it when implied by surrounding context:
        //   • the previous element is a user message (the agent picking up
        //     work right after a request is implicit)
        //   • the previous system-line already named this task (avoids
        //     "started X. now on X" redundancy)
        const idNum = summary.taskID ? parseInt(summary.taskID, 10) : null;
        if (idNum && summary.taskTitle) {
          taskDescriptions.set(idNum, summary.taskTitle);
        }
        if (idNum && idNum !== this.lastCurrentTaskId) {
          const previousID = this.lastCurrentTaskId;
          this.lastCurrentTaskId = idNum;
          if (previousID === null) return; // first steering of session: silent
          if (this.lastSystemLineMentions(idNum)) return;
          const line = document.createElement("div");
          line.className = "system-line system-line-now";
          line.textContent = 'now on "' + summary.taskTitle + '"';
          this.conversation.appendChild(line);
        }
        return;
      }
      if (summary.kind === "task-nudge") {
        // The daemon's "consider using task_list" reminder; not user-meaningful.
        return;
      }
      if (summary.kind === "full-list") {
        // Seed all descriptions from the parsed list.
        if (summary.tasks) {
          for (const t of summary.tasks) {
            taskDescriptions.set(t.id, t.description);
          }
        }
        const total = summary.tasks ? summary.tasks.length : 0;
        const line = document.createElement("a");
        line.className = "system-line system-line-pointer";
        line.href = "#";
        line.textContent = "task list reloaded · " + total + " item" + (total === 1 ? "" : "s");
        line.onclick = (e) => { e.preventDefault(); toggleTasksPanel(); };
        this.conversation.appendChild(line);
        return;
      }

      // Default: keep the existing collapsible divider for genuine system
      // notes (loop detection, read-only nudge, all-done, transcript
      // pointer, unknown).
      const el = document.createElement("details");
      el.className = "steering";
      const sum = document.createElement("summary");
      const verb = document.createElement("span");
      verb.className = "steering-verb";
      verb.textContent = "↻ " + summary.label;
      sum.appendChild(verb);
      if (summary.detail) {
        const detail = document.createElement("span");
        detail.className = "steering-detail";
        detail.textContent = " · " + summary.detail;
        sum.appendChild(detail);
      }
      el.appendChild(sum);
      const body = document.createElement("pre");
      body.className = "steering-body";
      body.textContent = summary.cleanText || text;
      el.appendChild(body);
      this.conversation.appendChild(el);
    },

    // appendTaskListSystemLine renders a task_list tool call as a single
    // line of system prose: "marked X done", "now starting Y", "added 3
    // tasks", etc. Multiple updates in one call are joined into one
    // sentence. action=view is suppressed entirely (it's a read with no
    // user-visible change).
    //
    // After rendering, kicks an immediate /tasks fetch so newly-assigned
    // ids (from appends) get a description in cache before the agent's
    // next update — minimising the "marked #N done" fallback window.
    appendTaskListSystemLine(args) {
      if (!args || args.action === "view") return;
      const text = formatTaskListAction(args);
      if (text) {
        const line = document.createElement("div");
        line.className = "system-line";
        line.textContent = text;
        this.conversation.appendChild(line);
      }
      this.refreshTaskBadgeSoon();
    },

    // lastSystemLineMentions returns true if the most-recent system-line in
    // the conversation already contains the description for the given task
    // id. Used to dedupe a redundant "now on X" steering that arrives right
    // after a task_list update saying "started X".
    lastSystemLineMentions(taskID) {
      const desc = taskDescriptions.get(taskID);
      if (!desc) return false;
      const all = this.conversation.querySelectorAll(".system-line");
      const last = all[all.length - 1];
      if (!last) return false;
      return last.textContent.includes('"' + desc + '"');
    },

    refreshTaskBadgeSoon() {
      if (!this.sessionId) return;
      fetch("/s/" + encodeURIComponent(this.sessionId) + "/tasks")
        .then(r => r.ok ? r.json() : [])
        .then(tasks => {
          if (!Array.isArray(tasks)) return;
          for (const t of tasks) {
            if (t && t.id != null && t.description) {
              taskDescriptions.set(t.id, t.description);
            }
          }
          const done = tasks.filter(t => t.status === "done").length;
          updateTasksBadge(done, tasks.length);
        }).catch(() => {});
    },

    scrollToBottom() {
      this.conversation.scrollTop = this.conversation.scrollHeight;
    },

    bindInputForm() {
      const form = document.querySelector("form[data-input-form]");
      if (!form) return;
      const ta = form.querySelector(".message-input");

      // Auto-grow the textarea up to 50% of the viewport, then scroll inside.
      const grow = () => {
        ta.style.height = "auto";
        ta.style.height = Math.min(ta.scrollHeight, window.innerHeight * 0.5) + "px";
      };
      ta.addEventListener("input", grow);
      grow();

      // Steer button: not wired to /steer yet (Step 4 of the bottom-strip plan).
      const steerBtn = form.querySelector("[data-steer-trigger]");
      if (steerBtn) {
        steerBtn.addEventListener("click", () => {
          console.warn("steer not wired yet");
        });
      }

      // Attachments: file picker, drag-drop, paste pipelines all funnel
      // images through addFiles() which validates and pushes onto the queue.
      const filePicker = form.querySelector("[data-file-picker]");
      const attachTrigger = form.querySelector("[data-attach-trigger]");
      const dropZone = form.querySelector("[data-drop-zone]");

      if (attachTrigger && filePicker) {
        attachTrigger.addEventListener("click", () => filePicker.click());
      }
      if (filePicker) {
        filePicker.addEventListener("change", (e) => {
          this.addFiles(e.target.files);
          // Reset so re-selecting the same file fires another change.
          e.target.value = "";
        });
      }
      if (dropZone) {
        const stopAndOver = (e) => {
          e.preventDefault();
          e.stopPropagation();
          dropZone.classList.add("drag-over");
        };
        dropZone.addEventListener("dragenter", stopAndOver);
        dropZone.addEventListener("dragover", stopAndOver);
        dropZone.addEventListener("dragleave", (e) => {
          // Only remove the class when truly leaving the drop zone.
          if (e.target === dropZone) dropZone.classList.remove("drag-over");
        });
        dropZone.addEventListener("drop", (e) => {
          e.preventDefault();
          e.stopPropagation();
          dropZone.classList.remove("drag-over");
          if (e.dataTransfer && e.dataTransfer.files) {
            this.addFiles(e.dataTransfer.files);
          }
        });
      }
      ta.addEventListener("paste", (e) => {
        const items = e.clipboardData && e.clipboardData.items;
        if (!items) return;
        const files = [];
        for (const item of items) {
          if (item.kind === "file" && item.type && item.type.startsWith("image/")) {
            const f = item.getAsFile();
            if (f) files.push(f);
          }
        }
        if (files.length > 0) {
          e.preventDefault();
          this.addFiles(files);
        }
      });

      const submit = async (e) => {
        e.preventDefault();
        const text = ta.value.trim();
        const hasAttachments = this.pendingAttachments && this.pendingAttachments.length > 0;
        if (!text && !hasAttachments) return;
        const sendBtn = form.querySelector(".send-btn");
        if (sendBtn) sendBtn.disabled = true;
        try {
          const body = {
            text,
            images: (this.pendingAttachments || []).map((a) => ({
              media_type: a.mediaType,
              data: a.dataBase64,
              name: a.name,
            })),
          };
          const resp = await fetch("/s/" + encodeURIComponent(this.sessionId) + "/send", {
            method: "POST",
            headers: { "Content-Type": "application/json" },
            body: JSON.stringify(body),
          });
          if (!resp.ok) {
            const detail = (await resp.text()).trim() || ("HTTP " + resp.status);
            this.appendBanner("error", "send failed: " + detail);
          } else {
            ta.value = "";
            ta.style.height = "";
            grow();
            this.pendingAttachments = [];
            this.renderAttachments();
          }
        } catch (err) {
          this.appendBanner("error", "send failed: " + err.message);
        } finally {
          if (sendBtn) sendBtn.disabled = false;
        }
      };
      form.addEventListener("submit", submit);
    },

    // addFiles validates and enqueues image files coming from the picker,
    // drag-drop, or clipboard paste. Errors render as inline error chips.
    addFiles(fileList) {
      const allowed = new Set(["image/png", "image/jpeg", "image/webp", "image/gif"]);
      const maxBytes = 8 * 1024 * 1024;
      const maxQueue = 10;
      const files = Array.from(fileList || []);
      const errors = [];
      const accepted = [];
      for (const file of files) {
        if ((this.pendingAttachments.length + accepted.length) >= maxQueue) {
          errors.push("max 10 attachments");
          break;
        }
        if (!allowed.has(file.type)) {
          errors.push("not an image: " + file.name);
          continue;
        }
        if (file.size > maxBytes) {
          errors.push("too large (>8MB): " + file.name);
          continue;
        }
        accepted.push(file);
      }
      // Render errors immediately even if reads are in flight.
      this.attachmentErrors = (this.attachmentErrors || []).concat(errors);
      if (accepted.length === 0) {
        this.renderAttachments();
        return;
      }
      // Read each accepted file as a data URL, then queue.
      const reads = accepted.map((file) => new Promise((resolve) => {
        const reader = new FileReader();
        reader.onload = () => {
          const result = reader.result || "";
          // Strip "data:image/png;base64," prefix to keep just base64.
          const comma = result.indexOf(",");
          const dataBase64 = comma >= 0 ? result.slice(comma + 1) : result;
          this.pendingAttachments.push({
            name: file.name,
            mediaType: file.type,
            dataBase64,
            thumbnail: result,
          });
          resolve();
        };
        reader.onerror = () => {
          this.attachmentErrors = (this.attachmentErrors || []).concat(["read failed: " + file.name]);
          resolve();
        };
        reader.readAsDataURL(file);
      }));
      Promise.all(reads).then(() => this.renderAttachments());
      // Render now too so error chips show up without waiting for reads.
      this.renderAttachments();
    },

    renderAttachments() {
      const container = document.querySelector("[data-attachments]");
      if (!container) return;
      container.innerHTML = "";
      const truncate = (s, n) => (s && s.length > n ? s.slice(0, n - 1) + "…" : s || "");
      (this.pendingAttachments || []).forEach((att, idx) => {
        const chip = document.createElement("div");
        chip.className = "attachment-chip";
        const img = document.createElement("img");
        img.className = "att-thumb";
        img.src = att.thumbnail;
        img.alt = att.name;
        chip.appendChild(img);
        const label = document.createElement("span");
        label.className = "att-name";
        label.textContent = truncate(att.name, 24);
        chip.appendChild(label);
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "att-remove";
        remove.textContent = "×";
        remove.addEventListener("click", () => {
          this.pendingAttachments.splice(idx, 1);
          this.renderAttachments();
        });
        chip.appendChild(remove);
        container.appendChild(chip);
      });
      const errors = this.attachmentErrors || [];
      errors.forEach((msg, idx) => {
        const chip = document.createElement("div");
        chip.className = "attachment-chip error";
        const label = document.createElement("span");
        label.className = "att-name";
        label.textContent = msg;
        chip.appendChild(label);
        const remove = document.createElement("button");
        remove.type = "button";
        remove.className = "att-remove";
        remove.textContent = "×";
        remove.addEventListener("click", () => {
          this.attachmentErrors.splice(idx, 1);
          this.renderAttachments();
        });
        chip.appendChild(remove);
        container.appendChild(chip);
      });
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

  // Client-side cache of task id → description, keyed by integer id. Seeded
  // from: task_list append calls (args.tasks), task_list update calls (no
  // descriptions in args; ignored), STEERING_INJECTED full-list parses, and
  // the session's /tasks endpoint when the side panel is open or the
  // background poller fires. Used by the system-line renderer to print
  // task transitions by description rather than by raw id.
  const taskDescriptions = new Map();
  function taskDesc(id) {
    const d = taskDescriptions.get(id);
    return d ? '"' + d + '"' : "#" + id;
  }

  function parseArgs(json) {
    if (!json) return {};
    try { return JSON.parse(json); } catch (e) { return {}; }
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

    const taskMatch = stripped.match(/<CURRENT-TASK\s+id="(\d+)">\s*<TITLE>([^<]+)<\/TITLE>/);
    if (taskMatch) {
      return {
        kind: "current-task",
        label: "current task",
        detail: "#" + taskMatch[1] + " " + taskMatch[2].trim(),
        cleanText: stripped,
        taskID: taskMatch[1],
        taskTitle: taskMatch[2].trim(),
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
        // Only strip a trailing reasoning-effort token (low|medium|high|xhigh).
        description = description.replace(/\s*\[(low|medium|high|xhigh)\]\s*$/, "");
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

  function setPanelToggleActive(selector, active) {
    const btn = document.querySelector(selector);
    if (btn) btn.classList.toggle("active", !!active);
  }

  // bindClickOutside dismisses a slide-over panel when the user clicks
  // anywhere outside it AND outside the trigger button that opened it.
  // Capture-phase mousedown so a click that would otherwise land on a
  // different control still closes the panel first. The handler self-removes
  // when (a) it dismisses the panel or (b) it sees the panel was already
  // detached (e.g. because the OTHER panel was opened, swapping it out).
  function bindClickOutside(panel, triggerSelector, closeFn) {
    const onDown = (ev) => {
      if (!panel.parentNode) {
        document.removeEventListener("mousedown", onDown, true);
        return;
      }
      if (panel.contains(ev.target)) return;
      if (ev.target.closest && ev.target.closest(triggerSelector)) return;
      closeFn();
      document.removeEventListener("mousedown", onDown, true);
    };
    document.addEventListener("mousedown", onDown, true);
  }

  function toggleTasksPanel() {
    const existing = document.getElementById("tasks-panel");
    if (existing) {
      if (existing.__pollTimer) clearInterval(existing.__pollTimer);
      existing.remove();
      setPanelToggleActive("[data-tasks-trigger]", false);
      return;
    }
    // Close details panel if open — they share the same slot.
    const details = document.getElementById("details-panel");
    if (details) details.remove();
    setPanelToggleActive("[data-details-trigger]", false);

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
    setPanelToggleActive("[data-tasks-trigger]", true);

    const close = () => {
      if (panel.__pollTimer) clearInterval(panel.__pollTimer);
      panel.remove();
      setPanelToggleActive("[data-tasks-trigger]", false);
    };
    document.addEventListener("keydown", function escClose(ev) {
      if (ev.key === "Escape") {
        close();
        document.removeEventListener("keydown", escClose);
      }
    });
    bindClickOutside(panel, "[data-tasks-trigger]", close);
  }

  function renderTasksInto(panel, tasks) {
    // Cache descriptions for inline rendering (system-line uses this).
    if (Array.isArray(tasks)) {
      for (const t of tasks) {
        if (t && t.id != null && t.description) {
          taskDescriptions.set(t.id, t.description);
        }
      }
    }

    const total = tasks.length;
    const done = tasks.filter(t => t.status === "done").length;
    const inProg = tasks.filter(t => t.status === "in_progress").length;
    const open = tasks.filter(t => t.status === "open").length;
    const cancelled = tasks.filter(t => t.status === "cancelled").length;

    // Update the tasks-button progress badge (e.g., "☑ tasks 3/7") as a
    // side-effect — visible without opening the panel.
    updateTasksBadge(done, total);

    const parts = [];
    parts.push("<header class='details-panel-header'>");
    parts.push("<span>tasks · " + done + "/" + total + "</span>");
    parts.push("<span class='details-panel-close'>esc to close</span>");
    parts.push("</header>");

    if (total === 0) {
      parts.push("<div class='tasks-empty'>no tasks for this session</div>");
      panel.innerHTML = parts.join("");
      return;
    }

    const counts = [];
    if (inProg) counts.push(inProg + " in progress");
    if (open) counts.push(open + " open");
    if (done) counts.push(done + " done");
    if (cancelled) counts.push(cancelled + " cancelled");
    parts.push("<div class='tasks-summary'>" + counts.join(" · ") + "</div>");

    parts.push("<ul class='tasks-list'>");
    for (const t of tasks) {
      const cls = "task-row task-status-" + (t.status || "open").replace(/_/g, "-");
      const icon = taskStatusIcon(t.status);
      const desc = escapeHTML(t.description || "");
      const id = t.id || "?";
      const head =
        "<span class='task-icon'>" + icon + "</span>" +
        "<span class='task-id'>#" + id + "</span>" +
        "<span class='task-desc'>" + desc + "</span>";

      const detail = [];
      if (t.type) {
        detail.push("<dt>type</dt><dd><span class='task-type-pill'>" + escapeHTML(t.type) + "</span></dd>");
      }
      if (t.status) {
        detail.push("<dt>status</dt><dd>" + escapeHTML(t.status) + "</dd>");
      }
      if (Array.isArray(t.depends_on) && t.depends_on.length > 0) {
        detail.push("<dt>depends on</dt><dd>" + t.depends_on.map(x => "#" + x).join(", ") + "</dd>");
      }
      if (t.reasoning_effort) {
        detail.push("<dt>reasoning</dt><dd>" + escapeHTML(t.reasoning_effort) + "</dd>");
      }
      if (t.prompt) {
        detail.push("<dt>prompt</dt><dd class='task-prompt'>" + escapeHTML(t.prompt) + "</dd>");
      }
      if (Array.isArray(t.notes) && t.notes.length > 0) {
        const notesHTML = t.notes.map((n, i) =>
          "<li class='task-note'><span class='task-note-num'>" + (i + 1) + "</span>" +
          "<span class='task-note-text'>" + escapeHTML(n) + "</span></li>"
        ).join("");
        detail.push("<dt>notes</dt><dd><ol class='task-notes-list'>" + notesHTML + "</ol></dd>");
      }

      parts.push(
        "<li class='" + cls + "'>" +
        "<details class='task-row-details'>" +
        "<summary>" + head + "<span class='task-row-chevron'>›</span></summary>" +
        "<dl class='task-detail'>" + detail.join("") + "</dl>" +
        "</details></li>"
      );
    }
    parts.push("</ul>");
    panel.innerHTML = parts.join("");
  }

  function updateTasksBadge(done, total) {
    const btn = document.querySelector("[data-tasks-trigger]");
    if (!btn) return;
    let badge = btn.querySelector(".panel-toggle-badge");
    if (total === 0) {
      if (badge) badge.remove();
      return;
    }
    if (!badge) {
      badge = document.createElement("span");
      badge.className = "panel-toggle-badge";
      btn.appendChild(badge);
    }
    badge.textContent = done + "/" + total;
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
    if (existing) {
      existing.remove();
      setPanelToggleActive("[data-details-trigger]", false);
      return;
    }
    // Close tasks panel if open — they share the same slot.
    const tasks = document.getElementById("tasks-panel");
    if (tasks) {
      if (tasks.__pollTimer) clearInterval(tasks.__pollTimer);
      tasks.remove();
      setPanelToggleActive("[data-tasks-trigger]", false);
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
    setPanelToggleActive("[data-details-trigger]", true);

    const close = () => {
      panel.remove();
      setPanelToggleActive("[data-details-trigger]", false);
    };
    document.addEventListener("keydown", function escClose(ev) {
      if (ev.key === "Escape") {
        close();
        document.removeEventListener("keydown", escClose);
      }
    });
    bindClickOutside(panel, "[data-details-trigger]", close);
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
    // Tear down any orphaned tasks-panel poll interval before the next swap
    // hides/replaces it; otherwise the panel keeps fetching the old session.
    const orphan = document.getElementById("tasks-panel");
    if (orphan && orphan.__pollTimer) {
      clearInterval(orphan.__pollTimer);
      orphan.__pollTimer = null;
    }
    const conv = document.getElementById("conversation");
    if (conv) SerfRenderer.init(conv);
  }
  document.addEventListener("DOMContentLoaded", autoInit);
  document.body && document.body.addEventListener("htmx:afterSwap", autoInit);
})();
