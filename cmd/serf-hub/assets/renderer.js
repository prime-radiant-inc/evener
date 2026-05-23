(function () {
  "use strict";

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

  const SerfRenderer = {
    init(conversationEl) {
      if (!conversationEl) return;
      // Idempotent: if we've already initialized this exact element for this
      // session, don't double-connect. Switching sessions = different element
      // node (htmx swapped innerHTML), so the marker won't be there.
      if (conversationEl.__serfInitialized) return;
      conversationEl.__serfInitialized = true;

      // Close any previous live stream handle so switching sessions doesn't leak
      // connections or replay duplicate events.
      if (this.liveStream) {
        try { this.liveStream.close(); } catch (e) {}
        this.liveStream = null;
      }
      if (this.appwireUnsubscribe) {
        try { this.appwireUnsubscribe(); } catch (e) {}
        this.appwireUnsubscribe = null;
      }
      if (this.appwireConnectionLostUnsubscribe) {
        try { this.appwireConnectionLostUnsubscribe(); } catch (e) {}
        this.appwireConnectionLostUnsubscribe = null;
      }
      if (this.appwireReconnectTimer) {
        clearTimeout(this.appwireReconnectTimer);
        this.appwireReconnectTimer = null;
      }

      this.conversation = conversationEl;
      this.sessionId = conversationEl.dataset.sessionId;
      this.cwd = conversationEl.dataset.cwd || "";
      this.home = conversationEl.dataset.home || "";
      this.appwireRef = null;
      this.appwireThreadId = null;
      this.appwireHydrated = false;
      this.activeTurnId = conversationEl.dataset.activeTurnId || "";
      this.state = conversationEl.dataset.state || "ended";
      this.liveSendCap = null;
      this.liveQueueCap = null;
      this.liveSteerCap = null;
      this.liveCapabilitiesStatus = "";
      this.statusUpdateSeq = 0;

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
      // Image attachments are owned by the SerfComposerAttachments helper
      // (kata r6a1/65mm — paste/drop/file-picker all funnel through it). The
      // submit handler reads .items off this state, base64-encodes the bytes
      // at the wire layer (appwire.js, kata v80q), and clears the bag on
      // successful response. Preserved on error so the user can retry.
      this.composerPasteState = { items: [] };
      this.lastUserText = "";            // most-recent user turn text (for "Retry turn")
      this.lastSubmittedTurn = null;      // {text, items} payload for diagnostic retry
      // queueState is the wire-sourced authoritative queue (kata r80p):
      // every entry is a first-line-truncated string in FIFO order, sourced
      // from thread.serf.queue on cold-load and from thread/queueChanged
      // notifications thereafter. Two browser tabs on the same session see
      // the same state because they both render this projection of daemon
      // truth instead of mirroring local POSTs. depth is the authoritative
      // count, preview is the head-first list.
      this.queueState = { depth: 0, preview: [] };

      this.conversation.innerHTML = "";

      if (window.SerfAppwire && this.sessionId) {
        this.connectAppwire();
      } else {
        this.appendBanner("error", "stream failed: appwire unavailable", { source: "hub", title: "Hub stream error" });
      }

      this.bindInputForm();
      this.syncTurnActionControls();
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

    refreshCapabilitiesForStatus(status, seq) {
      status = String(status || "").trim();
      if (!status || !this.sessionId || this.liveCapabilitiesStatus === status) return false;
      if (!window.SerfAppwire || typeof window.SerfAppwire.readThread !== "function") return false;
      const sessionId = this.sessionId;
      const conversation = this.conversation;
      window.SerfAppwire.readThread(sessionId, false, false)
        .then((resp) => {
          if (this.sessionId !== sessionId || this.conversation !== conversation) return;
          if (seq !== this.statusUpdateSeq) return;
          const thread = (resp && resp.thread) || {};
          const caps = thread.serf && thread.serf.capabilities;
          const refreshedStatus = (thread.status && thread.status.type) || status;
          if (refreshedStatus !== status) return;
          if (caps) {
            if (typeof caps.send === "boolean") this.liveSendCap = caps.send;
            if (typeof caps.queue === "boolean") this.liveQueueCap = caps.queue;
            if (typeof caps.steer === "boolean") this.liveSteerCap = caps.steer;
            this.liveCapabilitiesStatus = refreshedStatus;
          }
          this.updateThreadState(refreshedStatus);
        })
        .catch(() => {
          if (this.sessionId !== sessionId || this.conversation !== conversation) return;
          if (seq !== this.statusUpdateSeq) return;
          this.updateThreadState(status);
        });
      return true;
    },

    resetLiveCapabilities() {
      this.liveSendCap = null;
      this.liveQueueCap = null;
      this.liveSteerCap = null;
      this.liveCapabilitiesStatus = "";
    },

    updateThreadState(state) {
      state = String(state || "").trim();
      if (!state) return;
      this.state = state;
      if (this.conversation) this.conversation.dataset.state = state;
      const ended = state === "ended" || state === "closed";
      if (!this.turnAcceptsActions(state)) this.setActiveTurnId("");
      this.syncTurnActionControls();
      // Prefer source-advertised send/queue capabilities when AppWire has
      // supplied them. Some sources accept Send while active and do not expose
      // Queue, so state alone is not enough to decide composer mode.
      const sendBtn = document.querySelector("form[data-input-form] .send-btn");
      if (sendBtn) {
        if (ended) {
          sendBtn.setAttribute("data-capability-send", "false");
          sendBtn.setAttribute("data-capability-queue", "false");
          sendBtn.disabled = true;
          sendBtn.setAttribute("title", "send unavailable");
        } else if (this.liveCapabilitiesStatus === state && (typeof this.liveSendCap === "boolean" || typeof this.liveQueueCap === "boolean")) {
          const canSend = this.liveSendCap === true;
          const canQueue = this.liveQueueCap === true;
          sendBtn.setAttribute("data-capability-send", canSend ? "true" : "false");
          sendBtn.setAttribute("data-capability-queue", canQueue ? "true" : "false");
          sendBtn.disabled = !canSend && !canQueue;
          if (sendBtn.disabled) sendBtn.setAttribute("title", "send unavailable");
          else sendBtn.removeAttribute("title");
        } else if (state === "active" && this.liveQueueCap === false) {
          sendBtn.setAttribute("data-capability-send", "false");
          sendBtn.setAttribute("data-capability-queue", "false");
          sendBtn.disabled = true;
          sendBtn.setAttribute("title", "send unavailable");
        } else if (state === "active") {
          sendBtn.setAttribute("data-capability-send", "false");
          sendBtn.setAttribute("data-capability-queue", "true");
          sendBtn.disabled = false;
          sendBtn.removeAttribute("title");
        } else {
          sendBtn.setAttribute("data-capability-send", "true");
          sendBtn.setAttribute("data-capability-queue", "false");
          sendBtn.disabled = false;
          sendBtn.removeAttribute("title");
        }
      }
      for (const action of ["compact", "shutdown"]) {
        const btn = document.querySelector('[data-action-trigger="' + action + '"]');
        if (btn) btn.disabled = ended;
      }
    },

    setActiveTurnId(turnId) {
      this.activeTurnId = turnId || "";
      if (this.conversation) this.conversation.dataset.activeTurnId = this.activeTurnId;
      this.syncTurnActionControls();
    },

    syncTurnActionControls() {
      const hasActiveTurn = !!this.activeTurnId;
      const turnAcceptsActions = this.turnAcceptsActions(this.state);
      const interrupt = document.querySelector('[data-action-trigger="interrupt"]');
      if (interrupt) {
        const canInterrupt = interrupt.getAttribute("data-capability-interrupt") !== "false";
        interrupt.disabled = !canInterrupt || !turnAcceptsActions;
      }
      const steer = document.querySelector("[data-steer-trigger]");
      if (steer) {
        // The steer trigger now doubles as the force-steer-drain-queue
        // button (kata 0bq1). It is meaningful as long as there is an active
        // turn — whether or not the queue is non-empty — because pressing it
        // with just textarea text falls back to the classic steer path.
        const canSteer = typeof this.liveSteerCap === "boolean" ? this.liveSteerCap : steer.getAttribute("data-capability-steer") !== "false";
        steer.setAttribute("data-capability-steer", canSteer ? "true" : "false");
        steer.disabled = !canSteer || !hasActiveTurn || !turnAcceptsActions;
      }
    },

    turnAcceptsActions(state) {
      return state === "active" || state === "awaiting";
    },

    hydrateDescriptions() {
      if (!this.sessionId) return Promise.resolve();
      if (window.SerfAppwire) {
        return window.SerfAppwire.tasks(this.sessionId)
          .then(tasks => this.applyTasks(tasks))
          .catch(() => {});
      }
      return partialFetch(sessionPartialPath(this.sessionId, "tasks"))
        .then(r => r.ok ? r.json() : [])
        .then(tasks => this.applyTasks(tasks)).catch(() => {});
    },

    applyTasks(tasks) {
      if (!Array.isArray(tasks)) return;
      for (const t of tasks) {
        if (t && t.id != null && t.description) {
          rememberTask(t);
        }
      }
      const done = tasks.filter(t => t.status === "done").length;
      updateTasksBadge(done, tasks.length);
    },

    // Periodically pull /tasks to keep the workspace tasks-button badge
    // (e.g. "3/7") fresh when the panel is closed and to seed the
    // taskDescriptions cache so system-line transitions can name tasks.
    startTaskBadgePoller() {
      if (this.taskBadgeTimer) clearInterval(this.taskBadgeTimer);
      const tick = () => {
        if (!this.sessionId) return;
        if (window.SerfAppwire) {
          window.SerfAppwire.tasks(this.sessionId)
            .then(tasks => this.applyTasks(tasks))
            .catch(() => {});
          return;
        }
        partialFetch(sessionPartialPath(this.sessionId, "tasks"))
          .then(r => r.ok ? r.json() : [])
          .then(tasks => this.applyTasks(tasks))
          .catch(() => {});
      };
      tick();
      this.taskBadgeTimer = setInterval(tick, 5000);
    },

    // ensureLiveStream wires up the appwire stream for the current session
    // when no stream is open. Called after sending to an ended session:
    // the daemon was just resumed, so without this the new turn would only
    // appear on a full page reload.
    // We don't wipe rendered content — the past replay sequence already
    // populated the DOM, and this just appends new events on top.
    ensureLiveStream() {
      if (this.liveStream) return;
      if (!this.sessionId) return;
      if (window.SerfAppwire) {
        this.connectAppwire();
        return;
      }
      this.appendBanner("error", "stream failed: appwire unavailable", { source: "hub", title: "Hub stream error" });
    },

    connectAppwire() {
      if (!window.SerfAppwire || !this.sessionId) return;
      const sessionId = this.sessionId;
      const conversation = this.conversation;
      this.clearAppwireStream();
      this.liveStream = {
        close: () => this.clearAppwireStream(),
      };
      // Optimistic-rendering registry. SerfAppwire's optimisticCall
      // registers pending entries against this on every turn/start /
      // turn/steer / turn/queue / turn/drainAsSteer; deliverNotification
      // below calls tryReconcile after the authoritative reducer update.
      if (window.SerfAppwirePending && typeof window.SerfAppwirePending.create === "function") {
        // Find the queue-preview <ul data-queue-list> so turn/queue chips
        // land in the queue chrome instead of the transcript. It usually
        // lives outside this.conversation's subtree, so search document-
        // wide and fall back to null (registry then routes queue chips to
        // the conversation pane to remain backward-compatible).
        const queueListEl = (this.conversation && this.conversation.ownerDocument
          ? this.conversation.ownerDocument.querySelector("[data-queue-list]")
          : document.querySelector("[data-queue-list]"));
        this.pending = window.SerfAppwirePending.create({
          conversation: this.conversation,
          queueList: queueListEl,
          onRetry: (intent) => {
            // Re-issue the optimistic call. Errors propagate normally;
            // a new pending entry will be created by the wrapper.
            switch (intent.method) {
              case "turn/steer":
                return window.SerfAppwire.steer(this.sessionId, this.activeTurnId, intent.text);
              case "turn/start":
                return window.SerfAppwire.startTurn(this.appwireRef || this.sessionId, intent.text, intent.items || []);
              case "turn/queue":
                return window.SerfAppwire.queueTurn(this.sessionId, intent.text, intent.items || []);
              case "turn/drainAsSteer":
                return window.SerfAppwire.drainAsSteer(this.sessionId, intent.text, intent.items || []);
            }
          },
        });
        if (typeof window.SerfAppwire.setPendingRegistry === "function") {
          window.SerfAppwire.setPendingRegistry(this.pending);
        }
      }
      // reconcilePendingFromNotification translates an inbound daemon
      // notification into the wire-method name the optimisticCall wrapper
      // registered under, then calls pending.tryReconcile. Returns nothing;
      // unmatched notifications are silently ignored.
      //
      // Drain-special: serf/steering/injected ALSO consumes any in-flight
      // turn/drainAsSteer placeholder (first-come-first-served, regardless
      // of text), because the daemon collapses the queue into one STEERING
      // and the placeholder doesn't know the joined text in advance.
      const reconcilePendingFromNotification = (pending, method, params) => {
        switch (method) {
          case "serf/steering/injected":
            pending.tryReconcile("turn/steer", params || {});
            pending.tryReconcile("turn/drainAsSteer", params || {});
            return;
          case "thread/queueChanged": {
            // Queue chips reconcile against the authoritative preview list,
            // not by text-equality on a single field. The registry walks its
            // pending turn/queue entries and removes any whose text appears
            // in the preview. Chips still in flight (not yet in preview)
            // remain pending until the next queueChanged or until timeout.
            const preview = (params && params.queue && Array.isArray(params.queue.preview))
              ? params.queue.preview.map(p => typeof p === "string" ? p : ((p && p.text) || ""))
              : [];
            if (typeof pending.tryReconcileQueue === "function") {
              pending.tryReconcileQueue(preview);
            }
            return;
          }
          case "item/started":
          case "item/completed":
            if (params && params.item && params.item.type === "userMessage") {
              pending.tryReconcile("turn/start", { text: params.item.text || "", items: params.item.images || [] });
            }
            return;
          case "turn/completed":
            for (const item of (params && params.turn && params.turn.items) || []) {
              if (item && item.type === "userMessage") {
                pending.tryReconcile("turn/start", { text: item.text || "", items: item.images || [] });
              }
            }
            return;
        }
      };
      const pendingNotifications = [];
      const deliverNotification = (method, params) => {
        for (const [kind, data] of window.SerfAppwire.eventsFromNotification(method, params)) {
          this.handleData(kind, data);
        }
        // Single reconciliation site: after the authoritative reducer
        // update has applied, give the pending registry a chance to
        // find a matching placeholder and remove it. The hydration
        // replay loop below also runs through deliverNotification, so
        // replayed notifications get exactly one reconcile pass.
        if (this.pending) {
          reconcilePendingFromNotification(this.pending, method, params);
        }
      };
      const notificationMatches = (params) => {
        const ref = params && (params.ref || (params.item && params.item.ref));
        const threadId = params && (params.threadId || (params.item && params.item.threadId));
        const acceptedRefs = new Set([window.SerfAppwire.refForSession(sessionId)]);
        if (this.appwireRef) acceptedRefs.add(this.appwireRef);
        const acceptedThreadIds = new Set([sessionId]);
        if (this.appwireThreadId) acceptedThreadIds.add(this.appwireThreadId);
        if (ref && !acceptedRefs.has(ref)) return false;
        if (!ref && threadId && !acceptedThreadIds.has(threadId)) return false;
        return true;
      };
      const shouldWaitForHydration = (params) => {
        if (this.appwireRef || this.appwireThreadId) return false;
        const ref = params && (params.ref || (params.item && params.item.ref));
        const threadId = params && (params.threadId || (params.item && params.item.threadId));
        if (ref && ref !== window.SerfAppwire.refForSession(sessionId)) return true;
        if (!ref && threadId && threadId !== sessionId) return true;
        return false;
      };
      let hydratedNotificationKeys = { itemKeys: new Set(), completedItemKeys: new Set() };
      const firstNonEmpty = (...values) => {
        for (const value of values) {
          if (typeof value === "string" && value.trim() !== "") return value;
        }
        return "";
      };
      const scopedItemKey = (threadKey, turnKey, itemKey) => {
        if (!itemKey) return "";
        const parts = [];
        if (threadKey) parts.push("thread:" + threadKey);
        if (turnKey) parts.push("turn:" + turnKey);
        parts.push("item:" + itemKey);
        return parts.join("\x00");
      };
      const notificationItemKey = (params, itemOverride) => {
        params = params || {};
        const item = itemOverride || params.item || {};
        const turn = params.turn || {};
        const threadKey = firstNonEmpty(params.ref, params.threadRef, params.threadId, item.ref, item.threadRef, item.threadId);
        const turnKey = firstNonEmpty(params.turnId, item.turnId, turn.id);
        const itemKey = firstNonEmpty(params.itemId, params.callId, item.callId, item.id, item.itemId);
        return scopedItemKey(threadKey, turnKey, itemKey);
      };
      const hydrationKeysFromThread = (thread) => {
        const itemKeys = new Set();
        const completedItemKeys = new Set();
        const threadKey = firstNonEmpty(
          thread && thread.serf && thread.serf.ref,
          thread && thread.ref,
          thread && thread.threadId,
          thread && thread.id,
          thread && thread.sessionId,
        );
        for (const turn of (thread && thread.turns) || []) {
          const turnKey = firstNonEmpty(turn && turn.id);
          const turnCompleted = String((turn && turn.status) || "").toLowerCase() === "completed";
          for (const item of (turn && turn.items) || []) {
            const itemKey = firstNonEmpty(item && item.callId, item && item.id, item && item.itemId);
            const scoped = scopedItemKey(threadKey, turnKey, itemKey);
            if (scoped) {
              itemKeys.add(scoped);
              const itemCompleted = String((item && item.status) || "").toLowerCase() === "completed";
              if (turnCompleted || itemCompleted) completedItemKeys.add(scoped);
            }
          }
        }
        return { itemKeys, completedItemKeys };
      };
      const hydratedItemCompleted = (itemKey) => {
        return itemKey && hydratedNotificationKeys.completedItemKeys && hydratedNotificationKeys.completedItemKeys.has(itemKey);
      };
      const notificationCoveredByHydration = (method, params) => {
        if (String(method || "").indexOf("item/") === 0) {
          const itemKey = notificationItemKey(params);
          if (hydratedItemCompleted(itemKey)) return true;
        }
        return false;
      };
      const notificationForHydrationReplay = (method, params) => {
        if (method !== "turn/completed" || !params || !params.turn || !Array.isArray(params.turn.items)) {
          return params;
        }
        const items = params.turn.items.filter((item) => {
          const itemKey = notificationItemKey(params, item);
          return !hydratedItemCompleted(itemKey);
        });
        if (items.length === params.turn.items.length) return params;
        return Object.assign({}, params, {
          turn: Object.assign({}, params.turn, { items }),
        });
      };
      this.appwireUnsubscribe = window.SerfAppwire.onNotification((method, params) => {
        if (!this.appwireHydrated) {
          if (shouldWaitForHydration(params) || notificationMatches(params)) {
            pendingNotifications.push([method, params]);
          }
          return;
        }
        if (shouldWaitForHydration(params)) {
          pendingNotifications.push([method, params]);
          return;
        }
        if (!notificationMatches(params)) return;
        deliverNotification(method, params);
      });
      if (typeof window.SerfAppwire.onConnectionLost === "function") {
        this.appwireConnectionLostUnsubscribe = window.SerfAppwire.onConnectionLost((err) => {
          this.clearAppwireStream();
          this.statusUpdateSeq++;
          this.updateThreadState("closed");
          const detail = err && err.message ? err.message : "connection lost";
          this.appendBanner("error", "Local daemon unavailable: " + detail, {
            source: "hub",
            title: "Hub stream error",
          });
          this.scheduleAppwireReconnect();
        });
      }
      window.SerfAppwire.readThread(sessionId, true, true, true)
        .then((resp) => {
          if (this.sessionId !== sessionId || this.conversation !== conversation) return;
          const thread = resp.thread || {};
          if (this.appwireHydrated) this.resetTranscriptReplay();
          this.appwireThreadId = thread.id || this.appwireThreadId;
          this.appwireRef = (thread.serf && thread.serf.ref) || (this.appwireThreadId ? window.SerfAppwire.refForSession(this.appwireThreadId) : null);
          if (typeof window.SerfAppwire.activeTurnIDFromThread === "function") {
            this.setActiveTurnId(window.SerfAppwire.activeTurnIDFromThread(thread));
          }
          hydratedNotificationKeys = hydrationKeysFromThread(thread);
          for (const [kind, data] of window.SerfAppwire.eventsFromThread(thread)) {
            this.handleData(kind, data);
          }
          this.appwireHydrated = true;
          while (pendingNotifications.length > 0) {
            const [method, params] = pendingNotifications.shift();
            if (notificationCoveredByHydration(method, params)) continue;
            const replayParams = notificationForHydrationReplay(method, params);
            if (notificationMatches(replayParams)) deliverNotification(method, replayParams);
          }
        })
        .catch((err) => {
          if (this.sessionId !== sessionId || this.conversation !== conversation) return;
          this.appendBanner("error", "stream failed: " + err.message, { source: "hub", title: "Hub stream error" });
          this.clearAppwireStream();
        });
    },

    clearAppwireStream() {
      if (this.appwireUnsubscribe) {
        try { this.appwireUnsubscribe(); } catch (e) {}
        this.appwireUnsubscribe = null;
      }
      if (this.appwireConnectionLostUnsubscribe) {
        try { this.appwireConnectionLostUnsubscribe(); } catch (e) {}
        this.appwireConnectionLostUnsubscribe = null;
      }
      this.liveStream = null;
    },

    scheduleAppwireReconnect() {
      if (this.appwireReconnectTimer || !this.sessionId || !window.SerfAppwire) return;
      this.appwireReconnectTimer = setTimeout(() => {
        this.appwireReconnectTimer = null;
        this.ensureLiveStream();
      }, 250);
    },

    resetTranscriptReplay() {
      if (!this.conversation) return;
      this.conversation.innerHTML = "";
      this.activeMessages.clear();
      this.activeTools.clear();
      this.activeSubagents.clear();
      this.suppressedToolCalls.clear();
      this.pendingTaskCalls.clear();
      this.currentMessageId = null;
      this.userTurnIndex = 0;
      this.entryIndex = 0;
      this.cheapToolCluster = null;
    },

    handleData(kind, data) {
      this.handle(kind, { data: JSON.stringify(data || {}) });
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
        case "THREAD_STATUS_CHANGED":
          {
            const status = data.status || "";
            const seq = ++this.statusUpdateSeq;
            this.updateThreadState(status);
            this.refreshCapabilitiesForStatus(status, seq);
          }
          break;
        case "TURN_STARTED":
          this.setActiveTurnId(data.turnId || "");
          break;
        case "TURN_COMPLETED":
          if (!data.turnId || data.turnId === this.activeTurnId) {
            this.setActiveTurnId("");
            if (this.turnAcceptsActions(this.state)) this.updateThreadState("idle");
          }
          break;
	        case "SESSION_START":
	          this.statusUpdateSeq++;
	          if (data.session_id && data.session_id !== this.sessionId) {
	            this.sessionId = data.session_id;
	            this.resetLiveCapabilities();
	            history.replaceState(null, "", "/s/" + encodeURIComponent(data.session_id));
            this.conversation.innerHTML = "";
            this.activeMessages.clear();
            this.activeTools.clear();
            this.activeSubagents.clear();
            this.suppressedToolCalls.clear();
            this.pendingTaskCalls.clear();
            taskDescriptions.clear();
            taskDetails.clear();
            this.lastCurrentTaskId = null;
            this.userTurnIndex = 0;
            this.entryIndex = 0;
            // Drop any pending image attachments and let the composer-
            // attachments helper repaint the (now empty) chip container.
            this.composerPasteState = { items: [] };
            this.renderComposerChips();
            // Reset to empty; the next QUEUE_CHANGED event (cold-load or
            // notification) will fill in authoritative state from the wire.
	            this.queueState = { depth: 0, preview: [] };
	            this.renderQueuePreview();
	          }
	          if (data.capabilities) {
	            if (typeof data.capabilities.send === "boolean") this.liveSendCap = data.capabilities.send;
	            if (typeof data.capabilities.queue === "boolean") this.liveQueueCap = data.capabilities.queue;
	            if (typeof data.capabilities.steer === "boolean") this.liveSteerCap = data.capabilities.steer;
	            this.liveCapabilitiesStatus = data.status || "";
	          }
	          if (data.status) {
	            this.updateThreadState(data.status);
	          }
	          break;
        case "QUEUE_CHANGED":
          // Authoritative queue state from the daemon (kata r80p). The
          // depth + preview are stored verbatim; renderQueuePreview reads
          // straight from this.queueState.
          this.queueState = {
            depth: typeof data.depth === "number" ? data.depth : (Array.isArray(data.preview) ? data.preview.length : 0),
            preview: Array.isArray(data.preview) ? data.preview.slice() : [],
          };
          this.renderQueuePreview();
          break;
        case "USER_INPUT":
          this.lastUserText = data.text || "";
          this.lastSubmittedTurn = this.retryPayload(data.text || "", data.images || []);
          if (this.promoteLocalUserMessage(data)) break;
          this.userTurnIndex++;
          if (typeof data.turn === "number" && data.turn > 0) {
            this.entryIndex = data.turn;
          } else {
            this.entryIndex++;
          }
          const userWrap = this.appendUserMessage(data.text || "", this.entryIndex, data.images || []);
          if (data.turnId) userWrap.dataset.turnId = String(data.turnId);
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
              if (String(args.message || "").trim() && !this.lastElementIsAssistantText(args.message)) this.appendAssistantBlock(args.message);
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
                  rememberTask(t);
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
            if (data.tool_name === "communicate") {
              try {
                const args = JSON.parse(data.arguments_json || "{}");
                if (String(args.message || "").trim() && !this.lastElementIsAssistantText(args.message)) this.appendAssistantBlock(args.message);
              } catch (e) { /* ignore */ }
            }
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
          this.appendBanner("warning", data.message || "", data);
          break;
        case "ERROR":
          this.appendBanner("error", data.error || data.message || "", data);
          break;
        case "STEERING_INJECTED":
          this.appendSteeringMessage(data.text || imagePlaceholderForCount((data.images || []).length));
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
      // Each attachment renders as a card with the image thumbnail, filename,
      // and a click handler that opens it in a lightbox at full size.
      if (Array.isArray(images) && images.length > 0) {
        const gallery = document.createElement("div");
        gallery.className = "user-message-images";
        for (const img of images) {
          if (!img) continue;
          // Live USER_INPUT: bytes inline as base64 in img.data.
          // Transcript USER_INPUT: bytes referenced by sha; fetch lazily from
          // /s/<id>/images/<sha> so live payloads stay small.
          let src = "";
          if (img.data) {
            src = "data:" + (img.media_type || "image/png") + ";base64," + img.data;
          } else if (img.sha) {
            src = "/s/" + encodeURIComponent(this.sessionId) + "/images/" + encodeURIComponent(img.sha);
          } else if (img.url) {
            src = img.url;
          } else {
            continue;
          }
          const card = document.createElement("button");
          card.type = "button";
          card.className = "user-image-card";
          card.title = "click to enlarge";
          const thumb = document.createElement("img");
          thumb.className = "user-image-thumb";
          thumb.src = src;
          if (img.name) thumb.alt = img.name;
          card.appendChild(thumb);
          if (img.name) {
            const name = document.createElement("span");
            name.className = "user-image-name";
            name.textContent = img.name;
            card.appendChild(name);
          }
          card.onclick = (e) => { e.stopPropagation(); openImageLightbox(src, img.name || ""); };
          gallery.appendChild(card);
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
      return wrap;
    },

    appendLocalUserMessage(text, images, turnId, previousUserCount) {
      if (this.userMessageCount() > previousUserCount) return;
      this.lastUserText = text || "";
      this.lastSubmittedTurn = this.retryPayload(text || "", images || []);
      this.userTurnIndex++;
      this.entryIndex++;
      const wrap = this.appendUserMessage(text || "", this.entryIndex, images || []);
      wrap.dataset.localEcho = "true";
      wrap.dataset.localImageCount = String(Array.isArray(images) ? images.length : 0);
      if (turnId) wrap.dataset.turnId = String(turnId);
      this.scrollToBottom();
    },

    userMessageCount() {
      if (!this.conversation) return 0;
      // Exclude the optimistic-pending turn/start chip: it carries
      // .user-message for visual styling but is not an authoritative
      // user message yet, and we don't want appendLocalUserMessage to
      // short-circuit just because a pending chip is on screen.
      return this.conversation.querySelectorAll(".user-message:not(.optimistic-pending)").length;
    },

    retryPayload(text, items) {
      return { text: text || "", items: this.retryableAttachmentItems(items) };
    },

    retryableAttachmentItems(items) {
      const out = [];
      for (const item of items || []) {
        if (!item) continue;
        const url = String(item.url || "").trim();
        const sha = String(item.sha || item.sha256 || "").trim();
        const data = item.data;
        if (!url && !sha && itemDataToBase64(data) === "") continue;
        out.push({
          type: item.type || "image",
          mediaType: item.mediaType || item.media_type || "",
          media_type: item.media_type || item.mediaType || "",
          data,
          url,
          sha,
          name: item.name || "",
        });
      }
      return out;
    },

    promoteLocalUserMessage(data) {
      if (!this.conversation) return false;
      const text = this.normalizedUserText(data && data.text);
      const turnId = data && data.turnId ? String(data.turnId) : "";
      const imageCount = Array.isArray(data && data.images) ? data.images.length : 0;
      const localMessages = this.conversation.querySelectorAll('.user-message[data-local-echo="true"]');
      for (const wrap of localMessages) {
        if (turnId && wrap.dataset.turnId && wrap.dataset.turnId !== turnId) continue;
        const textEl = wrap.querySelector(".user-message-text");
        if (this.normalizedUserText(textEl && textEl.textContent) !== text) continue;
        if (Number(wrap.dataset.localImageCount || "0") !== imageCount) continue;
        if (typeof data.turn === "number" && data.turn > 0) {
          wrap.dataset.entryIdx = String(data.turn);
          this.entryIndex = Math.max(this.entryIndex, data.turn);
        }
        if (turnId) wrap.dataset.turnId = turnId;
        delete wrap.dataset.localEcho;
        delete wrap.dataset.localImageCount;
        return true;
      }
      return false;
    },

    normalizedUserText(text) {
      return String(text || "").replace(/\s+/g, " ").trim();
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
      cancel.className = "btn btn-ghost fork-cancel"; cancel.textContent = "cancel"; cancel.type = "button";
      const confirm = document.createElement("button");
      confirm.className = "btn btn-primary fork-confirm"; confirm.type = "button";
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
          const body = { turn, edited_message: editedText, label: input.value || autoLabel(originalText) };
          const json = window.SerfAppwire
            ? await window.SerfAppwire.forkThread(this.sessionId, body)
            : await fetch("/s/" + encodeURIComponent(this.sessionId) + "/fork", {
                method: "POST",
                headers: { "Content-Type": "application/json" },
                body: JSON.stringify(body),
              }).then(async (resp) => {
                if (!resp.ok) throw new Error(await resp.text());
                return resp.json();
              });
          // Refresh sidebar so new fork shows up.
          if (window.htmx) htmx.trigger(document.body, "sidebar:refresh");
          // Navigate to the child session.
          window.location.href = "/s/" + encodeURIComponent(json.ref || json.child_session_id || json.session_id);
        } catch (e) {
          cleanup();
          this.appendBanner("error", "fork failed: " + e.message, { source: "hub", title: "Hub fork error" });
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
      this.activeMessages.set(id, { el, textBuf: "" });
    },

    // appendAssistantBlock renders a complete assistant message in one shot —
    // no begin/delta/end. Used by COMMUNICATE events.
    appendAssistantBlock(text) {
      if (!String(text || "").trim()) return;
      this.cheapToolCluster = null;
      const el = document.createElement("div");
      el.className = "assistant-message";
      try { el.innerHTML = window.marked.parse(text); }
      catch (e) { el.textContent = text; }
      this.conversation.appendChild(el);
    },

    lastElementIsAssistantText(text) {
      const last = this.conversation && this.conversation.lastElementChild;
      if (!last || !last.classList || !last.classList.contains("assistant-message")) return false;
      return this.normalizedAssistantText(last.textContent) === this.renderedAssistantText(text);
    },

    normalizedAssistantText(text) {
      return String(text || "").replace(/\s+/g, " ").trim();
    },

    renderedAssistantText(text) {
      const el = document.createElement("div");
      try { el.innerHTML = window.marked.parse(String(text || "")); }
      catch (e) { el.textContent = String(text || ""); }
      return this.normalizedAssistantText(el.textContent);
    },

    appendAssistantDelta(delta) {
      const m = this.activeMessages.get(this.currentMessageId);
      if (!m) return;
      m.textBuf += delta;
      this.renderAssistantMessage(m, m.textBuf);
    },

    renderAssistantMessage(m, text) {
      try { m.el.innerHTML = window.marked.parse(text); }
      catch (e) { m.el.textContent = text; }
    },

    finalizeAssistantMessage(data) {
      const id = this.currentMessageId;
      const m = this.activeMessages.get(id);
      const finalText = (data && data.text) || (m && m.textBuf) || "";
      if (!m) {
        this.appendAssistantBlock(finalText);
        return;
      }
      this.activeMessages.delete(id);
      this.currentMessageId = null;
      if (!String(finalText || "").trim()) {
        if (m.el.parentNode) m.el.parentNode.removeChild(m.el);
        return;
      }
      this.renderAssistantMessage(m, finalText);
    },

    beginToolCall(data) {
      const callId = data.call_id || ("tool-" + Math.random().toString(36).slice(2, 9));
      const existing = this.toolStateFor(data);
      if (existing) {
        this.rememberToolAlias(existing, callId);
        this.rememberToolAlias(existing, data.item_id);
        return;
      }
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
      const status = document.createElement("span");
      status.className = "tool-status tool-status-pending";
      status.textContent = "…";
      el.appendChild(status);
      const verb = document.createElement("span");
      verb.className = "verb";
      verb.textContent = renderer.friendly || tool;
      el.appendChild(verb);
      const target = document.createElement("span");
      target.className = "target";
      target.textContent = renderer.target ? this.relativizePath(renderer.target(args, data)) : "";
      el.appendChild(target);
      const result = document.createElement("span");
      result.className = "result-detail";
      result.textContent = "";
      const meta = document.createElement("span");
      meta.className = "tool-meta";
      meta.textContent = "";
      el.appendChild(result); el.appendChild(meta);
      parent.appendChild(el);

      const startedAt = toolEventTime(data) || new Date();
      const state = { el, statusEl: status, resultEl: result, metaEl: meta, outputBuf: "", tool, args, renderer, body: null, ids: [], startedAt, durationMs: toolDuration(data) };
      this.renderToolMeta(state, null);
      if (renderer.body) {
        state.body = renderer.body(args, el, data);
        // Default expanded for diffs (edit/write/patch); collapsed for all others.
        const defaultExpanded = renderer.expand === true;
        el.dataset.expanded = defaultExpanded ? "true" : "false";
        // Caret button — keyboard accessible, toggles data-expanded.
        const caret = document.createElement("button");
        caret.type = "button";
        caret.className = "tool-expand-btn";
        caret.setAttribute("aria-label", defaultExpanded ? "collapse body" : "expand body");
        caret.dataset.expandToggle = "";
        caret.textContent = defaultExpanded ? "▾" : "▸";
        el.insertBefore(caret, el.firstChild);
      }
      this.rememberToolAlias(state, callId);
      this.rememberToolAlias(state, data.item_id);
    },

    appendToolDelta(data) {
      const m = this.toolStateFor(data);
      if (!m) return;
      m.outputBuf += data.delta || "";
      if (m.renderer.bodyDelta) m.renderer.bodyDelta(m, m.outputBuf);
    },

    finalizeToolCall(data) {
      const m = this.toolStateFor(data);
      if (!m) return;
      const out = data.output || m.outputBuf || "";

      const ok = !data.error && toolLooksGood(data);
      if (m.statusEl) {
        m.statusEl.textContent = ok ? "✓" : "✕";
        m.statusEl.className = "tool-status " + (ok ? "tool-status-good" : "tool-status-bad");
      }
      if (data.error) {
        m.resultEl.textContent = "";
      } else {
        const text = m.renderer.result ? m.renderer.result(data, out, m) : "";
        m.resultEl.textContent = (text === "ok" || text === "done") ? "" : text;
      }
      const endedAt = toolEventTime(data) || new Date();
      const duration = toolDuration(data);
      if (duration != null) m.durationMs = duration;
      this.renderToolMeta(m, endedAt);
      if (m.renderer.bodyEnd) m.renderer.bodyEnd(m, data, out);
      if (m.renderer.replace) {
        const replacement = m.renderer.replace(m, data);
        if (replacement && m.el.parentNode) m.el.parentNode.replaceChild(replacement, m.el);
      }
      for (const id of m.ids || []) {
        this.activeTools.delete(id);
      }
    },

    toolStateFor(data) {
      if (!data) return null;
      return this.activeTools.get(data.call_id) || this.activeTools.get(data.item_id) || null;
    },

    rememberToolAlias(state, id) {
      if (!state || !id) return;
      if (!state.ids) state.ids = [];
      if (!state.ids.includes(id)) state.ids.push(id);
      this.activeTools.set(id, state);
    },

    // relativizePath strips the session cwd prefix from an absolute path so
    // tool-call targets show project-relative paths (e.g. "handlers/signup.go"
    // instead of "/home/user/project/handlers/signup.go"). For paths outside
    // cwd: replaces the home dir with "~", then middle-truncates if the result
    // is still longer than 40 chars (keeping first ~14 and last ~24 chars).
    relativizePath(p) {
      if (!p) return p;
      if (this.cwd) {
        const prefix = this.cwd.endsWith("/") ? this.cwd : this.cwd + "/";
        if (p.startsWith(prefix)) return p.slice(prefix.length);
        if (p === this.cwd) return ".";
      }
      // Substitute ~ for home directory.
      let result = p;
      if (this.home) {
        const homePrefix = this.home.endsWith("/") ? this.home : this.home + "/";
        if (p.startsWith(homePrefix)) {
          result = "~/" + p.slice(homePrefix.length);
        } else if (p === this.home) {
          result = "~";
        }
      }
      // Middle-truncate if still long.
      if (result.length > 40) {
        result = result.slice(0, 14) + "…" + result.slice(-24);
      }
      return result;
    },

    renderToolMeta(state, endedAt) {
      if (!state || !state.metaEl) return;
      const parts = [];
      if (state.startedAt) parts.push(formatToolClock(state.startedAt));
      const duration = state.durationMs != null ? state.durationMs : (endedAt && state.startedAt ? endedAt - state.startedAt : null);
      if (duration != null) parts.push(formatToolDuration(duration));
      state.metaEl.textContent = parts.join(" · ");
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
      const dot = document.createElement("span");
      dot.className = "status-indicator";
      dot.style.color = "var(--state-processing)";
      dot.textContent = "●";
      ref.appendChild(verb);
      ref.appendChild(target);
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
        const turns = document.createElement("span"); turns.className = "result";
        turns.textContent = data.turns_used + " turns";
        ref.appendChild(turns);
      }
      // Make clickable if session_id is provided
      if (data.session_id) {
        ref.style.cursor = "pointer";
        ref.onclick = () => { window.location.href = "/s/" + encodeURIComponent(data.session_id); };
      }
    },

    appendBanner(kind, text, diagnostic) {
      this.cheapToolCluster = null;
      if ((kind === "error" || kind === "warning") && window.SerfDiagnostics && window.SerfDiagnostics.render) {
        const payload = Object.assign({}, diagnostic || {}, {
          severity: kind,
          message: text || (diagnostic && (diagnostic.message || diagnostic.error)) || "",
        });
        // Build action buttons for the diagnostic card.
        // "Retry turn" is offered when the error comes from the provider and
        // we have a user turn to replay.  Clicking it re-issues the last user
        // turn against the same daemon — the hub auto-resumes if needed.
        const actions = this.buildDiagnosticActions(payload);
        this.conversation.appendChild(window.SerfDiagnostics.render(payload, actions));
        return;
      }
      const el = document.createElement("div");
      el.className = "banner " + kind;
      el.textContent = "[" + kind + "] " + text;
      this.conversation.appendChild(el);
    },

    // buildDiagnosticActions returns an array of action descriptors for the
    // given diagnostic payload, or null if no actions apply.
    //
    // Two diagnostic sources can get a retry button:
    //   - source=provider → "Retry turn"  (model/API failed mid-turn)
    //   - source=hub      → "Reconnect & retry"  (daemon/session unavailable)
    // Both share the same onclick body: re-issue the last user turn against
    // the same session.  The hub's auto-resume layer transparently relaunches
    // a fresh daemon when the original one is gone, so this button works
    // uniformly for daemon/session-unavailable failures.
    buildDiagnosticActions(payload) {
      const classified = window.SerfDiagnostics ? window.SerfDiagnostics.classify(payload) : null;
      if (!classified) return null;
      const source = classified.source;
      if (source !== "provider" && source !== "hub") return null;
      if (source === "hub" && !this.isReconnectRetryDiagnostic(classified)) return null;
      const lastTurn = this.lastSubmittedTurn || { text: this.lastUserText || "", items: [] };
      if (!lastTurn.text && (!Array.isArray(lastTurn.items) || lastTurn.items.length === 0)) return null;
      const label = source === "hub" ? "Reconnect & retry" : "Retry turn";
      const failPrefix = source === "hub" ? "reconnect failed: " : "retry failed: ";
      const errTitle = source === "hub" ? "Reconnect error" : "Retry error";
      return [{ label, onclick: this.makeRetryTurnHandler(lastTurn, failPrefix, errTitle) }];
    },

    isReconnectRetryDiagnostic(diagnostic) {
      const text = ((diagnostic && (diagnostic.message || diagnostic.title || "")) || "").toLowerCase();
      return text.includes("rendezvous") ||
        text.includes("daemon spawn") ||
        text.includes("process exited before rendezvous") ||
        text.includes("resume timed out") ||
        text.includes("local daemon unavailable") ||
        text.includes("source not found") ||
        text.includes("session unavailable");
    },

    // makeRetryTurnHandler builds the onclick body shared by the
    // "Retry turn" and "Reconnect & retry" diagnostic action buttons.  The
    // failure-banner wording is parameterised so it reads naturally for the
    // originating source (retry vs. reconnect).
    //
    // SerfAppwire.startTurn is the only supported path: the hub's auto-resume
    // layer (katas t65c / ws5f / xcas) hangs off MethodTurnStart, so a fetch
    // against /s/<id>/send would re-issue against a dead daemon for the same
    // hub-source error that surfaced the button. If SerfAppwire is missing at
    // click time we surface that as a diagnostic — there is no useful fallback.
    makeRetryTurnHandler(lastTurn, failPrefix, errTitle) {
      const sessionId = this.sessionId;
      const appwireRef = this.appwireRef;
      const text = lastTurn && lastTurn.text || "";
      const items = Array.isArray(lastTurn && lastTurn.items) ? lastTurn.items.slice() : [];
      const self = this;
      return async function() {
        if (!window.SerfAppwire) {
          self.appendBanner("error", failPrefix + "appwire unavailable", { source: "hub", title: errTitle });
          return;
        }
        try {
          const retryItems = await self.hydrateRetryAttachmentItems(items);
          await window.SerfAppwire.startTurn(appwireRef || sessionId, text, retryItems);
          self.ensureLiveStream();
        } catch (err) {
          self.appendBanner("error", failPrefix + err.message, { source: "hub", title: errTitle });
        }
      };
    },

    async hydrateRetryAttachmentItems(items) {
      const hydrated = [];
      for (const item of items || []) {
        if (!item) continue;
        if (item.data || item.url || !item.sha) {
          hydrated.push(item);
          continue;
        }
        const resp = await fetch("/s/" + encodeURIComponent(this.sessionId) + "/images/" + encodeURIComponent(item.sha));
        if (!resp.ok) throw new Error("image retry fetch failed: HTTP " + resp.status);
        const data = await resp.arrayBuffer();
        const contentType = resp.headers && resp.headers.get ? resp.headers.get("content-type") : "";
        hydrated.push(Object.assign({}, item, {
          data,
          mediaType: item.mediaType || item.media_type || contentType || "image/png",
          media_type: item.media_type || item.mediaType || contentType || "image/png",
        }));
      }
      return hydrated;
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
          rememberTask({ id: idNum, description: summary.taskTitle, prompt: summary.taskPrompt });
        }
        if (idNum && idNum !== this.lastCurrentTaskId) {
          const previousID = this.lastCurrentTaskId;
          this.lastCurrentTaskId = idNum;
          if (previousID === null) return; // first steering of session: silent
          if (this.lastSystemLineMentions(idNum)) return;
          const line = document.createElement("div");
          line.className = "system-line system-line-now";
          appendTaskIcon(line, "in_progress");
          line.appendChild(document.createTextNode('now on "' + summary.taskTitle + '"'));
          appendTaskDetailDisclosure(line, taskDetailFor(idNum));
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
            rememberTask(t);
          }
        }
        const total = summary.tasks ? summary.tasks.length : 0;
        const line = document.createElement("a");
        line.className = "system-line system-line-pointer";
        line.href = "#";
        line.textContent = "task list reloaded · " + total + " item" + (total === 1 ? "" : "s");
        line.onclick = (e) => { e.preventDefault(); toggleTasksPanel(null); };
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
        appendTaskIcon(line, taskListIconKind(args));
        line.appendChild(document.createTextNode(text));
        appendTaskListDetails(line, args);
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
      if (window.SerfAppwire) {
        window.SerfAppwire.tasks(this.sessionId)
          .then(tasks => this.applyTasks(tasks))
          .catch(() => {});
        return;
      }
      partialFetch(sessionPartialPath(this.sessionId, "tasks"))
        .then(r => r.ok ? r.json() : [])
        .then(tasks => this.applyTasks(tasks)).catch(() => {});
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

      const snapshotComposerItems = () => ((this.composerPasteState && this.composerPasteState.items) || []).slice();
      const hasPendingComposerItems = () => snapshotComposerItems().some((item) => item && item.pending);
      const clearSubmittedComposerItems = (submitted) => {
        if (!this.composerPasteState || !Array.isArray(this.composerPasteState.items)) return;
        const sent = new Set(submitted || []);
        this.composerPasteState.items = this.composerPasteState.items.filter((item) => !sent.has(item));
        if (this.composerPasteState.items.length === 0 && window.SerfComposerAttachments) {
          window.SerfComposerAttachments.resetMarkerCounter(this.composerPasteState);
        }
        this.renderComposerChips();
      };
      const clearComposerDraftIfUnchanged = (submittedValue) => {
        if (ta.value !== submittedValue) return;
        ta.value = "";
        ta.style.height = "";
        grow();
      };

      // Seed the queue-preview chrome (kata r80p). queueState starts empty
      // on init; cold-load eventsFromThread + thread/queueChanged
      // notifications fill it in from authoritative wire data.
      this.renderQueuePreview();

      // The steer trigger is now the force-steer-drain-queue button (kata
      // 0bq1). Semantics by composer state:
      //   • textarea has text + queue empty → classic /steer with textarea
      //     content (matches kata a08v behavior).
      //   • textarea has text + queue non-empty → send text on the drain
      //     request so the daemon appends and drains atomically.
      //   • textarea empty + queue non-empty → drain.
      //   • textarea empty + queue empty → no-op except focus, matches
      //     the classic empty-steer placeholder.
      const steerBtn = form.querySelector("[data-steer-trigger]");
      if (steerBtn) {
        steerBtn.addEventListener("click", async () => {
	          const submittedValue = ta.value;
	          const text = submittedValue.trim();
	          const pendingItems = snapshotComposerItems();
	          const hasAttachments = pendingItems.length > 0;
	          const hasQueued = (this.queueState && this.queueState.depth > 0) || false;
	          if (hasPendingComposerItems()) {
	            this.appendBanner("error", "image attachment is still processing", { source: "hub", title: "Hub attachment error" });
	            return;
	          }
	          if (!text && !hasQueued && !hasAttachments) {
            ta.placeholder = "type a steering message, then click send as steer…";
            ta.focus();
            return;
          }
          steerBtn.disabled = true;
          try {
            // Path A: classic steer (no queue + no attachments). Keeps the
            // existing single-text /steer pipeline so the daemon writes one
            // STEERING entry with the textarea text. (When attachments are
            // present we always go through drain-as-steer so the image
            // bytes are preserved — /steer is text-only.)
            if (!hasQueued && !hasAttachments) {
              if (window.SerfAppwire) {
                if (!this.activeTurnId) {
                  this.appendBanner("error", "steer failed: no active turn", { source: "hub", title: "Hub steer error" });
                  return;
                }
                await window.SerfAppwire.steer(this.sessionId, this.activeTurnId, text);
              } else {
                const resp = await fetch("/s/" + encodeURIComponent(this.sessionId) + "/steer", {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  // REST shim uses snake_case; the appwire path above keeps `turnId`.
                  body: JSON.stringify({ text, turn_id: this.activeTurnId || "" }),
                });
                if (!resp.ok) {
                  const detail = (await resp.text()).trim() || ("HTTP " + resp.status);
                  this.appendBanner("error", "steer failed: " + detail, { source: "hub", title: "Hub steer error" });
                  return;
                }
              }
              clearComposerDraftIfUnchanged(submittedValue);
              return;
            }
            // Path B: drain. Text/attachments ride on drain-as-steer so the
            // daemon appends and drains the composer payload atomically.
            try {
              if (window.SerfAppwire) {
                await window.SerfAppwire.drainAsSteer(this.sessionId, text, pendingItems);
              } else {
                const fetchItems = pendingItems.map((a) => ({
                  type: "image",
                  mediaType: a.mediaType || "",
                  data: itemDataToBase64(a.data),
                  name: a.name || "",
                }));
                const resp = await fetch("/s/" + encodeURIComponent(this.sessionId) + "/drain-as-steer", {
                  method: "POST",
                  headers: { "Content-Type": "application/json" },
                  body: JSON.stringify({ text, items: fetchItems }),
                });
                if (!resp.ok) {
                  const detail = (await resp.text()).trim() || ("HTTP " + resp.status);
                  const info = resp.headers && resp.headers.get ? resp.headers.get("x-serf-error-info") : "";
                  if (info === "queuedDrainPartial") {
                    clearComposerDraftIfUnchanged(submittedValue);
                    clearSubmittedComposerItems(pendingItems);
                    this.appendBanner("error", "drain failed after queueing: " + detail, { source: "hub", title: "Hub drain error" });
                    return;
                  }
                  this.appendBanner("error", "drain failed: " + detail, { source: "hub", title: "Hub drain error" });
                  return;
                }
              }
              clearComposerDraftIfUnchanged(submittedValue);
              clearSubmittedComposerItems(pendingItems);
              // Daemon collapses the whole queue into one STEERING entry
              // and emits thread/queueChanged with depth=0; the preview
              // will hide when that notification lands. No local mirror.
            } catch (err) {
              if (err && err.serfErrorInfo === "queuedDrainPartial") {
                clearComposerDraftIfUnchanged(submittedValue);
                clearSubmittedComposerItems(pendingItems);
                this.appendBanner("error", "drain failed after queueing: " + err.message, { source: "hub", title: "Hub drain error" });
                return;
              }
              this.appendBanner("error", "drain failed: " + err.message, { source: "hub", title: "Hub drain error" });
            }
          } catch (err) {
            this.appendBanner("error", "steer failed: " + err.message, { source: "hub", title: "Hub steer error" });
          } finally {
            this.syncTurnActionControls();
          }
        });
      }

      // Attachments: paste / drag-drop / file-picker all funnel through
      // SerfComposerAttachments (kata r6a1 + 65mm). The submit handler below
      // reads composerPasteState.items at send/queue/drain time and lets
      // appwire.js base64-encode the ArrayBuffer payloads at the wire
      // boundary (kata v80q). One container — [data-composer-attachments] —
      // holds chips from every entry point; rejection banners go to the
      // sibling [data-attachment-error] element.
      const filePicker = form.querySelector("[data-file-picker]");
      const attachTrigger = form.querySelector("[data-attach-trigger]");
      const dropZone = form.querySelector("[data-drop-zone]");
      if (window.SerfComposerAttachments) {
        if (!this.composerPasteState) this.composerPasteState = { items: [] };
        const pasteContainer = form.querySelector("[data-composer-attachments]");
        window.SerfComposerAttachments.attachComposerImageHandlers(ta, this.composerPasteState);
        if (dropZone) {
          window.SerfComposerAttachments.attachComposerDropHandlers(dropZone, this.composerPasteState);
        }
        if (attachTrigger && filePicker) {
          window.SerfComposerAttachments.attachComposerFilePickerHandlers(attachTrigger, filePicker, this.composerPasteState);
        }
        if (pasteContainer) {
          window.SerfComposerAttachments.renderAttachmentChips(pasteContainer, this.composerPasteState);
        }
      }

      const submit = async (e) => {
        e.preventDefault();
	        const submittedValue = ta.value;
	        const text = submittedValue.trim();
	        const items = snapshotComposerItems();
	        const hasAttachments = items.length > 0;
	        if (hasPendingComposerItems()) {
	          this.appendBanner("error", "image attachment is still processing", { source: "hub", title: "Hub attachment error" });
	          return;
	        }
	        if (!text && !hasAttachments) return;
        const submittedSessionId = this.sessionId;
        const submittedConversation = this.conversation;
        const submittedAppwireRef = this.appwireRef;
        const sendStillCurrent = () => this.sessionId === submittedSessionId && this.conversation === submittedConversation;
        const sendBtn = form.querySelector(".send-btn");
        const canSend = !sendBtn || sendBtn.getAttribute("data-capability-send") !== "false";
        const canQueue = sendBtn && sendBtn.getAttribute("data-capability-queue") === "true";
        // When the session is active the send capability flips off and
        // the queue capability flips on (kata 111a). Route Enter ⌘↵ to
        // turn/queue in that mode so the message is buffered and processed
        // after the active turn completes. Attachments ride the same items
        // bag (kata v80q) — the daemon's TurnQueue handler routes images
        // through queueWithImagesFunc when present.
        if (!canSend && canQueue) {
          if (sendBtn) sendBtn.disabled = true;
          try {
            await this.queueText(text, items);
            if (!sendStillCurrent()) return;
            clearComposerDraftIfUnchanged(submittedValue);
            // Successful queue: drop only the submitted snapshot. Preserved
            // on error so the user can retry, and newly staged attachments
            // remain queued for the next message.
            clearSubmittedComposerItems(items);
          } catch (err) {
            if (sendStillCurrent()) this.appendBanner("error", "queue failed: " + err.message, { source: "hub", title: "Hub queue error" });
          } finally {
            if (sendStillCurrent()) {
              if (sendBtn) sendBtn.disabled = false;
              this.syncTurnActionControls();
            }
          }
          return;
        }
        if (!canSend) {
          this.appendBanner("error", "send is not available for this session", { source: "hub", title: "Hub send error" });
          return;
        }
        this.lastSubmittedTurn = this.retryPayload(text, items);
        if (sendBtn) sendBtn.disabled = true;
        try {
          // Snapshot the items so a successful response can clear the bag
          // afterwards without us having lost the references mid-flight.
          const previousUserCount = this.userMessageCount();
          let turnId = "";
          if (window.SerfAppwire) {
            const resp = await window.SerfAppwire.startTurn(submittedAppwireRef || submittedSessionId, text, items);
            if (!sendStillCurrent()) return;
            turnId = resp && resp.turn && resp.turn.id || "";
            this.setActiveTurnId(turnId || this.activeTurnId);
          } else {
            // Legacy fetch fallback (no appwire). Encode attachments to the
            // /send REST shape on the fly. /send accepts both legacy `images`
            // and v80q-style `items`; we send `items` so the shape matches
            // the appwire path.
            const fetchItems = items.map((a) => ({
              type: "image",
              mediaType: a.mediaType || "",
              data: itemDataToBase64(a.data),
              name: a.name || "",
            }));
            const resp = await fetch("/s/" + encodeURIComponent(submittedSessionId) + "/send", {
              method: "POST",
              headers: { "Content-Type": "application/json" },
              body: JSON.stringify({ text, items: fetchItems }),
            });
            if (!sendStillCurrent()) return;
            if (!resp.ok) {
              const detail = (await resp.text()).trim() || ("HTTP " + resp.status);
              if (!sendStillCurrent()) return;
              this.appendBanner("error", "send failed: " + detail, { source: "hub", title: "Hub send error" });
              return;
            }
          }
          // Build the legacy-shaped images list for local echo rendering —
          // appendUserMessage expects {data: <base64>, media_type, name}.
          const echoImages = items.map((a) => ({
            media_type: a.mediaType || "image/png",
            data: itemDataToBase64(a.data),
            name: a.name || "",
          }));
          this.appendLocalUserMessage(text, echoImages, turnId, previousUserCount);
          clearComposerDraftIfUnchanged(submittedValue);
          // Clear only the submitted snapshot and repaint the chip container
          // so attachments staged while this request was in flight remain.
          clearSubmittedComposerItems(items);
          this.ensureLiveStream();
        } catch (err) {
          if (sendStillCurrent()) this.appendBanner("error", "send failed: " + err.message, { source: "hub", title: "Hub send error" });
        } finally {
          if (sendStillCurrent() && sendBtn && canSend) sendBtn.disabled = false;
        }
      };
      form.addEventListener("submit", submit);
    },

    // renderComposerChips re-renders the chip container from the current
    // composerPasteState. Used after a successful send/queue/drain or when
    // the session is cleared. Pulls the container off the form so callers
    // don't have to thread it through; falls back to a global lookup when
    // multiple workspaces share the renderer (only one is live at a time).
    renderComposerChips() {
      if (!window.SerfComposerAttachments || !this.composerPasteState) return;
      const container = document.querySelector("[data-composer-attachments]");
      if (container) {
        window.SerfComposerAttachments.renderAttachmentChips(container, this.composerPasteState);
      }
    },

    // queueText POSTs to turn/queue (or /s/<id>/queue). The daemon emits a
    // thread/queueChanged notification which updates queueState — no local
    // mirroring (kata r80p). Rejections bubble up; callers surface them as
    // error banners. Optional attachments (kata v80q) ride alongside the
    // text in the items array; an empty/whitespace-only text with
    // attachments is allowed since the daemon's TurnQueue handler accepts
    // images alone.
    async queueText(text, attachments) {
      const trimmed = String(text || "").trim();
      const items = attachments || [];
      if (!trimmed && items.length === 0) throw new Error("text or attachments required");
      if (window.SerfAppwire) {
        await window.SerfAppwire.queueTurn(this.sessionId, trimmed, items);
      } else {
        const fetchItems = items.map((a) => ({
          type: "image",
          mediaType: a.mediaType || "",
          data: itemDataToBase64(a.data),
          name: a.name || "",
        }));
        const resp = await fetch("/s/" + encodeURIComponent(this.sessionId) + "/queue", {
          method: "POST",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ text: trimmed, items: fetchItems }),
        });
        if (!resp.ok) {
          const detail = (await resp.text()).trim() || ("HTTP " + resp.status);
          throw new Error(detail);
        }
      }
    },

    // renderQueuePreview repopulates the queue-preview container from the
    // authoritative wire state (kata r80p). Each entry in queueState.preview
    // is already first-line-truncated by the daemon; we only apply a visual
    // length cap. Hidden via [hidden] when depth is 0.
    renderQueuePreview() {
      const wrap = document.querySelector("[data-queue-preview]");
      if (!wrap) return;
      const list = wrap.querySelector("[data-queue-list]");
      const depthEl = wrap.querySelector("[data-queue-depth]");
      const state = this.queueState || { depth: 0, preview: [] };
      const depth = state.depth || (state.preview || []).length || 0;
      const preview = state.preview || [];
      const hasPendingQueue = this.pending && typeof this.pending.hasQueueEntries === "function" && this.pending.hasQueueEntries();
      if (depthEl) depthEl.textContent = String(depth);
      if (depth === 0) {
        wrap.hidden = !hasPendingQueue;
        if (list) list.innerHTML = "";
        return;
      }
      wrap.hidden = false;
      if (!list) return;
      list.innerHTML = "";
      preview.forEach((entry, idx) => {
        const li = document.createElement("li");
        li.className = "queue-preview-item";
        li.dataset.idx = String(idx);
        const idxEl = document.createElement("span");
        idxEl.className = "qp-idx";
        idxEl.textContent = "#" + (idx + 1);
        const textEl = document.createElement("span");
        textEl.className = "qp-text";
        // Daemon truncates to first line; we cap visual length at 140 chars.
        const oneLine = String(entry || "").split(/\r?\n/)[0].trim();
        textEl.textContent = oneLine.length > 140 ? (oneLine.slice(0, 139) + "…") : oneLine;
        li.appendChild(idxEl);
        li.appendChild(textEl);
        list.appendChild(li);
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
          return;
        }
        // Shift+Enter is the keybind equivalent of the "send as steer"
        // button (kata 0bq1): drain whatever's queued (plus anything in
        // the textarea) as a single STEERING injection. Pre-existing
        // browser default (newline insertion) is suppressed.
        if (e.key === "Enter" && e.shiftKey && !e.metaKey && !e.ctrlKey && !e.altKey) {
          const steer = document.querySelector("[data-steer-trigger]");
          if (steer && !steer.disabled) {
            e.preventDefault();
            steer.click();
            return;
          }
        }
        // "/" as the first character of an empty textarea opens the command
        // palette. Any other "/" (mid-text, or in a non-empty textarea) is
        // literal slash input.
        if (e.key === "/" && !e.metaKey && !e.ctrlKey && !e.altKey && ta.value === "") {
          if (window.SerfSearch && typeof window.SerfSearch.openWith === "function") {
            e.preventDefault();
            window.SerfSearch.openWith("/");
          }
        }
      });
    },
  };

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
      if (line.startsWith("+") && !line.startsWith("+++")) { span.className = "add"; kind = "add"; }
      else if (line.startsWith("-") && !line.startsWith("---")) { span.className = "del"; kind = "del"; }
      else if (line.startsWith("@@")) { span.className = "hunk"; kind = "hunk"; }
      span.dataset.lineKind = kind;
      span.textContent = line + "\n";
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
      : "show " + hiddenLineCount + " more " + (hiddenLineCount === 1 ? "line" : "lines");
  }

  function setExpandableOutput(body, text, opts) {
    if (!body || !body.pre || !body.wrap) return;
    opts = opts || {};
    const lines = splitOutputLines(text);
    body.pre.textContent = lines.slice(0, 5).join("\n");
    body.pre.style.display = lines.length ? "" : "none";
    if (body.moreWrap) {
      body.moreWrap.remove();
      body.moreWrap = null;
    }
    if (lines.length <= 5) return;
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
    const purpose = String((args && args.purpose) || "").trim();
    if (purpose) {
      const purposeEl = document.createElement("div");
      purposeEl.className = "read-tool-purpose";
      purposeEl.textContent = purpose;
      wrap.appendChild(purposeEl);
    }
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
      return outputPreviewBody("shell-body", "shell-output", conversation);
    },
    bodyDelta: (state, out) => {
      if (state.body && state.body.pre) {
        setExpandableOutput(state.body, clip(out, 8000), { moreClass: "shell-output-more", outputClassName: "shell-output" });
      }
    },
    bodyEnd: (state, data, out) => {
      if (!state.body) return;
      const text = data.error || out || "";
      setExpandableOutput(state.body, clip(text, 8000), { moreClass: "shell-output-more", outputClassName: "shell-output" });
      // Auto-open if non-empty and exit non-zero or output >2 lines.
      const st = parseToolState(data.tool_state);
      const failed = data.error || (st && st.exit_code && st.exit_code !== 0);
      if (text.trim() === "") {
        state.body.wrap.style.display = "none";
      } else if (failed && state.body.moreWrap) {
        state.body.moreWrap.open = true;
      }
    },
  };

  // Diff renderers for edit/write/apply_patch.
  function diffResult(data, out) {
    const adds = (out.match(/^\+/gm) || []).filter(l => !l.startsWith("+++")).length;
    const dels = (out.match(/^-/gm) || []).filter(l => !l.startsWith("---")).length;
    if (adds === 0 && dels === 0) return "ok";
    return "+" + adds + " -" + dels;
  }

  function diffRenderer(friendly) {
    return {
      mode: "card", friendly, expand: true,
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
      mode: "card", friendly: "edit", expand: true,
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
      mode: "card", friendly: "patch", expand: true,
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
      ref.innerHTML = '<span class="verb">subagent</span><span class="target"></span>' +
                      '<span class="result-good">●</span>' +
                      '<span class="result">' + (st.status || "done") + '</span>' +
                      '<span class="result">' + (st.turns_used || 0) + ' turns</span>';
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
    "edit_file": editRenderer(),
    "write_file": diffRenderer("write"),
    "apply_patch": patchRenderer(),
    "web_fetch": webFetchRenderer,
    "web_search": webSearchRenderer,
    "spawn_agent": spawnAgentRenderer,
    "resume_agent": subagentControlRenderer("resume"),
    "wait": subagentControlRenderer("wait"),
    "close_agent": subagentControlRenderer("close"),
  };

  // applyStatusDotPulse sets [data-pulse] on every .status-dot under root
  // whose data-state is in the "should breathe" set. Idempotent. Called
  // after any DOM change that may have introduced status dots.
  function applyStatusDotPulse(root) {
    const scope = root || document;
    const dots = scope.querySelectorAll(".status-dot[data-state]");
    dots.forEach(dot => {
      const state = dot.getAttribute("data-state");
      const shouldPulse = state === "active" || state === "awaiting" || state === "errored";
      if (shouldPulse) {
        dot.setAttribute("data-pulse", "");
      } else {
        dot.removeAttribute("data-pulse");
      }
    });
  }
  SerfRenderer.applyStatusDotPulse = applyStatusDotPulse;

  window.SerfRenderer = SerfRenderer;

  // Tab title — track sessions awaiting reply for the title-count notification
  // (off by default; opt-in via Settings → Notifications).
  // Pattern: "<section> · serf hub" for named pages; "serf hub" for the root.
  // Use the shared map from notifications.js if loaded; fall back to a
  // local copy so renderer.js still works if the asset load order ever
  // changes.
  const SETTINGS_SECTION_LABELS = window.SerfSectionLabels || {
    "general": "general", "theme": "theme", "notifications": "notifications",
    "providers": "providers", "agents": "agents",
    "launch-serf": "serf launch", "launch-codex": "codex launch",
    "inrepo": "in-repo config",
    "plugins": "plugins", "skills": "skills", "mcp": "mcp servers",
    "hub": "hub", "storage": "storage", "project": "project",
  };
  function pageSection() {
    // First check for an explicit data-settings-section attribute on the title span
    // (set by the settings shell on initial load).
    const el = document.querySelector(".workspace-title .title[data-settings-section]");
    if (el) {
      // After htmx nav within settings the URL may have changed — prefer the URL.
      const urlMatch = location.pathname.match(/^\/settings\/([^/?]+)/);
      if (urlMatch) return SETTINGS_SECTION_LABELS[urlMatch[1]] || urlMatch[1];
      const sec = el.dataset.settingsSection;
      return SETTINGS_SECTION_LABELS[sec] || sec;
    }
    const titleEl = document.querySelector(".workspace-title .title");
    return titleEl ? titleEl.textContent.trim() : "";
  }
  function formatTitle(section, prefix) {
    const base = section ? section + " \xb7 serf hub" : "serf hub";
    return prefix ? prefix + base : base;
  }
  function refreshTabTitle() {
    const prefs = readNotifPrefs();
    const section = pageSection();
    if (!prefs.title) {
      document.title = formatTitle(section);
      return;
    }
    const searchPromise = window.SerfAppwire
      ? window.SerfAppwire.search("")
      : fetch("/api/search?q=").then(r => r.json());
    searchPromise.then(resp => {
      const awaiting = (resp.live || []).filter(s => s.state === "awaiting").length;
      const prefix = awaiting > 0 ? "(" + awaiting + ") " : "";
      document.title = formatTitle(pageSection(), prefix);
    }).catch(() => { document.title = formatTitle(pageSection()); });
  }
  function readNotifPrefs() {
    try { return JSON.parse(localStorage.getItem("serf-hub.notifications") || "{}"); }
    catch (e) { return {}; }
  }

  // Tool-call expand/collapse caret. Delegated on document so it fires even
  // after htmx swaps the conversation element.
  document.addEventListener("click", (e) => {
    const btn = e.target && (e.target.matches("[data-expand-toggle]") ? e.target : (e.target.closest && e.target.closest("[data-expand-toggle]")));
    if (!btn) return;
    e.preventDefault();
    const toolCall = btn.closest(".tool-call");
    if (!toolCall) return;
    const expanded = toolCall.dataset.expanded === "true";
    const next = !expanded;
    toolCall.dataset.expanded = next ? "true" : "false";
    btn.textContent = next ? "▾" : "▸";
    btn.setAttribute("aria-label", next ? "collapse body" : "expand body");
  });
  document.addEventListener("keydown", (e) => {
    if (e.key !== "Enter" && e.key !== " ") return;
    const btn = e.target && e.target.matches && e.target.matches("[data-expand-toggle]") ? e.target : null;
    if (!btn) return;
    e.preventDefault();
    btn.click();
  });

  // Copy-session-ID button.
  document.body && document.body.addEventListener("click", (e) => {
    const t = e.target;
    if (!t || !t.matches) return;
    if (t.matches("[data-copy-id]")) {
      e.preventDefault();
      const id = t.getAttribute("data-copy-id");
      if (id && navigator.clipboard) {
        navigator.clipboard.writeText(id).then(() => {
          if (window.SerfToast) window.SerfToast.show("Session ID copied", "success");
        }, () => {
          if (window.SerfToast) window.SerfToast.show("Copy failed — clipboard blocked", "error");
        });
      }
    } else if (t.matches("[data-details-trigger]") || t.closest && t.closest("[data-details-trigger]")) {
      e.preventDefault();
      var detailsTrigger = t.matches("[data-details-trigger]") ? t : t.closest("[data-details-trigger]");
      toggleDetailsPanel(detailsTrigger);
    } else if (t.matches("[data-tasks-trigger]") || t.closest && t.closest("[data-tasks-trigger]")) {
      e.preventDefault();
      var tasksTrigger = t.matches("[data-tasks-trigger]") ? t : t.closest("[data-tasks-trigger]");
      toggleTasksPanel(tasksTrigger);
    } else {
      const actionBtn = t.matches("[data-action-trigger]") ? t : (t.closest && t.closest("[data-action-trigger]"));
      if (actionBtn) {
        e.preventDefault();
        if (actionBtn.disabled) return;
        triggerSessionAction(actionBtn.getAttribute("data-action-trigger"));
      }
    }
  });

  // triggerSessionAction sends the app-wire action for the currently-rendered
  // workspace session. Actions are silent on success and surface failures via
  // appendBanner.
  function triggerSessionAction(action) {
    const conv = document.getElementById("conversation");
    const sessionId = conv && conv.getAttribute("data-session-id");
    if (!sessionId) return;
    const turnId = (window.SerfRenderer && window.SerfRenderer.activeTurnId) || (conv && conv.getAttribute("data-active-turn-id")) || "";
    const actionPromise = window.SerfAppwire
      ? window.SerfAppwire.action(sessionId, action, turnId).then(() => ({ ok: true, text: () => Promise.resolve("") }))
      : fetch("/s/" + encodeURIComponent(sessionId) + "/" + action, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        // REST shim is snake_case; the appwire path above keeps the
        // protocol's camelCase `turnId`.
        body: action === "interrupt" ? JSON.stringify({ turn_id: turnId }) : undefined,
      });
    actionPromise
      .then((resp) => {
        if (!resp.ok) {
          return resp.text().then((txt) => {
            if (window.SerfRenderer && window.SerfRenderer.appendBanner) {
              window.SerfRenderer.appendBanner("error", action + " failed: " + (txt || resp.status), { source: "hub", title: "Hub action error" });
            }
          });
        }
      })
      .catch((err) => {
        if (window.SerfRenderer && window.SerfRenderer.appendBanner) {
          window.SerfRenderer.appendBanner("error", action + " failed: " + err.message, { source: "hub", title: "Hub action error" });
        }
      });
  }

  function setPanelToggleActive(selector, active) {
    const btn = document.querySelector(selector);
    if (!btn) return;
    if (active) {
      btn.setAttribute("data-active", "");
    } else {
      btn.removeAttribute("data-active");
    }
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

  function toggleTasksPanel(trigger) {
    const existing = document.getElementById("tasks-panel");
    if (existing) {
      if (existing.__pollTimer) clearInterval(existing.__pollTimer);
      if (existing.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(existing.__trapHandle);
      }
      existing.remove();
      setPanelToggleActive("[data-tasks-trigger]", false);
      return;
    }
    // Close details panel if open — they share the same slot.
    const details = document.getElementById("details-panel");
    if (details) {
      if (details.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(details.__trapHandle);
      }
      details.remove();
    }
    setPanelToggleActive("[data-details-trigger]", false);

    const header = document.querySelector(".workspace-header");
    if (!header) return;
    const id = header.dataset.sessionId;
    if (!id) return;

    const triggerEl = trigger || document.querySelector("[data-tasks-trigger]");

    const panel = document.createElement("aside");
    panel.id = "tasks-panel";
    panel.className = "details-panel";
    panel.innerHTML = "<div class='details-loading'>loading…</div>";
    document.body.appendChild(panel);

    const refresh = () => {
      const tasksPromise = window.SerfAppwire
        ? window.SerfAppwire.tasks(id)
        : partialFetch(sessionPartialPath(id, "tasks")).then(r => r.json());
      tasksPromise.then(tasks => {
        renderTasksInto(panel, tasks);
      }).catch(() => {
        panel.innerHTML = "<div class='details-loading'>failed to load</div>";
      });
    };
    refresh();
    panel.__pollTimer = setInterval(refresh, 2000);
    setPanelToggleActive("[data-tasks-trigger]", true);

    if (window.SerfFocusTrap) {
      panel.__trapHandle = window.SerfFocusTrap.activate(panel, triggerEl);
    }

    const close = () => {
      if (panel.__pollTimer) clearInterval(panel.__pollTimer);
      if (panel.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(panel.__trapHandle);
      }
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
          rememberTask(t);
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
    parts.push("<button class='details-panel-close' aria-label='close panel' onclick=\"document.dispatchEvent(new KeyboardEvent('keydown',{key:'Escape',bubbles:true}))\">✕</button>");
    parts.push("</header>");

    if (total === 0) {
      parts.push("<div class='empty-state empty-state-tasks'><p class='empty-state-title'>No tasks yet</p><p class='empty-state-body'>The agent's task list is empty for this session.</p></div>");
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

  function toggleDetailsPanel(trigger) {
    const existing = document.getElementById("details-panel");
    if (existing) {
      if (existing.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(existing.__trapHandle);
      }
      existing.remove();
      setPanelToggleActive("[data-details-trigger]", false);
      return;
    }
    // Close tasks panel if open — they share the same slot.
    const tasks = document.getElementById("tasks-panel");
    if (tasks) {
      if (tasks.__pollTimer) clearInterval(tasks.__pollTimer);
      if (tasks.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(tasks.__trapHandle);
      }
      tasks.remove();
      setPanelToggleActive("[data-tasks-trigger]", false);
    }
    const header = document.querySelector(".workspace-header");
    if (!header) return;
    const id = header.dataset.sessionId;
    if (!id) return;

    const triggerEl = trigger || document.querySelector("[data-details-trigger]");

    const panel = document.createElement("aside");
    panel.id = "details-panel";
    panel.className = "details-panel";
    panel.innerHTML = "<div class='details-loading'>loading…</div>";
    document.body.appendChild(panel);
    partialFetch(sessionPartialPath(id, "details")).then(r => r.text()).then(html => {
      panel.innerHTML = html;
      if (window.SerfFocusTrap && !panel.__trapHandle) {
        // Re-activate now that the panel has real focusable children.
        panel.__trapHandle = window.SerfFocusTrap.activate(panel, triggerEl);
      }
    }).catch(() => { panel.innerHTML = "<div class='details-loading'>failed to load</div>"; });
    setPanelToggleActive("[data-details-trigger]", true);

    // Initial activation (may have no focusable children until fetch resolves;
    // helper falls back to focusing the panel itself).
    if (window.SerfFocusTrap) {
      panel.__trapHandle = window.SerfFocusTrap.activate(panel, triggerEl);
    }

    const close = () => {
      if (panel.__trapHandle && window.SerfFocusTrap) {
        window.SerfFocusTrap.deactivate(panel.__trapHandle);
      }
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

  // Drive [data-pulse] on status dots after every htmx swap.
  document.addEventListener("htmx:afterSwap", () => {
    if (window.SerfRenderer && window.SerfRenderer.applyStatusDotPulse) {
      window.SerfRenderer.applyStatusDotPulse(document);
    }
  });
  // Also apply on initial load in case dots are already in the page.
  if (typeof window !== "undefined") {
    if (document.readyState === "loading") {
      document.addEventListener("DOMContentLoaded", () => {
        if (window.SerfRenderer && window.SerfRenderer.applyStatusDotPulse) {
          window.SerfRenderer.applyStatusDotPulse(document);
        }
      });
    } else {
      if (window.SerfRenderer && window.SerfRenderer.applyStatusDotPulse) {
        window.SerfRenderer.applyStatusDotPulse(document);
      }
    }
  }
})();
