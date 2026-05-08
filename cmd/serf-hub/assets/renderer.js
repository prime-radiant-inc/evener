// SerfRenderer: client-side SSE coalescing for the hub drive page.
// Subscribes to /live/<sessionId>/events, parses event frames, and
// updates the transcript pane with coalesced messages, tool calls,
// and status. Mirrors serf-tui's coalescing model.

(function () {
  "use strict";

  const SerfRenderer = {
    init(opts) {
      this.opts = opts;
      this.sessionId = opts.sessionId;
      this.transcript = opts.transcript;
      this.statusBar = opts.statusBar;
      this.input = opts.input;
      this.activeMessages = new Map(); // messageId -> {el, textBuf}
      this.activeTools = new Map();    // callId -> {el, outputBuf}
      this.currentMessageId = null;
      this.eventSource = null;
      this.transcript.innerHTML = "";
      this.connect();
      this.bindButtons();
    },

    connect() {
      const url = "/live/" + encodeURIComponent(this.sessionId) + "/events";
      this.eventSource = new EventSource(url);

      const kinds = [
        "SESSION_START", "SESSION_END", "USER_INPUT",
        "ASSISTANT_TEXT_START", "ASSISTANT_TEXT_DELTA", "ASSISTANT_TEXT_END",
        "TOOL_CALL_START", "TOOL_CALL_OUTPUT_DELTA", "TOOL_CALL_END",
        "STEERING_INJECTED", "WARNING", "ERROR",
        "SUBAGENT_START", "SUBAGENT_END", "COMMUNICATE",
      ];
      kinds.forEach((kind) => {
        this.eventSource.addEventListener(kind, (ev) => this.handle(kind, ev));
      });
      this.eventSource.onerror = () => {
        // Browser auto-reconnects; nothing to do here.
      };
    },

    handle(kind, ev) {
      let data = {};
      try { data = JSON.parse(ev.data); } catch (e) { /* ignore */ }
      switch (kind) {
        case "SESSION_START":
          if (data.session_id && data.session_id !== this.sessionId) {
            // /clear minted a new session; rebind URL and clear transcript.
            this.sessionId = data.session_id;
            history.replaceState(null, "", "/live/" + encodeURIComponent(data.session_id));
            this.transcript.innerHTML = "";
            this.activeMessages.clear();
            this.activeTools.clear();
          }
          break;
        case "USER_INPUT":
          this.appendUserMessage(data.text || "");
          break;
        case "ASSISTANT_TEXT_START":
          this.beginAssistantMessage();
          break;
        case "ASSISTANT_TEXT_DELTA":
          this.appendAssistantDelta(data.delta || "");
          break;
        case "ASSISTANT_TEXT_END":
          this.finalizeAssistantMessage(data);
          break;
        case "TOOL_CALL_START":
          this.beginToolCall(data);
          break;
        case "TOOL_CALL_OUTPUT_DELTA":
          this.appendToolDelta(data);
          break;
        case "TOOL_CALL_END":
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
          this.appendBanner("note", "[subagent start] " + (data.task || ""));
          break;
        case "SUBAGENT_END":
          this.appendBanner("note", "[subagent end] status=" + (data.status || "?"));
          break;
        case "COMMUNICATE":
          this.appendUserMessage("[communicate] " + (data.message || ""));
          break;
        default:
          break;
      }
      this.scrollToBottom();
    },

    appendUserMessage(text) {
      const el = document.createElement("div");
      el.className = "msg user";
      el.innerHTML = '<div class="role">user</div>';
      const body = document.createElement("div");
      body.textContent = text;
      el.appendChild(body);
      this.transcript.appendChild(el);
    },

    beginAssistantMessage() {
      const id = "msg-" + Math.random().toString(36).slice(2, 9);
      this.currentMessageId = id;
      const el = document.createElement("div");
      el.className = "msg assistant";
      el.innerHTML = '<div class="role">assistant</div><div class="body"></div>';
      this.transcript.appendChild(el);
      this.activeMessages.set(id, { el, textBuf: "" });
    },

    appendAssistantDelta(delta) {
      const id = this.currentMessageId;
      const m = this.activeMessages.get(id);
      if (!m) return;
      m.textBuf += delta;
      // Render partial markdown for live feedback.
      const body = m.el.querySelector(".body");
      try { body.innerHTML = window.marked.parse(m.textBuf); }
      catch (e) { body.textContent = m.textBuf; }
    },

    finalizeAssistantMessage(data) {
      const id = this.currentMessageId;
      const m = this.activeMessages.get(id);
      if (!m) return;
      const final = (data && data.text) || m.textBuf;
      const body = m.el.querySelector(".body");
      try { body.innerHTML = window.marked.parse(final); }
      catch (e) { body.textContent = final; }
      this.activeMessages.delete(id);
      this.currentMessageId = null;
    },

    beginToolCall(data) {
      const callId = data.call_id || ("tool-" + Math.random().toString(36).slice(2, 9));
      const el = document.createElement("div");
      el.className = "msg tool";
      el.innerHTML = '<div class="role">tool · ' + escapeHtml(data.tool_name || "?") + '</div>';
      const args = document.createElement("div");
      args.className = "tool-args";
      args.textContent = (data.arguments_json || "").slice(0, 200);
      el.appendChild(args);
      const out = document.createElement("pre");
      out.className = "tool-output";
      out.textContent = "";
      el.appendChild(out);
      this.transcript.appendChild(el);
      this.activeTools.set(callId, { el, outputBuf: "" });
    },

    appendToolDelta(data) {
      const m = this.activeTools.get(data.call_id);
      if (!m) return;
      m.outputBuf += data.delta || "";
      const out = m.el.querySelector(".tool-output");
      out.textContent = m.outputBuf;
    },

    finalizeToolCall(data) {
      const m = this.activeTools.get(data.call_id);
      if (!m) return;
      const out = m.el.querySelector(".tool-output");
      out.textContent = (data.output || m.outputBuf || "");
      if (data.error) {
        const errEl = document.createElement("div");
        errEl.className = "tool-error";
        errEl.style.color = "var(--error)";
        errEl.textContent = data.error;
        m.el.appendChild(errEl);
      }
      this.activeTools.delete(data.call_id);
    },

    appendBanner(kind, text) {
      const el = document.createElement("div");
      el.className = "msg " + kind;
      el.textContent = "[" + kind + "] " + text;
      this.transcript.appendChild(el);
    },

    scrollToBottom() {
      this.transcript.scrollTop = this.transcript.scrollHeight;
    },

    bindButtons() {
      const post = (path, body) => {
        return fetch("/live/" + encodeURIComponent(this.sessionId) + path, {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: body ? JSON.stringify(body) : null,
        });
      };
      this.opts.sendBtn.addEventListener("click", () => {
        const text = this.input.value.trim();
        if (!text) return;
        post("/input", { text });
        this.input.value = "";
      });
      this.opts.steerBtn.addEventListener("click", () => {
        const text = this.input.value.trim();
        if (!text) return;
        post("/steer", { text });
        this.input.value = "";
      });
      this.opts.interruptBtn.addEventListener("click", () => post("/interrupt"));
      this.opts.compactBtn.addEventListener("click", () => post("/compact"));
      this.opts.clearBtn.addEventListener("click", () => post("/clear"));
      this.opts.shutdownBtn.addEventListener("click", () => {
        if (confirm("shut down this daemon?")) post("/shutdown");
      });
      this.input.addEventListener("keydown", (e) => {
        if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          this.opts.sendBtn.click();
        }
      });
    },
  };

  function escapeHtml(s) {
    return String(s).replace(/[&<>"']/g, (c) => ({
      "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;"
    }[c]));
  }

  window.SerfRenderer = SerfRenderer;
})();
