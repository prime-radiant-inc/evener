(function () {
  "use strict";

  // Stateless helpers live in renderer-format.js (loaded first); import them
  // here so the call sites below stay unchanged.
  const {
    itemDataToBase64,
    imagePlaceholderForCount,
    normalizedJobRefData,
    systemMessageDisplayTitle,
    shouldRenderSystemMessage,
    shouldRenderSystemLine,
    partialFetch,
    sessionPartialPath,
    autoLabel,
    openImageLightboxSet,
    taskDescriptions,
    taskDetails,
    rememberTask,
    taskDetailFor,
    parseArgs,
    toolIntent,
    classifySteering,
    appendTaskIcon,
    appendTaskDetailDisclosure,
    appendTaskListDetails,
    taskListIconKind,
    planStateClass,
    planGlyphForStatus,
    formatTaskListAction,
    parseToolState,
    parseToolJSON,
    formatBytes,
    formatTokenCount,
    compactParts,
    toolLooksGood,
    toolEventTime,
    toolDuration,
    formatToolClock,
    formatToolDuration,
    clip,
    reasoningGist,
    reasoningTier,
    bindDisclosureToggle,
  } = window.SerfRendererInternal;

  // Tool-output renderers live in renderer-tools.js (loaded after format).
  const { toolRendererFor } = window.SerfRendererInternal;

  // Side panels live in renderer-panels.js (loaded after tools).
  const { toggleTasksPanel, currentTaskSummary, updateTasksBadge } =
    window.SerfRendererInternal;

  // Liveness has two honest thresholds (mockup #13). A short silence is
  // EXPECTED (the model is thinking, a long tool is running), so once we cross
  // QUIET we surface a calm, quantized "quiet ~Nm" line but keep breathing.
  // Only once silence crosses STALL do we escalate to concern — amber, a glyph,
  // and the dropped pulse — because past that point we can no longer honestly
  // claim the agent is alive. Reasoning streams as frames, so a real gap means
  // a genuinely silent phase, not normal thinking.
  const LIVENESS_QUIET_MS = 20000;
  const LIVENESS_STALL_MS = 180000;
  const LIVENESS_TICK_MS = 3000;

  const SerfRenderer = {
    // isInPane returns true when the renderer is running inside a framed pane
    // (a side-pane iframe). Uses the standard same-origin cross-frame check;
    // wrapped in try/catch so a cross-origin parent doesn't throw on access.
    // Defined as a method so tests can stub it without touching window.top.
    isInPane() {
      try { return window.self !== window.top; } catch (e) { return true; }
    },

    init(conversationEl) {
      if (!conversationEl) return;
      // Idempotent: if we've already initialized this exact element for this
      // session, don't double-connect. Switching sessions = different element
      // node (htmx swapped innerHTML), so the marker won't be there.
      if (conversationEl.__serfInitialized) return;
      conversationEl.__serfInitialized = true;

      // Compact mode: when running inside a pane iframe, mark <body> with
      // pane-compact so CSS can apply denser layout without any query param.
      if (this.isInPane()) {
        document.body.classList.add("pane-compact");
      }

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
      if (this.livenessTimer) {
        clearInterval(this.livenessTimer);
        this.livenessTimer = null;
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
      this.liveInterruptCap = null;
      this.liveCapabilitiesStatus = "";
      this.statusUpdateSeq = 0;

      this.activeMessages = new Map();   // messageId -> {el, textBuf, markdownTimer}
      this.activeTools = new Map();      // callId -> {el, outputBuf}
      this.activeJobs = new Map();       // appwire jobId -> subagent row el
      this.subagentModule = null;        // current "Subagents (N)" module (per fan-out)
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
      this.lastFrameAt = Date.now();     // wall-clock of the last frame, for honest liveness
      this.livenessLevel = "none";       // none | quiet | concern (mockup #13)
      // New-content pill: counts transcript entries that rendered while the
      // reader was scrolled up, so the floating "↓ N new" affordance can tell
      // them content arrived off-screen. Reset to zero on every session.
      this.newContentCount = 0;
      this.newContentPaintedCount = 0;
      this.newContentNeedsYou = false;
      this.newContentJumpTarget = null;
      // Blocking "needs-you" question (mockup #16): the agentMessage element
      // for an unanswered agent question, or null when none is
      // pending. Drives both the in-flow amber frame (Alt A) and the docked
      // bar above the composer (Alt C).
      this.agentQuestionEl = null;

      this.conversation.innerHTML = "";

      if (window.SerfAppwire && this.sessionId) {
        this.connectAppwire();
      } else {
        // No live stream available: a transport-class failure. Keep it in the
        // chrome (red "Connection lost"), never the conversation.
        this.showConnectionBanner("lost");
      }

      this.bindInputForm();
      this.bindScrollAffordance();
      this.bindPaneParentLinks();
      this.syncTurnActionControls();
      this.bindKeyboard();
      this.ensureLivenessEl();
      this.startLivenessTimer();
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
        // Cold start (mockup #21 Alt C): a session that hydrated with no
        // transcript content is a crafted welcome, not a void. Shown only
        // once nothing has rendered, so resumed/active sessions never see it.
        this.maybeShowWelcome();
      });
      this.startTaskBadgePoller();
      this.autoOpenObservers(conversationEl);
    },

    // autoOpenObservers opens a side pane for each LIVE observer subagent of the
    // session being viewed. The server renders data-observers (filtered to live
    // observers) on #conversation; here we open each one beside the worker so
    // "watch this run live" pairs the worker and its observer automatically.
    // Guards: skip when window.SerfPanes is absent (we are inside a pane iframe,
    // and panes must not nest — same guard as the manual ⇲ button), and skip any
    // href the user has explicitly dismissed (panes.js suppression memory), so
    // auto-open never fights a user's close. SerfPanes.open dedups by href and
    // enforces the pane cap, so re-init and over-cap opens are safe no-ops.
    threadHref(ref) {
      ref = String(ref || "").trim();
      if (!ref) return "";
      if (window.SerfPanes && window.SerfPanes.threadHref) return window.SerfPanes.threadHref(ref);
      return "/thread/" + encodeURIComponent(ref);
    },

    openBeside(spec) {
      if (!spec || !spec.href) return false;
      if (window.SerfPanes && window.SerfPanes.open) {
        window.SerfPanes.open(spec.href, spec.title);
        return true;
      }
      if (this.isInPane && this.isInPane() && window.parent) {
        window.parent.postMessage({ type: "serf:open-beside", href: spec.href, title: spec.title || spec.href }, window.location.origin);
        return true;
      }
      return false;
    },

    bindPaneParentLinks() {
      if (this.__paneParentLinksBound) return;
      this.__paneParentLinksBound = true;
      document.addEventListener("click", (e) => {
        const a = e.target && e.target.closest && e.target.closest("[data-open-parent-beside]");
        if (!a) return;
        const href = a.getAttribute("data-open-parent-beside") || a.getAttribute("href") || "";
        const title = a.textContent || href;
        if (!this.openBeside({ href, title })) return;
        e.preventDefault();
      });
    },

    autoOpenObservers(conversationEl) {
      if (!window.SerfPanes || !conversationEl) return;
      var refs = (conversationEl.dataset.observers || "").split(/\s+/).filter(Boolean);
      for (var i = 0; i < refs.length; i++) {
        var href = this.threadHref(refs[i]);
        if (window.SerfPanes.isSuppressed && window.SerfPanes.isSuppressed(href)) continue;
        window.SerfPanes.open(href, refs[i]);
      }
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
            if (typeof caps.interrupt === "boolean") this.liveInterruptCap = caps.interrupt;
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
      this.liveInterruptCap = null;
      this.liveCapabilitiesStatus = "";
    },

    updateThreadState(state) {
      state = String(state || "").trim();
      if (!state) return;
      this.state = state;
      if (this.conversation) this.conversation.dataset.state = state;
      // A turn that ends by asking the user (awaiting) often flips state in a
      // separate frame from the one that rendered the question. If the
      // new-content pill is already showing off-screen content, upgrade it to
      // the attention "↓ needs you" treatment.
      if (state === "awaiting" && this.newContentCount > 0) {
        this.newContentNeedsYou = true;
        this.renderNewContentPill();
      }
      // The awaiting flip is the daemon's "agent is blocked on you" signal;
      // (re)evaluate the docked needs-you bar whenever the state changes.
      this.renderNeedsYouDock();
      const ended = state === "ended" || state === "closed";
      if (!this.turnAcceptsActions(state)) this.setActiveTurnId("");
      this.syncTurnActionControls();
      // Honest liveness (mockup #8 alt A): if the session is no longer live,
      // every still-"running" subagent row that never reported a completion
      // demotes to a neutral terminal "?" unknown — a spinner must never
      // outlive the work it claims to track.
      this.finalizeDanglingSubagents(state);
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
        const canInterrupt = typeof this.liveInterruptCap === "boolean" ? this.liveInterruptCap : interrupt.getAttribute("data-capability-interrupt") !== "false";
        interrupt.setAttribute("data-capability-interrupt", canInterrupt ? "true" : "false");
        interrupt.disabled = !canInterrupt || !hasActiveTurn || !turnAcceptsActions;
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
      updateTasksBadge(done, tasks.length, currentTaskSummary(tasks));
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
      // Transport-class failure: surface in the chrome, not the conversation.
      this.showConnectionBanner("lost");
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
        this.appwireConnectionLostUnsubscribe = window.SerfAppwire.onConnectionLost(() => {
          this.clearAppwireStream();
          this.statusUpdateSeq++;
          this.updateThreadState("closed");
          // Transport failure is Serf's fault, not the agent's: it must NOT
          // pollute the conversation. Surface it as a chrome reconnect banner
          // (mockup #15 Alt A) — amber while recovering — and disable send
          // (we disable rather than fake a queue). The reconnect schedule
          // clears or escalates the banner depending on the outcome.
          this.showConnectionBanner("reconnecting");
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
          // The stream is live again: a reconnect succeeded, so retire any
          // chrome reconnect banner and re-enable the composer.
          this.clearConnectionBanner();
          while (pendingNotifications.length > 0) {
            const [method, params] = pendingNotifications.shift();
            if (notificationCoveredByHydration(method, params)) continue;
            const replayParams = notificationForHydrationReplay(method, params);
            if (notificationMatches(replayParams)) deliverNotification(method, replayParams);
          }
        })
        .catch((err) => {
          if (this.sessionId !== sessionId || this.conversation !== conversation) return;
          // A connect attempt failed. This is transport, not the agent — keep
          // it out of the transcript and escalate the chrome banner to red
          // "Connection lost", then keep retrying in the background.
          this.clearAppwireStream();
          this.showConnectionBanner("lost");
          this.scheduleAppwireReconnect();
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

    // ── Transport reconnect banner (mockup #15 Alt A, case 1) ────────────────
    // A daemon/appwire drop is Serf losing the agent — Serf's fault, not the
    // agent's — so it must live in the workspace chrome (a docked bar under the
    // top bar), never in the conversation. AMBER "Reconnecting…" while the
    // socket is down (recovering, not broken); RED "Connection lost" once a
    // reconnect attempt fails. Cleared on a successful reconnect. Each state is
    // glyph-paired so it reads without color (colorblind-safe). Send is disabled
    // while disconnected (via updateThreadState("closed")) — we disable rather
    // than fake a queue, since the composer has no real buffer-and-replay path.
    connectionBannerEl() {
      let banner = document.getElementById("connection-banner");
      if (!banner) {
        banner = document.createElement("div");
        banner.id = "connection-banner";
        banner.className = "connection-banner";
        banner.setAttribute("role", "status");
        const top = document.querySelector(".workspace-header") || document.body.firstChild;
        if (top && top.parentNode) top.parentNode.insertBefore(banner, top);
        else document.body.insertBefore(banner, document.body.firstChild);
        document.body.classList.add("has-connection-banner");
      }
      return banner;
    },

    showConnectionBanner(level) {
      const banner = this.connectionBannerEl();
      banner.classList.remove("reconnecting", "lost");
      if (level === "lost") {
        banner.classList.add("lost");
        banner.innerHTML = '<span class="connection-banner-glyph" aria-hidden="true">⚠</span>' +
          '<span class="connection-banner-msg">Connection lost</span>' +
          '<span class="connection-banner-sub">retrying… — the agent keeps running on the daemon</span>';
      } else {
        banner.classList.add("reconnecting");
        banner.innerHTML = '<span class="connection-banner-glyph" aria-hidden="true">⟳</span>' +
          '<span class="connection-banner-msg">Reconnecting…</span>' +
          '<span class="connection-banner-sub">the agent keeps running on the daemon</span>';
      }
    },

    clearConnectionBanner() {
      const banner = document.getElementById("connection-banner");
      if (banner && banner.parentNode) banner.parentNode.removeChild(banner);
      document.body.classList.remove("has-connection-banner");
    },

    resetTranscriptReplay() {
      if (!this.conversation) return;
      this.conversation.innerHTML = "";
      this.activeMessages.clear();
      this.activeTools.clear();
      this.activeJobs.clear();
      this.suppressedToolCalls.clear();
      this.pendingTaskCalls.clear();
      this.currentMessageId = null;
      this.userTurnIndex = 0;
      this.entryIndex = 0;
      this.cheapToolCluster = null;
      this.subagentModule = null;
    },

    refreshTranscriptStatusVisibility() {
      if (!this.conversation || !this.sessionId) return false;
      if (!window.SerfAppwire || typeof window.SerfAppwire.readThread !== "function" || typeof window.SerfAppwire.eventsFromThread !== "function") {
        return false;
      }
      const sessionId = this.sessionId;
      const conversation = this.conversation;
      window.SerfAppwire.readThread(sessionId, true, false, false)
        .then((resp) => {
          if (this.sessionId !== sessionId || this.conversation !== conversation) return;
          const thread = (resp && resp.thread) || {};
          this.resetTranscriptReplay();
          this.appwireThreadId = thread.id || this.appwireThreadId;
          this.appwireRef = (thread.serf && thread.serf.ref) || (this.appwireThreadId ? window.SerfAppwire.refForSession(this.appwireThreadId) : null);
          if (typeof window.SerfAppwire.activeTurnIDFromThread === "function") {
            this.setActiveTurnId(window.SerfAppwire.activeTurnIDFromThread(thread));
          }
          for (const [kind, data] of window.SerfAppwire.eventsFromThread(thread)) {
            this.handleData(kind, data);
          }
          this.appwireHydrated = true;
        })
        .catch(() => {});
      return true;
    },

    handleData(kind, data) {
      this.handle(kind, { data: JSON.stringify(data || {}) });
    },

    handle(kind, ev) {
      // Every frame from the daemon (incl. reasoning) resets the liveness clock.
      this.lastFrameAt = Date.now();
      // Buffer events until the cold-load /tasks fetch resolves, so the
      // first batch of system-lines renders with task descriptions and
      // never shows the #N → title flash on resume.
      if (!this.descriptionsReady && this.eventBuffer) {
        this.eventBuffer.push([kind, ev]);
        return;
      }
      // Measure before the DOM mutation: only stick to the bottom if the reader
      // is already there, so streaming frames don't yank them off history.
      const stick = this.isNearBottom();
      // Count rendered transcript entries before the switch so we can tell
      // whether this frame actually appended visible content (suppressed/no-op
      // events leave the count unchanged) — the trigger for the new-content pill.
      const entriesBefore = this.conversation ? this.conversation.children.length : 0;
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
          // A new turn supersedes any prior blocking question.
          this.clearAgentQuestion();
          this.setActiveTurnId(data.turnId || "");
          // TURN_STARTED is a real signal but not yet a first frame: the
          // cold-start skeleton stands until model output arrives. If a turn
          // begins with no echo (e.g. a second tab), the welcome dissolves now.
          this.dissolveWelcome();
          this.showColdStartSkeleton();
          break;
        case "TURN_COMPLETED":
          this.finalizeReasoning();
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
            this.reasoningEl = null;
            this.activeMessages.clear();
            this.activeTools.clear();
            this.activeJobs.clear();
            this.subagentModule = null;
            this.suppressedToolCalls.clear();
            this.pendingTaskCalls.clear();
            taskDescriptions.clear();
            taskDetails.clear();
            this.lastCurrentTaskId = null;
            this.userTurnIndex = 0;
            this.entryIndex = 0;
            this.clearAgentQuestion();
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
            if (typeof data.capabilities.interrupt === "boolean") this.liveInterruptCap = data.capabilities.interrupt;
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
          // A user message answers any pending blocking question; tear down the
          // live "awaiting your answer" affordances (the in-flow frame stays).
          this.clearAgentQuestion();
          this.lastUserText = data.text || "";
          this.lastSubmittedTurn = this.retryPayload(data.text || "", data.images || []);
          this.dissolveWelcome();
          if (this.promoteLocalUserMessage(data)) { this.showColdStartSkeleton(); break; }
          this.userTurnIndex++;
          if (typeof data.turn === "number" && data.turn > 0) {
            this.entryIndex = data.turn;
          } else {
            this.entryIndex++;
          }
          const userWrap = this.appendUserMessage(data.text || "", this.entryIndex, data.images || []);
          if (data.turnId) userWrap.dataset.turnId = String(data.turnId);
          this.showColdStartSkeleton();
          break;
        case "ASSISTANT_TEXT_START":
          this.finalizeReasoning();
          this.entryIndex++;
          this.beginAssistantMessage();
          break;
        case "ASSISTANT_TEXT_DELTA":
          this.appendAssistantDelta(data.delta || "");
          break;
        case "ASSISTANT_TEXT_END":
          this.finalizeAssistantMessage(data);
          break;
        case "ASSISTANT_TEXT_RESET":
          this.resetAssistantMessage();
          break;
        case "REASONING_START":
          this.beginReasoning();
          break;
        case "REASONING_DELTA":
          this.appendReasoningDelta(data.delta || "");
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
        case "SYSTEM_MESSAGE":
          if (data && data.raw && data.raw.compaction) {
            this.appendContextCompaction(data.raw.compaction);
            break;
          }
          if (!shouldRenderSystemMessage(data)) break;
          this.appendSystemMessage(data);
          break;
        case "SYSTEM_LINE":
          if (!shouldRenderSystemLine(data.text || data.message || "")) break;
          this.appendSystemLine(data.text || data.message || "");
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
        case "JOB_STARTED":
          this.beginJobRef(data);
          break;
        case "JOB_FINISHED":
          this.finalizeJobRef(data);
          break;
        case "COMMUNICATE":
          // Already rendered via TOOL_CALL_START's arguments_json.message.
          // The daemon emits both events for the same content; drop this one
          // to avoid duplicates.
          break;
      }
      if (stick) {
        this.scrollToBottom();
      } else {
        // Reader is up in history: if this frame rendered new entries, surface
        // the floating "↓ N new" affordance instead of yanking the viewport.
        const added = this.conversation ? this.conversation.children.length - entriesBefore : 0;
        if (added > 0) this.noteNewContent(added);
      }
    },

    // appendContextCompaction renders the context-compaction lifecycle event
    // (mockup #17 Alt A): a quiet neutral system one-liner that EXPANDS to show
    // the real before/after math (tokens before→after, turns before→after,
    // layer). It is a DONE event — never a silent rug-pull — and shows only the
    // real numbers carried in the projection (no invented "auto-compaction"
    // language). `c` is the structured compaction payload from raw.compaction.
    appendContextCompaction(c) {
      c = c || {};
      this.endCheapCluster();
      this.closeSubagentModule();

      const turnsBefore = Number(c.turns_before || c.turnsBefore || 0);
      const turnsAfter = Number(c.turns_after || c.turnsAfter || 0);
      const tokBefore = Number(c.est_tokens_before || c.estTokensBefore || 0);
      const tokAfter = Number(c.est_tokens_after || c.estTokensAfter || 0);
      const layer = String(c.layer || "");

      const el = document.createElement("details");
      el.className = "system-line context-compaction-line";

      const summary = document.createElement("summary");
      const glyph = document.createElement("span");
      glyph.className = "context-compaction-glyph";
      glyph.textContent = "⊟";
      const label = document.createElement("span");
      label.className = "context-compaction-label";
      label.textContent = "Context compacted";
      summary.append(glyph, label);
      // Quiet inline delta on the summary, only from real numbers.
      const deltaBits = [];
      if (turnsBefore || turnsAfter) deltaBits.push(turnsBefore + " turns → summary");
      if (tokBefore && tokAfter) {
        deltaBits.push(formatTokenCount(tokBefore) + " → " + formatTokenCount(tokAfter));
      }
      if (deltaBits.length) {
        const delta = document.createElement("span");
        delta.className = "context-compaction-delta";
        delta.textContent = " · " + deltaBits.join(" · ");
        summary.append(delta);
      }

      const body = document.createElement("div");
      body.className = "context-compaction-body";
      const rows = [];
      if (tokBefore || tokAfter) {
        rows.push("Estimated tokens: " + formatTokenCount(tokBefore) + " → " + formatTokenCount(tokAfter));
      }
      if (turnsBefore || turnsAfter) {
        rows.push("Turns: " + turnsBefore + " → " + turnsAfter);
      }
      if (layer) rows.push("Layer: " + layer);
      rows.push("Earlier turns were summarized to make room. They remain in this transcript above, but are no longer in the model's context window.");
      for (const row of rows) {
        const line = document.createElement("div");
        line.className = "context-compaction-stat";
        line.textContent = row;
        body.appendChild(line);
      }

      el.append(summary, body);
      this.conversation.appendChild(el);
    },

    // appendSystemMessage renders a lifecycle event (skill activated, plugin
    // loaded, tools, prompt) as a quiet, dim one-liner — never divider-weight,
    // and without the meaningless "N chars" payload count (mockup #7 alt A).
    // The full payload stays available behind the disclosure. Adjacent events
    // coalesce into one collapsed line once a run reaches 3 (alt B).
    appendSystemMessage(data) {
      data = data || {};
      const text = String(data.text || "");
      if (!text.trim()) return;
      this.endCheapCluster();
      this.closeSubagentModule();
      const title = systemMessageDisplayTitle(data.title || "System");
      const el = document.createElement("details");
      el.className = "steering system-message";
      el.dataset.systemTitle = title;

      const summary = document.createElement("summary");
      const verb = document.createElement("span");
      verb.className = "steering-verb";
      verb.textContent = title;
      summary.append(verb);

      const body = document.createElement("div");
      body.className = "steering-body";
      body.textContent = text;
      el.append(summary, body);

      const run = this.ensureSystemRun();
      run.querySelector(".system-run-body").appendChild(el);
      this.coalesceSystemRun(run);
    },

    // ensureSystemRun returns the open lifecycle run if the last transcript
    // entry is one (adjacency), else starts a new one. Self-resetting: any
    // other entry type appended in between makes the last child not a run, so
    // the next lifecycle event opens a fresh run (mockup #7 alt B).
    ensureSystemRun() {
      const last = this.conversation.lastElementChild;
      if (last && last.classList.contains("system-run")) return last;
      const run = document.createElement("div");
      run.className = "system-run";
      const toggle = document.createElement("button");
      toggle.type = "button";
      toggle.className = "system-run-toggle";
      bindDisclosureToggle(toggle, run);
      const inner = document.createElement("div");
      inner.className = "system-run-body";
      run.append(toggle, inner);
      this.conversation.appendChild(run);
      return run;
    },

    // coalesceSystemRun folds a run of 3+ adjacent lifecycle events into one
    // quiet line ("N system events · <first> + N more"); the individual blocks
    // hide until the run is expanded.
    coalesceSystemRun(run) {
      const blocks = run.querySelectorAll(".system-run-body > .system-message");
      const n = blocks.length;
      const toggle = run.querySelector(".system-run-toggle");
      if (n < 3) {
        run.classList.remove("coalesced");
        return;
      }
      run.classList.add("coalesced");
      const first = blocks[0] && blocks[0].dataset.systemTitle || "system event";
      const more = n - 1;
      toggle.textContent = "✦ " + n + " system events · " + first + (more > 0 ? " + " + more + " more" : "");
    },

    appendSystemLine(text) {
      text = String(text || "").trim();
      if (!text) return;
      this.endCheapCluster();
      this.closeSubagentModule();
      const line = document.createElement("div");
      line.className = "system-line";
      line.textContent = text;
      this.conversation.appendChild(line);
    },

    appendUserMessage(text, entryIdx, images) {
      this.endCheapCluster();
      this.closeSubagentModule();
      const wrap = document.createElement("div");
      wrap.className = "user-message";
      wrap.dataset.entryIdx = String(entryIdx || "");
      wrap.dataset.userTurn = String(this.userTurnIndex || "");
      // Quiet "You" tag anchors the demoted user prompt (design-system #2:
      // never emphasize the user's own message). It is a sibling of the pill
      // so .pill.textContent stays the clean prompt text.
      const tag = document.createElement("span");
      tag.className = "user-message-tag";
      tag.textContent = "You";
      const pill = document.createElement("div");
      pill.className = "pill";
      // Thumbnails first so they sit above the prompt text inside the pill.
      // ONE image keeps the single neutral card; MULTIPLE images lay out as a
      // contact-sheet grid inside ONE neutral card (mockup #20 Alt B) — never
      // a card-of-cards. Either way, opening any thumbnail pages through the
      // whole message's set in the shared lightbox (←/→, Esc).
      if (Array.isArray(images) && images.length > 0) {
        const resolved = [];
        for (const img of images) {
          const src = this.imageSrc(img);
          if (!src) continue;
          resolved.push({ src, name: (img && img.name) || "" });
        }
        if (resolved.length === 1) {
          const gallery = document.createElement("div");
          gallery.className = "user-message-images";
          gallery.appendChild(this.buildSingleImageCard(resolved, 0));
          pill.appendChild(gallery);
        } else if (resolved.length > 1) {
          pill.appendChild(this.buildImageSheet(resolved));
        }
      }
      if (text) {
        const t = document.createElement("div");
        t.className = "user-message-text";
        t.textContent = text;
        pill.appendChild(t);
      }
      const actions = document.createElement("div");
      actions.className = "user-message-actions";
      const copy = document.createElement("button");
      copy.type = "button";
      copy.className = "action copy"; copy.textContent = "copy";
      copy.onclick = () => navigator.clipboard.writeText(text);
      const edit = document.createElement("button");
      edit.type = "button";
      edit.className = "action edit"; edit.textContent = "✎ edit";
      edit.onclick = () => this.startEdit(wrap, pill, text);
      actions.appendChild(copy); actions.appendChild(edit);
      wrap.appendChild(tag); wrap.appendChild(pill); wrap.appendChild(actions);
      this.conversation.appendChild(wrap);
      return wrap;
    },

    // imageSrc resolves a user-message image descriptor to a renderable URL.
    //   Live USER_INPUT: bytes inline as base64 in img.data.
    //   Transcript USER_INPUT: bytes referenced by sha; fetched lazily from
    //     /s/<id>/images/<sha> so live payloads stay small.
    //   url: an already-resolvable URL.
    // Returns "" when the descriptor carries no usable source.
    imageSrc(img) {
      if (!img) return "";
      if (img.data) return "data:" + (img.media_type || "image/png") + ";base64," + img.data;
      if (img.sha) return "/s/" + encodeURIComponent(this.sessionId) + "/images/" + encodeURIComponent(img.sha);
      if (img.url) return img.url;
      return "";
    },

    // captionFilename attaches the filename (mono) to a caption row and, once
    // the thumbnail's natural dimensions are known, appends "· WxH" using the
    // REAL decoded pixel size. Dims are omitted until the image loads (and in
    // jsdom, which never decodes) — we never fabricate a size.
    captionFilename(caption, thumb, name) {
      const fn = document.createElement("span");
      fn.className = "user-image-filename";
      fn.textContent = name || "image";
      caption.appendChild(fn);
      const stampDims = () => {
        if (!thumb.naturalWidth || !thumb.naturalHeight) return;
        if (caption.querySelector(".user-image-dims")) return;
        const sep = document.createElement("span");
        sep.className = "user-image-cap-sep";
        sep.textContent = "·";
        const dims = document.createElement("span");
        dims.className = "user-image-dims";
        dims.textContent = thumb.naturalWidth + "×" + thumb.naturalHeight;
        caption.append(sep, dims);
      };
      if (thumb.complete) stampDims();
      thumb.addEventListener("load", stampDims);
    },

    // buildSingleImageCard renders the one-image neutral card (today's path).
    // `resolved` is the message's full image set; `idx` is this card's index,
    // so opening it pages the shared lightbox across the set.
    buildSingleImageCard(resolved, idx) {
      const m = resolved[idx];
      const card = document.createElement("button");
      card.type = "button";
      card.className = "user-image-card";
      card.title = "click to enlarge";
      const thumb = document.createElement("img");
      thumb.className = "user-image-thumb";
      thumb.src = m.src;
      if (m.name) thumb.alt = m.name;
      card.appendChild(thumb);
      if (m.name) {
        const name = document.createElement("span");
        name.className = "user-image-name";
        name.textContent = m.name;
        card.appendChild(name);
      }
      card.onclick = (e) => { e.stopPropagation(); openImageLightboxSet(resolved, idx); };
      return this.attachImageOpenBeside(card, m);
    },

    // FILE_OPEN_BESIDE_TOOLS lists the tools whose args reference a single repo
    // file we can open in a read-only document pane via /doc/file. Multi-target
    // tools (apply_patch) and directory/pattern tools (grep, ls) are excluded.
    fileOpenBesideArg(tool, args) {
      if (!args) return "";
      switch (tool) {
        case "read_file":
        case "edit_file":
        case "write_file":
          return args.file_path || args.path || "";
        default:
          return "";
      }
    },

    // cwdRelative returns p expressed relative to the session cwd, or "" when p
    // is not inside the cwd. The /doc/file route only serves files contained by
    // the session cwd, so an out-of-cwd path earns no affordance.
    cwdRelative(p) {
      if (!p || !this.cwd) return "";
      const prefix = this.cwd.endsWith("/") ? this.cwd : this.cwd + "/";
      if (p === this.cwd) return "";
      if (p.indexOf(prefix) === 0) return p.slice(prefix.length);
      return "";
    },

    // attachFileOpenBeside adds the ⇲ "open beside" control to a file-
    // referencing tool card. The control opens /doc/file for the referenced
    // file (path relativized against the session cwd) in a side pane. Skipped
    // when the tool doesn't reference a single in-cwd file.
    attachFileOpenBeside(el, tool, args) {
      if (!el || el.querySelector(".open-beside-btn")) return;
      const abs = this.fileOpenBesideArg(tool, args);
      const rel = this.cwdRelative(abs);
      if (!rel) return;
      const sessionId = this.sessionId;
      const href = "/doc/file?session=" + encodeURIComponent(sessionId) +
        "&path=" + encodeURIComponent(rel);
      const title = rel.split("/").pop() || rel;
      const beside = this.makeOpenBesideButton("open file beside", function () {
        return { href: href, title: title };
      });
      if (beside) el.appendChild(beside);
    },

    // attachImageOpenBeside wraps an image card so a ⇲ "open beside" control can
    // sit beside it. The control is a SIBLING of the card, not a child: the
    // card is a <button>, and nesting an interactive control inside a button is
    // invalid. It returns the element to insert into the DOM — a positioned
    // wrapper when the affordance applies, or the bare card otherwise.
    //
    // The affordance only applies when the image has a stable same-origin URL
    // (a sha-addressed /s/<id>/images/<sha> path). data: URLs are skipped: they
    // have no stable URL and a data: iframe src is blocked by the same-origin
    // CSP, so an "open beside" pane would render blank.
    attachImageOpenBeside(card, m) {
      if (!m || typeof m.src !== "string" || m.src.charAt(0) !== "/") return card;
      var beside = this.makeOpenBesideButton("open image beside", function () {
        return { href: m.src, title: m.name || "image" };
      });
      if (!beside) return card;
      var wrap = document.createElement("div");
      wrap.className = "image-beside-wrap";
      wrap.appendChild(card);
      wrap.appendChild(beside);
      return wrap;
    },

    // buildImageSheet lays a multi-image set as a contact-sheet grid inside ONE
    // neutral card (mockup #20 Alt B). Each cell is a thumbnail + a per-cell
    // caption (filename · dims). Opening any cell pages the shared lightbox
    // across the whole set. Provenance grouping (Alt D) is omitted — there is
    // no backend signal for image origin.
    buildImageSheet(resolved) {
      const sheet = document.createElement("div");
      sheet.className = "user-image-sheet";
      const head = document.createElement("div");
      head.className = "user-image-sheet-head";
      head.textContent = resolved.length + " images";
      const grid = document.createElement("div");
      grid.className = "user-image-grid";
      resolved.forEach((m, idx) => {
        const cell = document.createElement("button");
        cell.type = "button";
        cell.className = "user-image-cell";
        cell.title = "click to enlarge";
        const thumb = document.createElement("img");
        thumb.className = "user-image-thumb";
        thumb.src = m.src;
        if (m.name) thumb.alt = m.name;
        const caption = document.createElement("span");
        caption.className = "user-image-caption";
        this.captionFilename(caption, thumb, m.name);
        cell.append(thumb, caption);
        cell.onclick = (e) => { e.stopPropagation(); openImageLightboxSet(resolved, idx); };
        grid.appendChild(this.attachImageOpenBeside(cell, m));
      });
      sheet.append(head, grid);
      return sheet;
    },

    // conversationHasContent reports whether anything substantive has rendered
    // into the transcript. The cold-start welcome and skeleton are scaffolding,
    // not content, so they don't count.
    conversationHasContent() {
      if (!this.conversation) return false;
      for (const child of this.conversation.children) {
        if (child.classList.contains("cold-start-welcome")) continue;
        if (child.classList.contains("cold-start-skeleton")) continue;
        return true;
      }
      return false;
    },

    // maybeShowWelcome renders the crafted empty-session welcome (mockup #21
    // Alt C) when a hydrated session has no transcript content: a one-line
    // orientation plus a few example prompts that prefill the composer on
    // click. It dissolves on first send and never lingers for active sessions.
    maybeShowWelcome() {
      if (!this.conversation) return;
      if (this.conversationHasContent()) return;
      if (this.conversation.querySelector(".cold-start-welcome")) return;

      const pane = document.createElement("div");
      pane.className = "cold-start-welcome";

      const intro = document.createElement("div");
      intro.className = "cold-start-intro";
      intro.textContent = "Describe a task and the agent gets to work — you'll watch it think, run tools, and spawn subagents in real time.";
      pane.appendChild(intro);

      const tryLabel = document.createElement("div");
      tryLabel.className = "cold-start-try";
      tryLabel.textContent = "Try";
      pane.appendChild(tryLabel);

      const list = document.createElement("div");
      list.className = "cold-start-examples";
      // Example-prompt copy is UI text we author (not a fabricated signal).
      const examples = [
        "Find and fix the root cause of a flaky test",
        "Audit error handling across this package",
        "Explain how a request flows from router to handler",
      ];
      for (const prompt of examples) {
        const btn = document.createElement("button");
        btn.type = "button";
        btn.className = "cold-start-example";
        btn.dataset.prompt = prompt;
        const arr = document.createElement("span");
        arr.className = "cold-start-example-arrow";
        arr.textContent = "→";
        const txt = document.createElement("span");
        txt.textContent = prompt;
        btn.append(arr, txt);
        btn.addEventListener("click", () => this.prefillComposer(prompt));
        list.appendChild(btn);
      }
      pane.appendChild(list);
      this.conversation.appendChild(pane);
    },

    // prefillComposer drops example-prompt text into the composer and focuses
    // it, so the user can edit before sending.
    prefillComposer(text) {
      const ta = document.querySelector("form[data-input-form] .message-input")
        || document.querySelector(".message-input");
      if (!ta) return;
      ta.value = text;
      ta.dispatchEvent(new Event("input", { bubbles: true }));
      ta.focus();
    },

    // dissolveWelcome removes the welcome pane the instant a turn begins, so it
    // never lingers as clutter behind the live transcript.
    dissolveWelcome() {
      if (!this.conversation) return;
      const pane = this.conversation.querySelector(".cold-start-welcome");
      if (pane) pane.remove();
    },

    // showColdStartSkeleton fills the send→first-frame gap (mockup #21 Alt A+B)
    // with a faint skeleton placeholder turn and a calm neutral "starting…"
    // line carrying the single sanctioned breathing dot — so the gap reads
    // "loading," never "broken." Shown only in the real gap: when the last
    // transcript entry is the just-sent user message and no model output has
    // landed yet. Idempotent. No invented stage narration — only real signals
    // (the user's send / TURN_STARTED) drive it; ASSISTANT_TEXT_START clears it.
    showColdStartSkeleton() {
      if (!this.conversation) return;
      if (this.conversation.querySelector(".cold-start-skeleton")) return;
      const last = this.conversation.lastElementChild;
      if (!last || !last.classList.contains("user-message")) return;

      const skel = document.createElement("div");
      skel.className = "cold-start-skeleton";
      skel.setAttribute("data-loading", "");
      const ghost = document.createElement("div");
      ghost.className = "cold-start-skeleton-lines";
      ghost.setAttribute("aria-hidden", "true");
      for (const w of ["skeleton-line-80", "skeleton-line-70", "skeleton-line-60"]) {
        const line = document.createElement("span");
        line.className = "skeleton skeleton-line " + w;
        ghost.appendChild(line);
      }
      const starting = document.createElement("div");
      starting.className = "cold-start-starting";
      const dot = document.createElement("span");
      dot.className = "cold-start-dot";
      starting.append(dot, document.createTextNode("starting…"));
      skel.append(ghost, starting);
      this.conversation.appendChild(skel);
    },

    // removeColdStartSkeleton tears the cold-start placeholder down the instant
    // the first real model frame arrives (text, reasoning, or a tool call).
    removeColdStartSkeleton() {
      if (!this.conversation) return;
      const skel = this.conversation.querySelector(".cold-start-skeleton");
      if (skel) skel.remove();
    },

    appendLocalUserMessage(text, images, turnId, previousUserCount) {
      if (this.userMessageCount() > previousUserCount) return;
      this.lastUserText = text || "";
      this.lastSubmittedTurn = this.retryPayload(text || "", images || []);
      this.userTurnIndex++;
      this.entryIndex++;
      // Cold start (mockup #21): the welcome dissolves on first send and a
      // faint skeleton + calm "starting…" stands where the first frame lands.
      this.dissolveWelcome();
      const wrap = this.appendUserMessage(text || "", this.entryIndex, images || []);
      wrap.dataset.localEcho = "true";
      wrap.dataset.localImageCount = String(Array.isArray(images) ? images.length : 0);
      if (turnId) wrap.dataset.turnId = String(turnId);
      this.showColdStartSkeleton();
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
      this.removeColdStartSkeleton();
      this.endCheapCluster();
      this.closeSubagentModule();
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
      if (!String(text || "").trim()) return null;
      this.removeColdStartSkeleton();
      this.endCheapCluster();
      this.closeSubagentModule();
      const el = document.createElement("div");
      el.className = "assistant-message";
      try { el.innerHTML = window.marked.parse(text); }
      catch (e) { el.textContent = text; }
      this.conversation.appendChild(el);
      return el;
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

    // resetAssistantMessage discards the in-progress assistant message so a
    // retried model call's output replaces, rather than appends to, the partial
    // that was already streamed.
    resetAssistantMessage() {
      const id = this.currentMessageId;
      if (!id) return;
      const m = this.activeMessages.get(id);
      this.activeMessages.delete(id);
      this.currentMessageId = null;
      if (m && m.el && m.el.parentNode) m.el.parentNode.removeChild(m.el);
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

    // markAgentQuestion frames an agent message as the blocking "needs-you"
    // question (mockup #16 Alt A): an amber ◆ header above the literal question
    // text, the one containment device that pulls the eye. The composer is the
    // reply affordance — clicking the frame focuses it. The framed element is
    // recorded as the live question target so the docked bar (Alt C) can scroll
    // to it. Idempotent: re-framing the same element only refreshes the dock.
    markAgentQuestion(el) {
      if (!el) return;
      if (!el.classList.contains("agent-question")) {
        el.classList.add("agent-question");
        const head = document.createElement("div");
        head.className = "agent-question-head";
        head.setAttribute("data-agent-question-head", "");
        const glyph = document.createElement("span");
        glyph.className = "agent-question-glyph";
        glyph.textContent = "◆";
        const label = document.createElement("span");
        label.className = "agent-question-label";
        label.textContent = "Needs you";
        head.appendChild(glyph);
        head.appendChild(label);
        el.insertBefore(head, el.firstChild);
        el.addEventListener("click", () => this.focusComposer());
      }
      this.agentQuestionEl = el;
      this.renderNeedsYouDock();
    },

    // Live thinking: the quietest transcript entry (design-system §2.5).
    // Reserved-slot collapse (mockup #5 alt A): while streaming the block sits
    // in a fixed-height slot showing a one-line teleprompter tail of the newest
    // reasoning, so the prose below never reflows token-by-token. It collapses
    // to "Thought for Ns" plus a duration-ranked noun-phrase gist (alt D).
    // Click to expand the full reasoning body. One per turn; the projector
    // emits a single reasoning item per turn.
    beginReasoning() {
      if (this.reasoningEl) return;
      this.removeColdStartSkeleton();
      this.endCheapCluster();
      this.closeSubagentModule();
      const el = document.createElement("button");
      el.type = "button";
      el.className = "think streaming";
      const label = document.createElement("span");
      label.className = "think-label";
      label.textContent = "✦ Thinking…";
      const pv = document.createElement("span");
      pv.className = "pv";
      const body = document.createElement("span");
      body.className = "think-body";
      el.appendChild(label);
      el.appendChild(pv);
      el.appendChild(body);
      bindDisclosureToggle(el, el);
      this.conversation.appendChild(el);
      this.reasoningEl = el;
      this.reasoningBuf = "";
      this.reasoningStartedAt = Date.now();
    },

    appendReasoningDelta(delta) {
      if (!this.reasoningEl) this.beginReasoning();
      this.reasoningBuf += delta || "";
      const body = this.reasoningEl.querySelector(".think-body");
      const pv = this.reasoningEl.querySelector(".pv");
      if (body) body.textContent = this.reasoningBuf;
      // Teleprompter tail: the trailing fragment of the live reasoning. The
      // slot is one line tall and the tail reveals right-to-left (CSS), so its
      // height never changes as tokens arrive.
      if (pv) pv.textContent = clip(this.reasoningBuf.replace(/\s+/g, " ").trim(), 200);
    },

    // finalizeReasoning collapses the in-progress thought to a one-line summary
    // (or drops it if nothing streamed). Called when the assistant starts its
    // answer or the turn completes. The collapsed line carries a duration tier
    // and a noun-phrase gist so a stack of thoughts is scannable by effort.
    finalizeReasoning() {
      const el = this.reasoningEl;
      if (!el) return;
      this.reasoningEl = null;
      if (!String(this.reasoningBuf || "").trim()) {
        if (el.parentNode) el.parentNode.removeChild(el);
        return;
      }
      el.classList.remove("streaming");
      const secs = Math.max(1, Math.round((Date.now() - (this.reasoningStartedAt || Date.now())) / 1000));
      el.classList.add("think-tier-" + reasoningTier(secs));
      const label = el.querySelector(".think-label");
      if (label) label.innerHTML = "✦ Thought for <span class=\"num\">" + secs + "s</span>";
      const pv = el.querySelector(".pv");
      const gist = reasoningGist(this.reasoningBuf);
      if (pv) pv.textContent = gist ? "— " + gist : "";
    },

    // ensureLivenessEl keeps a single liveness line just below the transcript
    // (a sibling of #conversation, so transcript appends never disturb it).
    ensureLivenessEl() {
      if (!this.conversation || !this.conversation.parentNode) { this.livenessEl = null; return; }
      let el = this.conversation.parentNode.querySelector(".liveness");
      if (!el) {
        el = document.createElement("div");
        el.className = "liveness";
        el.hidden = true;
        this.conversation.parentNode.insertBefore(el, this.conversation.nextSibling);
      }
      this.livenessEl = el;
    },

    startLivenessTimer() {
      if (this.livenessTimer) clearInterval(this.livenessTimer);
      this.livenessTimer = setInterval(() => this.refreshLiveness(), LIVENESS_TICK_MS);
    },

    // refreshLiveness runs the honest calm→concern model (mockup #13) while an
    // actively working session has gone quiet:
    //   gap < QUIET            calm — no line, the dot keeps breathing.
    //   QUIET ≤ gap < STALL    calm-quiet — a NEUTRAL "quiet ~Nm" line with a
    //                          coarse bucket (not a rising counter); the dot
    //                          keeps breathing, because a short silence is
    //                          expected, not a stall.
    //   gap ≥ STALL            concern — amber + a glyph + "may be stalled";
    //                          the dot stops breathing, because we can no longer
    //                          honestly claim the agent is alive. Escalation
    //                          REMOVES the alive cue rather than adding a loop.
    refreshLiveness() {
      const el = this.livenessEl;
      if (!el) return;
      const gap = this.lastFrameAt ? (Date.now() - this.lastFrameAt) : 0;
      let level = "none";
      if (this.state === "active") {
        if (gap >= LIVENESS_STALL_MS) level = "concern";
        else if (gap >= LIVENESS_QUIET_MS) level = "quiet";
      }
      if (level === "quiet") {
        el.dataset.level = "quiet";
        el.textContent = "working · quiet " + this.formatLivenessQuiet(gap);
        el.hidden = false;
      } else if (level === "concern") {
        el.dataset.level = "concern";
        el.textContent = "";
        const glyph = document.createElement("span");
        glyph.className = "liveness-glyph";
        glyph.textContent = "!";
        el.appendChild(glyph);
        el.appendChild(document.createTextNode(
          "no updates for " + this.formatLivenessGap(gap) + " — may be stalled"));
        el.hidden = false;
      } else {
        el.hidden = true;
        el.textContent = "";
        el.removeAttribute("data-level");
      }
      if (level !== this.livenessLevel) {
        this.livenessLevel = level;
        // Only the concern band flags the conversation as stalled and drops the
        // reassuring pulse; the calm-quiet band leaves both alone.
        if (this.conversation) {
          if (level === "concern") this.conversation.dataset.stalled = "true";
          else this.conversation.removeAttribute("data-stalled");
        }
        applyStatusDotPulse(document);
      }
    },

    formatLivenessGap(ms) {
      const secs = Math.round(ms / 1000);
      if (secs < 60) return secs + "s";
      const m = Math.floor(secs / 60);
      const s = secs % 60;
      return m + "m" + (s ? " " + s + "s" : "");
    },

    // formatLivenessQuiet rounds a calm-quiet gap to a coarse bucket so the line
    // shows a stable word (~30s, ~1m, ~2m) instead of a per-second rising
    // counter. Buckets: 20–45s → ~30s, 45–90s → ~1m, 90s–STALL → ~2m.
    formatLivenessQuiet(ms) {
      const secs = ms / 1000;
      if (secs < 45) return "~30s";
      if (secs < 90) return "~1m";
      return "~2m";
    },

    beginToolCall(data) {
      const callId = data.call_id || ("tool-" + Math.random().toString(36).slice(2, 9));
      const existing = this.toolStateFor(data);
      if (existing) {
        this.rememberToolAlias(existing, callId);
        this.rememberToolAlias(existing, data.item_id);
        return;
      }
      this.removeColdStartSkeleton();
      const tool = data.tool_name || "?";
      const renderer = toolRendererFor(tool);
      const args = parseArgs(data.arguments_json);
      const mode = renderer.mode || "default";

      let parent;
      if (mode === "cheap") {
        if (!this.cheapToolCluster) {
          const cluster = document.createElement("div");
          cluster.className = "tool-call-cluster";
          // In compact (pane) mode, mark the cluster so CSS can hide the body
          // even while it is still live — collapsing cheap-tool clusters to
          // their summary by default in the dense pane layout.
          if (document.body.classList.contains("pane-compact")) {
            cluster.dataset.compact = "";
          }
          // Collapsed summary line (mockup #6 alt A): hidden while the cluster
          // is live; revealed and filled in by endCheapCluster once the cluster
          // is behind us. The rows live in a body wrapper it can fold.
          const summary = document.createElement("button");
          summary.type = "button";
          summary.className = "tool-cluster-summary";
          bindDisclosureToggle(summary, cluster);
          const body = document.createElement("div");
          body.className = "tool-cluster-body";
          cluster.append(summary, body);
          cluster.clusterCalls = [];
          this.cheapToolCluster = cluster;
          this.conversation.appendChild(cluster);
        }
        parent = this.cheapToolCluster.querySelector(".tool-cluster-body");
      } else {
        this.endCheapCluster();
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
      const intent = toolIntent(data, args);
      if (intent) {
        const intentEl = document.createElement("div");
        intentEl.className = "tool-intent";
        intentEl.textContent = intent;
        el.appendChild(intentEl);
      }
      parent.appendChild(el);
      this.attachFileOpenBeside(el, tool, args);

      const startedAt = toolEventTime(data) || new Date();
      const state = { el, statusEl: status, resultEl: result, metaEl: meta, outputBuf: "", tool, args, renderer, body: null, caretEl: null, ids: [], startedAt, durationMs: toolDuration(data) };
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
        state.caretEl = caret;
      }
      // Cluster bookkeeping for the collapsed summary (mockup #6 alt A): record
      // each call's verb, target, and whether it mutated state so the summary
      // can lead with the consequential step.
      if (mode === "cheap" && this.cheapToolCluster && this.cheapToolCluster.clusterCalls) {
        this.cheapToolCluster.clusterCalls.push({
          verb: renderer.friendly || tool,
          target: target.textContent,
          mutating: renderer.mutating === true,
        });
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
      // Mark an errored row as a queryable attention anchor so the new-content
      // pill can find a buried failure below (or above) the fold (mockup #14).
      if (m.el) {
        if (ok) m.el.removeAttribute("data-attention");
        else m.el.dataset.attention = "error";
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
      // Silent success / no "expand reveals nothing" (mockup #6 alt D, #7 alt C):
      // a tool that returned no displayable body keeps just its "✓ verb target"
      // line — drop the caret so it never promises an expand over empty content.
      if (m.caretEl && this.toolBodyIsEmpty(m)) {
        m.caretEl.remove();
        m.caretEl = null;
        if (m.el) m.el.removeAttribute("data-expanded");
      }
      // A spawning tool (delegate) drops its own tool card and contributes a row
      // to the aggregated subagents module instead.
      if (m.renderer.subagentSpawn) {
        const info = m.renderer.subagentSpawn.call(this, m, data, out);
        if (info && info.jobId) {
          if (m.el && m.el.parentNode) m.el.parentNode.removeChild(m.el);
          this.upsertJobRef(info);
        }
      }
      // A reconciling tool (job_read_output / job_list / delegate_send)
      // flips any matching subagent rows from a non-JOB_FINISHED signal.
      if (m.renderer.subagentReconcile) {
        const infos = m.renderer.subagentReconcile.call(this, m, data, out);
        for (const info of infos || []) this.reconcileSubagent(info);
      }
      for (const id of m.ids || []) {
        this.activeTools.delete(id);
      }
    },

    // toolBodyIsEmpty reports whether a finished tool-call rendered any
    // displayable body content. A renderer signals "nothing to show" by hiding
    // its body wrapper (display:none) or leaving it textless; either way the
    // caret would be a lie, so we treat the row as a silent-success line.
    toolBodyIsEmpty(m) {
      if (!m || !m.el) return true;
      const bodies = m.el.querySelectorAll(".tool-body, .diff-body, .output-preview-body");
      for (const b of bodies) {
        if (b.style && b.style.display === "none") continue;
        if (b.textContent && b.textContent.trim()) return false;
        if (b.querySelector("img, svg, .tool-output-dropped")) return false;
      }
      return true;
    },

    // endCheapCluster finalizes the active recon cluster once it is behind us
    // (a non-cheap entry follows), folding it to a single "✓ N steps · targets"
    // summary that leads with the consequential (mutating) step (mockup #6 alt
    // A). Below 2 steps it stays a plain row list — a single read needs no
    // summary.
    endCheapCluster() {
      const cluster = this.cheapToolCluster;
      this.cheapToolCluster = null;
      if (!cluster) return;
      const calls = cluster.clusterCalls || [];
      const summary = cluster.querySelector(".tool-cluster-summary");
      if (calls.length < 2 || !summary) return;
      const mutators = calls.filter(c => c.mutating);
      const targets = [];
      const seen = new Set();
      for (const c of calls) {
        const t = String(c.target || "").trim();
        if (t && !seen.has(t)) { seen.add(t); targets.push(t); }
      }
      let lede;
      if (mutators.length) {
        // Consequential step leads; recon recedes to a trailing count.
        const recon = calls.length - mutators.length;
        lede = mutators.map(c => c.verb + " " + c.target).join(", ");
        if (recon > 0) lede += " · after " + recon + (recon === 1 ? " read" : " reads");
      } else {
        // Pure recon: a step count plus the touched targets (mockup #6 alt A's
        // lede-less fallback).
        const shown = targets.slice(0, 3).join(", ");
        const extra = targets.length > 3 ? " + " + (targets.length - 3) + " more" : "";
        lede = calls.length + " steps" + (shown ? " · " + shown + extra : "");
      }
      summary.textContent = "✓ " + lede;
      cluster.classList.add("done");
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
      // Middle-truncate only very long paths/commands. The tool row has room to
      // wrap, so keep substantially more context than the old 40-char cap.
      if (result.length > 96) {
        result = result.slice(0, 36) + "…" + result.slice(-52);
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

    // ===================== Subagents module =====================
    // Spawned subagents aggregate into ONE inline "Subagents (N)" module per
    // fan-out cluster (the box is the single containment device). Each row:
    // status glyph · name · last-action/result · duration · an always-visible
    // (dim) "view →" link into the subagent's transcript. The header tallies
    // running / done / failed; a failed child surfaces in red at the module
    // level — never folded into a "N done" count.
    //
    // The current module stays open across consecutive spawns and closes when
    // any other conversation entry is appended (closeSubagentModule, mirroring
    // cheapToolCluster), so the next fan-out gets its own module.

    // Threshold past which extra rows collapse behind "+N more · expand".
    SUBAGENT_VISIBLE_ROWS: 6,

    // classifyJobStatus maps a raw status to one of: running / done / failed /
    // unknown. A subagent that RAN FINE but found bad news is "done" (neutral) —
    // only a subagent or tool that itself failed-to-run is "failed" (red). The
    // "unknown" kind is never a raw wire status; it is the honest terminal
    // demotion a still-"running" row earns when the session goes dark without a
    // completion signal (mockup #8 alt A).
    classifyJobStatus(status) {
      const s = String(status || "").trim().toLowerCase();
      if (s === "failed" || s === "errored" || s === "error") return "failed";
      if (s === "completed" || s === "done" || s === "cancelled" || s === "stopped" || s === "succeeded") return "done";
      if (s === "unknown") return "unknown";
      return "running";
    },

    subagentGlyph(kind) {
      if (kind === "done") return "✓";
      if (kind === "failed") return "✕";
      if (kind === "unknown") return "?";
      return "⟳";
    },

    // Status kinds (running/done/failed/unknown) map to short glyph CSS classes
    // (run/done/err/unk) — the four-color, colorblind-safe glyph treatment.
    // "unknown" is neutral (not alarming, not live): a terminal "?" with the
    // breathing/spinner motion gone.
    subagentGlyphClass(kind) {
      if (kind === "done") return "done";
      if (kind === "failed") return "err";
      if (kind === "unknown") return "unk";
      return "run";
    },

    ensureSubagentModule() {
      if (this.subagentModule && this.subagentModule.parentNode === this.conversation) {
        return this.subagentModule;
      }
      // A new module is a transcript entry: end any open cheap-tool cluster so
      // later cheap reads start a fresh cluster below the module (preserving
      // chronological order).
      this.endCheapCluster();
      const mod = document.createElement("div");
      mod.className = "subs";
      mod.dataset.expanded = "false";
      mod.dataset.hasFailure = "false";

      const header = document.createElement("div");
      header.className = "subs-h";
      const title = document.createElement("span");
      title.className = "t";
      const tally = document.createElement("span");
      tally.className = "tally";
      header.appendChild(title);
      header.appendChild(tally);

      const rows = document.createElement("div");
      rows.className = "subs-rows";

      const more = document.createElement("button");
      more.type = "button";
      more.className = "subs-more";
      more.hidden = true;
      more.addEventListener("click", () => {
        mod.dataset.expanded = mod.dataset.expanded === "true" ? "false" : "true";
        this.refreshSubagentModule(mod);
      });

      mod.appendChild(header);
      mod.appendChild(rows);
      mod.appendChild(more);
      this.conversation.appendChild(mod);
      this.subagentModule = mod;
      return mod;
    },

    // closeSubagentModule ends the current fan-out so the next spawned
    // subagent starts a fresh module. Called wherever a non-subagent entry is
    // appended (alongside cheapToolCluster = null).
    closeSubagentModule() {
      this.subagentModule = null;
    },

    makeSubagentRow() {
      const row = document.createElement("button");
      row.type = "button";
      row.className = "sub-r";
      const glyph = document.createElement("span");
      glyph.className = "g run";
      glyph.textContent = "⟳";
      const name = document.createElement("span");
      name.className = "nm";
      const res = document.createElement("span");
      res.className = "res";
      const dur = document.createElement("span");
      dur.className = "dur num";
      const link = document.createElement("span");
      link.className = "lk";
      link.textContent = "view →";
      row.appendChild(glyph);
      row.appendChild(name);
      row.appendChild(res);
      row.appendChild(dur);
      row.appendChild(link);
      row.dataset.startedAt = String(Date.now());
      row.dataset.statusKind = "running";
      // Spawn order, preserved so worst-first sorting stays stable within a
      // severity band (mockup #8 alt B sorts by severity, not by recency).
      row.dataset.spawnIndex = String(this.subagentSpawnSeq = (this.subagentSpawnSeq || 0) + 1);
      return row;
    },

    // findSubagentRow locates a row by jobId. It prefers the current module but
    // falls back to a transcript-wide scan, because a reconciliation signal
    // (job_read_output etc.) can arrive after the module was closed by an
    // intervening entry — and we still want that stale "running" row to flip.
    findSubagentRow(jobId) {
      if (!jobId || !this.conversation) return null;
      for (const row of this.conversation.querySelectorAll(".sub-r")) {
        if (row.dataset.jobId === jobId) return row;
      }
      return null;
    },

    // upsertJobRef creates or updates a subagent row from a payload. Creating
    // one ensures the current module exists; the module header and overflow are
    // refreshed afterward. Returns the row element.
    upsertJobRef(data) {
      const norm = normalizedJobRefData(data);
      // Preserve caller extras the normalizer drops (resultText, lastAction).
      const merged = Object.assign({}, norm, {
        resultText: (data && data.resultText) || "",
        lastAction: (data && data.lastAction) || "",
      });
      let row = this.findSubagentRow(merged.jobId);
      if (!row) {
        this.ensureSubagentModule();
        row = this.makeSubagentRow();
        this.subagentModule.querySelector(".subs-rows").appendChild(row);
      }
      this.updateSubagentRow(row, merged);
      const mod = row.closest(".subs");
      if (mod) this.refreshSubagentModule(mod);
      if (merged.jobId) this.activeJobs.set(merged.jobId, row);
      return row;
    },

    // updateSubagentRow applies an already-shaped payload (no re-normalization,
    // so resultText/lastAction survive) to a row's glyph, name, result, etc.
    updateSubagentRow(row, data) {
      if (!row) return;
      data = data || {};
      if (data.jobId) row.dataset.jobId = data.jobId;
      if (data.jobType && !row.dataset.jobType) row.dataset.jobType = data.jobType;
      const name = row.querySelector(".nm");
      if (name) {
        const label = data.label || row.dataset.jobType || data.jobType || data.jobId || "subagent";
        if (label && (!name.textContent || data.label)) name.textContent = clip(label, 80);
      }
      const kind = this.classifyJobStatus(data.status || row.dataset.status || "running");
      if (data.status) row.dataset.status = data.status;
      row.dataset.statusKind = kind;
      // Stamp the last time we got a real signal for this row, so a later
      // honest-clock demotion can say "last seen Ns ago" (mockup #8 alt A).
      row.dataset.lastSeenAt = String(Date.now());
      const glyph = row.querySelector(".g");
      if (glyph) {
        glyph.className = "g " + this.subagentGlyphClass(kind);
        glyph.textContent = this.subagentGlyph(kind);
      }
      this.renderSubagentResult(row, data, kind);
      this.renderSubagentDuration(row, kind);
      this.applyJobRefTarget(row, data);
    },

    renderSubagentResult(row, data, kind) {
      const res = row.querySelector(".res");
      if (!res) return;
      if (data.lastAction) row.dataset.lastAction = data.lastAction;
      let text = String(data.resultText || "").trim();
      if (!text && kind === "running") {
        res.innerHTML = "";
        const live = document.createElement("span");
        live.className = "live";
        live.textContent = "running";
        res.appendChild(live);
        const action = data.lastAction || row.dataset.lastAction || "";
        if (action) res.append(" · " + clip(action, 60));
        row.classList.remove("res-error");
        return;
      }
      // A demoted-unknown row is honest about why: it was running when the
      // session went dark and never reported finishing. Neutral, italic, with
      // "last seen Ns ago" — never a fake spinner outliving the session.
      if (kind === "unknown" && !text) {
        res.innerHTML = "";
        const unk = document.createElement("span");
        unk.className = "unk";
        const ago = this.lastSeenAgo(row);
        unk.textContent = "never reported finishing" + (ago ? " — last seen " + ago : "");
        res.appendChild(unk);
        row.classList.remove("res-error");
        return;
      }
      if (!text) {
        if (kind === "failed") text = data.reason || "failed";
        else text = data.outputBytes != null ? formatBytes(data.outputBytes) : "done";
      }
      res.textContent = clip(text, 120);
      row.classList.toggle("res-error", kind === "failed");
    },

    renderSubagentDuration(row, kind) {
      const dur = row.querySelector(".dur");
      if (!dur) return;
      const started = Number(row.dataset.startedAt || 0);
      if (!started) return;
      if (kind === "running") {
        const elapsed = Date.now() - started;
        dur.textContent = elapsed >= 1000 ? formatToolDuration(elapsed) : "";
        return;
      }
      if (!row.dataset.endedAt) row.dataset.endedAt = String(Date.now());
      const span = formatToolDuration(Number(row.dataset.endedAt) - started);
      // Unknown freezes its LAST-KNOWN elapsed marked approximate (the ~ admits
      // we never saw it finish) — mockup #8 alt A's "~0:51".
      dur.textContent = kind === "unknown" && span ? "~" + span : span;
    },

    // lastSeenAgo returns a short "Ns ago" / "Nm ago" relative to the last
    // signal we recorded for a row (its lastSeenAt stamp). Empty when unknown.
    lastSeenAgo(row) {
      const seen = Number(row && row.dataset && row.dataset.lastSeenAt || 0);
      if (!seen) return "";
      const secs = Math.max(0, Math.round((Date.now() - seen) / 1000));
      if (secs < 60) return secs + "s ago";
      const mins = Math.round(secs / 60);
      if (mins < 60) return mins + "m ago";
      return Math.round(mins / 60) + "h ago";
    },

    // makeOpenBesideButton builds the quiet, hover-revealed ⇲ control shared by
    // every "open beside" affordance (subagent rows, image cards, file-
    // referencing tool cards). `ariaLabel` names the specific action; `resolve`
    // returns {href, title} at click time so callers can defer the URL/label
    // until the row's data is final. Returns null when SerfPanes is absent outside
    // a pane iframe; framed renderers post a bridge request to the host instead.
    makeOpenBesideButton(ariaLabel, resolve) {
      if (!window.SerfPanes && !(this.isInPane && this.isInPane())) return null;
      var beside = document.createElement("span");
      beside.className = "open-beside-btn";
      beside.setAttribute("role", "button");
      beside.setAttribute("tabindex", "0");
      beside.setAttribute("aria-label", ariaLabel);
      beside.title = "open beside";
      beside.textContent = "⇲";
      var self = this;
      function openBeside(e) {
        e.preventDefault();
        e.stopPropagation(); // do not trigger the host card's own click
        var spec = resolve();
        if (spec && spec.href) self.openBeside(spec);
      }
      beside.addEventListener("click", openBeside);
      beside.addEventListener("keydown", function (e) {
        if (e.key === "Enter" || e.key === " ") openBeside(e);
      });
      return beside;
    },

    applyJobRefTarget(row, data) {
      if (!row || !data || !data.transcriptRef) return;
      row.dataset.transcriptRef = data.transcriptRef;
      row.onclick = () => { this.navigateTo("/s/" + encodeURIComponent(data.transcriptRef)); };
      // "Open beside" — opens the subagent in a side pane instead of navigating away.
      if (!row.querySelector(".open-beside-btn")) {
        var renderer = this;
        var beside = this.makeOpenBesideButton("open subagent beside", function () {
          var ref = row.dataset.transcriptRef;
          var label = (row.querySelector(".nm") || {}).textContent || ref;
          return { href: renderer.threadHref(ref), title: label };
        });
        if (beside) row.appendChild(beside);
      }
    },

    // navigateTo performs a hard navigation to a workspace route. Centralized so
    // the subagent-nav affordances (a row's "view →", the Esc-to-parent
    // accelerator) share one seam — overridable in tests where jsdom has no
    // real navigation.
    navigateTo(href) {
      window.location.href = href;
    },

    // Severity rank for worst-first ordering (mockup #8 alt B). A failure sorts
    // to the very top; an unknown sits just under it (it might BE a failure we
    // never heard about); a genuinely-live row next; clean "done" recedes last.
    SUBAGENT_SEVERITY: { failed: 0, unknown: 1, running: 2, done: 3 },

    refreshSubagentModule(mod) {
      if (!mod) return;
      // Sort worst-first so a failure (or an unknown that might be one) is
      // always above the fold and the clean "done" results recede — never a
      // failure buried under a wall of green. Stable within a band by spawn
      // order.
      const rowsContainer = mod.querySelector(".subs-rows");
      const rows = Array.from(mod.querySelectorAll(".sub-r"));
      if (rowsContainer && rows.length > 1) {
        const rank = (r) => {
          const v = this.SUBAGENT_SEVERITY[r.dataset.statusKind || "running"];
          return v == null ? 2 : v;
        };
        const sorted = rows.slice().sort((a, b) => {
          const d = rank(a) - rank(b);
          if (d !== 0) return d;
          return Number(a.dataset.spawnIndex || 0) - Number(b.dataset.spawnIndex || 0);
        });
        for (const row of sorted) rowsContainer.appendChild(row);
      }
      let running = 0, done = 0, failed = 0, unknown = 0;
      for (const row of rows) {
        const kind = row.dataset.statusKind || "running";
        if (kind === "failed") failed++;
        else if (kind === "done") done++;
        else if (kind === "unknown") unknown++;
        else running++;
      }
      const title = mod.querySelector(".t");
      if (title) title.textContent = "Subagents (" + rows.length + ")";
      const tally = mod.querySelector(".tally");
      if (tally) {
        tally.innerHTML = "";
        const parts = [];
        if (failed) parts.push(["f", "✕ " + failed + " failed"]);
        if (unknown) parts.push(["u", "? " + unknown + " unknown"]);
        if (running) parts.push(["r", "⟳ " + running + " running"]);
        if (done) parts.push(["o", "✓ " + done + " done"]);
        parts.forEach(([cls, text], i) => {
          if (i > 0) tally.append(" · ");
          const span = document.createElement("span");
          span.className = cls;
          span.textContent = text;
          tally.appendChild(span);
        });
      }
      // Box-level failure flag so CSS can mark the module without averaging it
      // into a tally count.
      mod.dataset.hasFailure = failed > 0 ? "true" : "false";
      // Stale flag: the module went dark with work still unaccounted for (honest
      // neutral treatment, not a fake spinner).
      mod.dataset.stale = unknown > 0 ? "true" : "false";

      // Overflow: hide rows past the visible threshold unless expanded.
      const expanded = mod.dataset.expanded === "true";
      const limit = this.SUBAGENT_VISIBLE_ROWS;
      const hiddenCount = Math.max(0, rows.length - limit);
      Array.from(rowsContainer ? rowsContainer.querySelectorAll(".sub-r") : rows).forEach((row, i) => {
        row.hidden = !expanded && hiddenCount > 0 && i >= limit;
      });
      const more = mod.querySelector(".subs-more");
      if (more) {
        if (hiddenCount > 0) {
          more.hidden = false;
          more.textContent = expanded ? "collapse" : ("+" + hiddenCount + " more · expand");
        } else {
          more.hidden = true;
        }
      }
      // Bubble this session's worst direct-child state up onto its own parent
      // breadcrumb, so a deep failure surfaces on the way back out (mockup #9).
      this.updateBreadcrumbRollup();
    },

    // updateBreadcrumbRollup colors a worst-state chip on this subagent's
    // breadcrumb from its OWN direct children (the subagent modules in this
    // transcript). It bubbles a failure (or an unanswered "unknown") up one
    // level so a deep ✕ never hides behind a green parent: every hop up the
    // chain re-derives the chip from that level's children. There is no daemon
    // signal carrying a parent's grandchildren into the live stream, so the
    // rollup is composed honestly from the worst state we can actually observe
    // here — this session's direct children.
    updateBreadcrumbRollup() {
      const chip = document.querySelector("[data-subagent-rollup]");
      if (!chip) return;
      let failed = 0, unknown = 0;
      for (const row of this.conversation.querySelectorAll(".sub-r")) {
        const kind = row.dataset.statusKind || "running";
        if (kind === "failed") failed++;
        else if (kind === "unknown") unknown++;
      }
      if (!failed && !unknown) {
        chip.hidden = true;
        chip.textContent = "";
        chip.classList.remove("bad");
        return;
      }
      chip.hidden = false;
      if (failed) {
        chip.classList.add("bad");
        chip.textContent = "✕ " + failed + (failed === 1 ? " child failed" : " children failed");
      } else {
        chip.classList.remove("bad");
        chip.textContent = "? " + unknown + (unknown === 1 ? " child unknown" : " children unknown");
      }
    },

    // reconcileSubagent flips a (possibly stale-running) subagent row from a
    // signal other than JOB_FINISHED — a successful job_read_output / job_list /
    // delegate_send that names the job and its status. This is the fix for
    // the subagent that showed "● running" forever because JOB_FINISHED never
    // arrived. It only updates an EXISTING row (never spawns one from a read).
    reconcileSubagent(info) {
      info = info || {};
      const jobId = info.jobId || info.job_id || "";
      if (!jobId) return;
      const row = this.findSubagentRow(jobId);
      if (!row) return;
      this.updateSubagentRow(row, {
        jobId,
        jobType: info.jobType || info.type || row.dataset.jobType || "",
        status: info.status || "",
        reason: info.reason || "",
        outputBytes: info.outputBytes != null ? info.outputBytes : info.output_bytes,
        transcriptRef: info.transcriptRef || info.transcript_ref || "",
        resultText: info.resultText || "",
        lastAction: info.lastAction || "",
      });
      if (this.classifyJobStatus(info.status) !== "running") this.activeJobs.delete(jobId);
      const mod = row.closest(".subs");
      if (mod) this.refreshSubagentModule(mod);
    },

    // subagentSessionIsLive reports whether the parent session is still live
    // enough that a "running" subagent row can be trusted. Terminal states
    // (ended/closed/notLoaded) are never live; idle is live ONLY while a turn is
    // active. awaiting/warning/systemError keep the session live (the user can
    // still resume), so they do not demote dangling rows.
    subagentSessionIsLive(state) {
      if (state === "ended" || state === "closed" || state === "notLoaded") return false;
      if (state === "idle") return !!this.activeTurnId;
      return true;
    },

    // finalizeDanglingSubagents demotes every still-"running" subagent row to a
    // neutral terminal "?" unknown once the parent session is no longer live and
    // no completion signal ever arrived (mockup #8 alt A). This is the honest
    // fix for the spinner that ticked forever on a dead session: it freezes the
    // last-known elapsed (marked ~approximate) and says "never reported
    // finishing — last seen Ns ago". A genuinely-live session demotes nothing.
    finalizeDanglingSubagents(state) {
      if (this.subagentSessionIsLive(state)) return;
      if (!this.conversation) return;
      const touched = new Set();
      for (const row of this.conversation.querySelectorAll(".sub-r")) {
        if ((row.dataset.statusKind || "running") !== "running") continue;
        // Freeze the elapsed at the last signal we saw, not at "now" — the work
        // stopped being observable then, so that is the honest endpoint.
        const seen = Number(row.dataset.lastSeenAt || 0) || Number(row.dataset.startedAt || 0);
        if (seen && !row.dataset.endedAt) row.dataset.endedAt = String(seen);
        row.dataset.status = "unknown";
        row.dataset.statusKind = "unknown";
        const glyph = row.querySelector(".g");
        if (glyph) {
          glyph.className = "g " + this.subagentGlyphClass("unknown");
          glyph.textContent = this.subagentGlyph("unknown");
        }
        this.renderSubagentResult(row, {}, "unknown");
        this.renderSubagentDuration(row, "unknown");
        const jobId = row.dataset.jobId;
        if (jobId) this.activeJobs.delete(jobId);
        const mod = row.closest(".subs");
        if (mod) touched.add(mod);
      }
      for (const mod of touched) this.refreshSubagentModule(mod);
    },

    beginJobRef(data) {
      if (!this.shouldRenderJobRefAsSubagent(data)) return;
      this.upsertJobRef(data);
    },

    shouldRenderJobRefAsSubagent(data) {
      const norm = normalizedJobRefData(data);
      const jobType = String(norm.jobType || "").trim().toLowerCase();
      if (jobType) return jobType === "delegate";
      return !!norm.transcriptRef;
    },

    finalizeJobRef(data) {
      data = normalizedJobRefData(data);
      const jobId = data.jobId || "";
      const status = data.status || "completed";
      // A standalone JOB_FINISHED (no preceding spawn row) still creates a row
      // for subagent jobs so the completion is visible; otherwise reconcile the
      // existing row. Non-subagent jobs (shell, etc.) stay with their tool cards.
      if (this.findSubagentRow(jobId)) {
        this.reconcileSubagent({
          jobId, jobType: data.jobType, status, reason: data.reason,
          outputBytes: data.outputBytes, transcriptRef: data.transcriptRef,
        });
      } else if (this.shouldRenderJobRefAsSubagent(data)) {
        this.upsertJobRef(Object.assign({}, data, { status }));
      }
      this.activeJobs.delete(jobId);
    },

    appendBanner(kind, text, diagnostic) {
      this.endCheapCluster();
      this.closeSubagentModule();
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
      if (attrs.output_bytes && attrs.output_bytes !== "0") push("output", attrs.output_bytes + " B");
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

    appendSteeringMessage(text) {
      this.endCheapCluster();
      this.closeSubagentModule();
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

      if (summary.kind === "notification") {
        this.appendNotificationCard(summary);
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
      this.appendPlanBlock();
      this.refreshTaskBadgeSoon();
    },

    // appendPlanBlock renders the inline plan checklist (mockup #18 alt C): the
    // full current task set as ONE neutral box at the point the plan was
    // declared/updated, with the three glyph-paired states (✓ done · ⟳ current
    // · ○ pending) and a progress count. Empty plan ⇒ render NOTHING (no plan,
    // no control). The task set is read from the shared description/status cache
    // (taskDetails), which append/update calls and /tasks fetches keep current,
    // so the block always shows real statuses ordered by task id.
    appendPlanBlock() {
      const tasks = Array.from(taskDetails.values())
        .filter(t => t && t.id != null)
        .sort((a, b) => Number(a.id) - Number(b.id));
      if (!tasks.length) return;

      const done = tasks.filter(t => t.status === "done" || t.status === "cancelled").length;
      const block = document.createElement("div");
      block.className = "plan-block";

      const header = document.createElement("div");
      header.className = "plan-header";
      const title = document.createElement("span");
      title.className = "plan-title";
      title.textContent = "Plan";
      const prog = document.createElement("span");
      prog.className = "plan-progress";
      prog.textContent = done + " / " + tasks.length;
      header.appendChild(title);
      header.appendChild(prog);
      block.appendChild(header);

      for (const t of tasks) {
        const item = document.createElement("div");
        item.className = "plan-item " + planStateClass(t.status);
        const glyph = document.createElement("span");
        glyph.className = "plan-glyph";
        glyph.textContent = planGlyphForStatus(t.status);
        const step = document.createElement("span");
        step.className = "plan-step";
        step.textContent = t.description || t.title || ("#" + t.id);
        item.appendChild(glyph);
        item.appendChild(step);
        block.appendChild(item);
      }
      this.conversation.appendChild(block);
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
      // Once parked at the bottom there is no unseen content, so the
      // new-content pill (and its counter) must reset.
      this.clearNewContentPill();
    },

    // isNearBottom reports whether the transcript is scrolled to (or within a
    // line or two of) the bottom — the condition for auto-sticking on new frames.
    isNearBottom() {
      const el = this.conversation;
      if (!el) return true;
      return (el.scrollHeight - el.scrollTop - el.clientHeight) < 50;
    },

    // isAnchorBelow / isAnchorAbove report where an attention anchor sits
    // relative to the current viewport, so the pill's arrow can point toward it.
    isAnchorBelow(el) {
      const sc = this.conversation;
      if (!sc || !el) return false;
      return el.offsetTop > sc.scrollTop + sc.clientHeight - 24;
    },
    isAnchorAbove(el) {
      const sc = this.conversation;
      if (!sc || !el) return false;
      return el.offsetTop + el.offsetHeight < sc.scrollTop + 24;
    },

    // pickUrgentAnchor picks the most urgent attention anchor relative to the
    // viewport (mockup #14): error > needs-you > plain new. An errored tool row
    // (data-attention="error") is a per-row anchor we can jump to; needs-you is
    // a whole-session signal whose target is the transcript tail. Returns
    // {kind, dir, el} or null when nothing urgent is off-screen.
    pickUrgentAnchor() {
      const sc = this.conversation;
      if (!sc) return null;
      const errors = Array.from(sc.querySelectorAll('.tool-call[data-attention="error"]'));
      const errBelow = errors.find(el => this.isAnchorBelow(el));
      if (errBelow) return { kind: "error", dir: "down", el: errBelow };
      const errAbove = errors.find(el => this.isAnchorAbove(el));
      if (errAbove) return { kind: "error", dir: "up", el: errAbove };
      // Needs-you is a session-level signal; its home is the latest content at
      // the tail, which is always below the reader while they're scrolled up.
      // The docked bar above the composer (mockup #16 Alt C) is the
      // authoritative blocking indicator — when it owns the signal, the pill
      // defers so there is never a duplicate amber "needs you" affordance.
      if ((this.newContentNeedsYou || this.state === "awaiting") && !this.needsYouDockActive()) {
        return { kind: "needs-you", dir: "down", el: null };
      }
      return null;
    },

    // bindScrollAffordance wires the floating "↓ N new" pill. The pill lives in
    // the conversation's positioned parent (not inside the scroll area), so it
    // stays anchored near the bottom of the transcript while content scrolls
    // underneath. A scroll listener on the conversation clears it the moment the
    // reader returns to the bottom on their own.
    bindScrollAffordance() {
      const el = this.conversation;
      if (!el) return;
      const host = el.parentNode;
      if (host && !host.querySelector("[data-new-content-pill]")) {
        const pill = document.createElement("button");
        pill.type = "button";
        pill.className = "new-content-pill";
        pill.setAttribute("data-new-content-pill", "");
        pill.hidden = true;
        pill.addEventListener("click", () => this.jumpToNewContent());
        host.appendChild(pill);
      }
      // Avoid stacking listeners when init runs again on a re-entered element.
      if (!el.__serfScrollPillBound) {
        el.__serfScrollPillBound = true;
        el.addEventListener("scroll", () => {
          if (this.isNearBottom()) this.clearNewContentPill();
          // Re-evaluate the urgent anchor as the reader scrolls, so the arrow
          // flips (↓→↑) when they pass an error and the label tracks what's
          // still off-screen — without churning the debounced count.
          else if (this.newContentCount > 0) this.renderNewContentPill();
          // The dock tracks whether the question scrolled out of view, so it
          // appears the moment the ask leaves the viewport and clears when the
          // reader returns to it.
          this.renderNeedsYouDock();
        });
      }
      this.clearNewContentPill();
      // Materialize the dock up front so its presence is independent of the
      // first scroll event.
      this.needsYouDockEl();
    },

    newContentPillEl() {
      const el = this.conversation;
      if (!el || !el.parentNode) return null;
      return el.parentNode.querySelector("[data-new-content-pill]");
    },

    // noteNewContent records that `added` new transcript entries rendered while
    // the reader was scrolled up, and repaints the pill. The pill goes
    // attention-aware ("↓ needs you", amber) when the thread is in the awaiting
    // state — the daemon-advertised signal that the agent is waiting on the user.
    noteNewContent(added) {
      this.newContentCount += added;
      if (this.state === "awaiting") this.newContentNeedsYou = true;
      this.renderNewContentPill();
    },

    // renderNewContentPill paints the floating pill, glyph-paired and aware of
    // the most urgent thing off-screen (mockup #14): "✕ error" (red) outranks
    // "◆ needs you" (amber) outranks the plain "↓ N new" (neutral). The arrow
    // points ↓ to an urgent anchor below the viewport, ↑ once you've scrolled
    // past it. Only the plain count is debounced — a burst of appends settles to
    // one number rather than churning — while urgency repaints immediately.
    renderNewContentPill() {
      const pill = this.newContentPillEl();
      if (!pill) return;
      if (this.newContentCount <= 0) {
        this.clearNewContentPill();
        return;
      }
      pill.hidden = false;
      const urgent = this.pickUrgentAnchor();
      pill.classList.remove("needs-you", "error");
      if (urgent && urgent.kind === "error") {
        this.newContentJumpTarget = urgent.el;
        pill.classList.add("error");
        pill.textContent = (urgent.dir === "up" ? "↑" : "↓") + " ✕ error";
      } else if (urgent && urgent.kind === "needs-you") {
        this.newContentJumpTarget = null;
        pill.classList.add("needs-you");
        pill.textContent = "↓ ◆ needs you";
      } else {
        this.newContentJumpTarget = null;
        // The plain count is the only churning value, so it is debounced: a
        // burst of appends settles to one number instead of repainting the
        // badge on every frame. Show the last settled value now and schedule a
        // trailing commit of the latest count.
        pill.textContent = "↓ " + this.newContentPaintedCount + " new";
        this.scheduleNewContentCountPaint();
      }
    },

    // scheduleNewContentCountPaint commits the live count to the pill on a
    // trailing debounce so a burst of appends repaints the number once it
    // settles, not on every frame.
    scheduleNewContentCountPaint() {
      if (this.newContentCountTimer) clearTimeout(this.newContentCountTimer);
      this.newContentCountTimer = setTimeout(() => {
        this.newContentCountTimer = null;
        this.newContentPaintedCount = this.newContentCount;
        const pill = this.newContentPillEl();
        // Repaint only while still showing the plain count (urgency takes over
        // the label otherwise).
        if (pill && !pill.hidden && !pill.classList.contains("needs-you") &&
            !pill.classList.contains("error")) {
          pill.textContent = "↓ " + this.newContentPaintedCount + " new";
        }
      }, 300);
    },

    // jumpToNewContent jumps to the urgent anchor (smooth) when there is one,
    // otherwise scrolls to the bottom, then recomputes the pill.
    jumpToNewContent() {
      const sc = this.conversation;
      const target = this.newContentJumpTarget;
      if (sc && target && sc.contains(target)) {
        sc.scrollTo({ top: Math.max(0, target.offsetTop - 16), behavior: "smooth" });
        // We've moved; the urgent anchor (and the count) must be recomputed.
        if (this.isNearBottom()) this.clearNewContentPill();
        else this.renderNewContentPill();
      } else {
        this.scrollToBottom();
      }
    },

    clearNewContentPill() {
      this.newContentCount = 0;
      this.newContentPaintedCount = 0;
      this.newContentNeedsYou = false;
      this.newContentJumpTarget = null;
      if (this.newContentCountTimer) {
        clearTimeout(this.newContentCountTimer);
        this.newContentCountTimer = null;
      }
      const pill = this.newContentPillEl();
      if (!pill) return;
      pill.hidden = true;
      pill.classList.remove("needs-you", "error");
      pill.textContent = "";
    },

    // ── Blocking needs-you dock (mockup #16 Alt C) ───────────────────────────
    // The amber bar that docks above the composer so a blocking agent question
    // can never hang off-screen. It is the authoritative needs-you indicator;
    // the new-content pill defers to it (no duplicate amber signal). Created
    // once, just before the composer form, and shown only while an unanswered
    // question is off-screen OR the session is awaiting.
    needsYouDockEl() {
      const form = document.querySelector("form[data-input-form]");
      if (!form || !form.parentNode) return null;
      let dock = form.parentNode.querySelector("[data-needs-you-dock]");
      if (!dock) {
        dock = document.createElement("button");
        dock.type = "button";
        dock.className = "needs-you-dock";
        dock.setAttribute("data-needs-you-dock", "");
        dock.hidden = true;
        dock.addEventListener("click", () => this.jumpToAgentQuestion());
        form.parentNode.insertBefore(dock, form);
      }
      return dock;
    },

    // needsYouDockActive reports whether the dock should claim the needs-you
    // signal: there is an unanswered question that is off-screen, or the session
    // is in the awaiting state. While active, the pill must not also paint
    // "needs you" (the dock is authoritative).
    needsYouDockActive() {
      const q = this.agentQuestionEl;
      if (q && this.conversation && this.conversation.contains(q) &&
          (this.isAnchorBelow(q) || this.isAnchorAbove(q))) {
        return true;
      }
      return this.state === "awaiting" && !!q && this.conversation && this.conversation.contains(q);
    },

    renderNeedsYouDock() {
      const dock = this.needsYouDockEl();
      if (!dock) return;
      if (!this.needsYouDockActive()) {
        dock.hidden = true;
        dock.textContent = "";
        // The dock just released the needs-you signal; let the pill repaint so
        // it can reclaim "needs you" if content is still off-screen.
        if (this.newContentCount > 0) this.renderNewContentPill();
        return;
      }
      dock.hidden = false;
      dock.textContent = "◆ The agent is waiting on your answer — jump to it";
      // Authoritative signal: drop any duplicate needs-you treatment on the pill.
      const pill = this.newContentPillEl();
      if (pill && pill.classList.contains("needs-you")) this.renderNewContentPill();
    },

    // jumpToAgentQuestion scrolls the transcript to the blocking question and
    // focuses the composer so the reply is one keystroke away.
    jumpToAgentQuestion() {
      const sc = this.conversation;
      const q = this.agentQuestionEl;
      if (sc && q && sc.contains(q)) {
        const top = Math.max(0, q.offsetTop - 16);
        if (typeof sc.scrollTo === "function") sc.scrollTo({ top, behavior: "smooth" });
        else sc.scrollTop = top;
      }
      this.focusComposer();
      this.renderNeedsYouDock();
    },

    focusComposer() {
      const ta = document.querySelector("form[data-input-form] .message-input");
      if (ta) ta.focus();
    },

    // clearAgentQuestion drops the blocking-question state once it has been
    // answered (the user sent a message) or superseded (a new turn started).
    // The in-flow amber frame stays as a record of the resolved exchange; only
    // the live "awaiting your answer" affordances are torn down.
    clearAgentQuestion() {
      if (!this.agentQuestionEl) return;
      this.agentQuestionEl = null;
      this.renderNeedsYouDock();
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
            ta.placeholder = "type a steering message, then click steer…";
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
      this.bindSubagentEscapeToParent();
      const ta = document.querySelector(".message-input");
      if (!ta) return;
      const suppressSubmitShortcuts = this.isInPane && this.isInPane();
      ta.addEventListener("keydown", (e) => {
        if (!suppressSubmitShortcuts && e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
          e.preventDefault();
          const form = ta.closest("form");
          if (form) form.requestSubmit();
          return;
        }
        // Shift+Enter is the keybind equivalent of the "steer"
        // button (kata 0bq1): drain whatever's queued (plus anything in
        // the textarea) as a single STEERING injection. Pre-existing
        // browser default (newline insertion) is suppressed.
        if (!suppressSubmitShortcuts && e.key === "Enter" && e.shiftKey && !e.metaKey && !e.ctrlKey && !e.altKey) {
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

    // bindSubagentEscapeToParent makes "Esc → parent" real on a subagent's
    // workspace (mockup #9): Escape navigates up to the parent. It defers to any
    // open overlay (a panel/dialog/picker's own Escape handler closes that
    // first) and ignores Escape while typing, so it only fires as a last-resort
    // "get me out of this subagent" accelerator. Bound once per process.
    bindSubagentEscapeToParent() {
      if (this.__escToParentBound) return;
      this.__escToParentBound = true;
      document.addEventListener("keydown", (e) => {
        if (e.key !== "Escape" || e.metaKey || e.ctrlKey || e.altKey) return;
        // Defer to any open overlay — its own handler owns this Escape press.
        if (document.getElementById("panel-scrim")) return;
        if (document.getElementById("details-panel")) return;
        if (document.getElementById("tasks-panel")) return;
        const dlg = document.getElementById("search-dialog");
        if (dlg && dlg.open) return;
        // Don't steal Escape from text entry.
        const ae = document.activeElement;
        if (ae && (ae.tagName === "TEXTAREA" || ae.tagName === "INPUT" || ae.isContentEditable)) return;
        const crumb = document.querySelector(".subagent-parent-up[href]");
        if (!crumb) return;
        e.preventDefault();
        this.navigateTo(crumb.getAttribute("href"));
      });
    },
  };

  // applyStatusDotPulse sets [data-pulse] on every .status-dot under root
  // whose data-state is in the "should breathe" set. Idempotent. Called
  // after any DOM change that may have introduced status dots.
  function applyStatusDotPulse(root) {
    const scope = root || document;
    // A session past the stall threshold must not keep breathing as if all is
    // well — but a brief calm-quiet gap is expected and keeps the pulse.
    const stalled = !!document.querySelector('.conversation[data-stalled="true"]');
    const dots = scope.querySelectorAll(".status-dot[data-state]");
    dots.forEach(dot => {
      const state = dot.getAttribute("data-state");
      const shouldPulse = !stalled && (state === "active" || state === "awaiting" || state === "errored");
      if (shouldPulse) {
        dot.setAttribute("data-pulse", "");
      } else {
        dot.removeAttribute("data-pulse");
      }
    });
  }
  SerfRenderer.applyStatusDotPulse = applyStatusDotPulse;

  window.SerfRenderer = SerfRenderer;

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
  document.addEventListener("serf-hub:transcript-system-status-changed", () => {
    SerfRenderer.refreshTranscriptStatusVisibility();
  });

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
