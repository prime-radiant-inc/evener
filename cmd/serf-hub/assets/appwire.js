(function () {
  "use strict";

  const METHOD = {
    initialize: "initialize",
    threadList: "thread/list",
    threadRead: "thread/read",
    threadStart: "thread/start",
    threadFork: "thread/fork",
    threadClear: "thread/clear",
    threadModelSet: "thread/model/set",
    threadCompactStart: "thread/compact/start",
    threadShutdown: "thread/shutdown",
    turnStart: "turn/start",
    turnSteer: "turn/steer",
    turnInterrupt: "turn/interrupt",
    turnQueue: "turn/queue",
    turnDrainAsSteer: "turn/drainAsSteer",
    tasksList: "serf/tasks/list",
    dirsComplete: "serf/dirs/complete",
    modelList: "model/list",
  };

  let ws = null;
  let connecting = null;
  let nextId = 1;
  const pending = new Map();
  const notificationHandlers = new Set();
  const connectionLostHandlers = new Set();
  const liveItemState = new Map();

  function rpcURL() {
    const proto = window.location.protocol === "https:" ? "wss:" : "ws:";
    return proto + "//" + window.location.host + "/rpc";
  }

  function connect() {
    if (ws && ws.readyState === WebSocket.OPEN) return Promise.resolve(ws);
    if (connecting) return connecting;
    connecting = new Promise((resolve, reject) => {
      const sock = new WebSocket(rpcURL());
      let disconnected = false;
      let initializeFailed = false;
      const markDisconnected = (err) => {
        if (disconnected) return;
        disconnected = true;
        notifyConnectionLost(err);
      };
      const rejectPending = (err) => {
        for (const item of pending.values()) item.reject(err);
        pending.clear();
      };
      ws = sock;
      sock.addEventListener("open", () => {
        request(METHOD.initialize, {
          clientInfo: { name: "serf-web", version: "0.1.0" },
          capabilities: {},
        }).then(() => resolve(sock), (err) => {
          initializeFailed = true;
          if (ws === sock) ws = null;
          connecting = null;
          rejectPending(err);
          try { sock.close(); } catch (_) {}
          reject(err);
        });
      });
      sock.addEventListener("message", (event) => handleMessage(event.data));
      sock.addEventListener("error", () => {
        const err = new Error("appwire connection error");
        if (ws === sock) ws = null;
        connecting = null;
        rejectPending(err);
        markDisconnected(err);
        reject(err);
      });
      sock.addEventListener("close", () => {
        const err = new Error("appwire connection closed");
        if (ws === sock) {
          ws = null;
          connecting = null;
          rejectPending(err);
          if (!initializeFailed) markDisconnected(err);
        }
      });
    });
    return connecting;
  }

  function request(method, params) {
    const id = nextId++;
    const send = () => new Promise((resolve, reject) => {
      pending.set(String(id), { resolve, reject });
      ws.send(JSON.stringify({ id, method, params: params || {} }));
    });
    if (ws && ws.readyState === WebSocket.OPEN) return send();
    if (method === METHOD.initialize) return send();
    return connect().then(send);
  }

  function handleMessage(raw) {
    let msg;
    try { msg = JSON.parse(raw); } catch (_) { return; }
    if (msg.id != null) {
      const slot = pending.get(String(msg.id));
      if (!slot) return;
      pending.delete(String(msg.id));
      if (msg.error) slot.reject(errorFromWire(msg.error));
      else slot.resolve(msg.result || {});
      return;
    }
    if (msg.method) {
      for (const handler of Array.from(notificationHandlers)) {
        try { handler(msg.method, msg.params || {}); } catch (_) {}
      }
    }
  }

  function errorFromWire(wire) {
    const err = new Error((wire && wire.message) || "appwire error");
    if (!wire) return err;
    if (Object.prototype.hasOwnProperty.call(wire, "code")) err.code = wire.code;
    if (Object.prototype.hasOwnProperty.call(wire, "data")) {
      err.data = wire.data;
      if (wire.data && typeof wire.data.serfErrorInfo === "string") {
        err.serfErrorInfo = wire.data.serfErrorInfo;
      }
    }
    return err;
  }

  function onNotification(handler) {
    notificationHandlers.add(handler);
    return () => notificationHandlers.delete(handler);
  }

  function onConnectionLost(handler) {
    connectionLostHandlers.add(handler);
    return () => connectionLostHandlers.delete(handler);
  }

  function notifyConnectionLost(err) {
    for (const handler of Array.from(connectionLostHandlers)) {
      try { handler(err || new Error("appwire connection lost")); } catch (_) {}
    }
  }

  function refForSession(sessionId) {
    if (!sessionId) return "";
    if (String(sessionId).includes(":")) return String(sessionId);
    return "local:" + sessionId;
  }

  function splitProviderModel(raw) {
    const value = String(raw || "").trim();
    const idx = value.indexOf("/");
    if (idx < 0) return { provider: "", model: value };
    return { provider: value.slice(0, idx), model: value.slice(idx + 1) };
  }

  function threadID(thread) {
    return (thread && (thread.sessionId || thread.id)) || "";
  }

  function threadRef(thread) {
    return (thread && thread.serf && thread.serf.ref) || refForSession(threadID(thread));
  }

  function replaySessionID(thread) {
    const id = threadID(thread);
    const ref = threadRef(thread);
    if (ref && !ref.startsWith("local:")) return ref;
    return id;
  }

  function basename(path) {
    const trimmed = String(path || "").replace(/\/+$/, "");
    if (!trimmed) return "(no project)";
    const parts = trimmed.split("/");
    return parts[parts.length - 1] || trimmed;
  }

  function searchResult(thread) {
    const id = threadID(thread);
    const ref = threadRef(thread);
    const state = (thread.status && thread.status.type) || "ended";
    const title = thread.name || thread.preview || id;
    return {
      id: ref || id,
      ref,
      title,
      state,
      project: thread.path || basename(thread.cwd),
      age: state === "ended" ? "" : "now",
    };
  }

  function listThreads(params) {
    return request(METHOD.threadList, params || {}).then((resp) => resp.data || []);
  }

  function search(query) {
    return listThreads({ searchTerm: query || "", limit: 50, includeSubagents: true }).then((threads) => {
      const live = [];
      const past = [];
      for (const thread of threads) {
        const result = searchResult(thread);
        if (result.state === "ended" || result.state === "closed") past.push(result);
        else live.push(result);
      }
      return { live, past };
    });
  }

  function listModels(params) {
    return request(METHOD.modelList, params || {}).then((resp) => resp.data || []);
  }

  function completeDirs(prefix) {
    return request(METHOD.dirsComplete, { prefix: prefix || "" }).then((resp) => ({
      results: (resp.data || []).map((path) => ({ path, is_git: false })),
    }));
  }

  function startThread(body) {
    return request(METHOD.threadStart, {
      harness: body.harness || "",
      cwd: body.working_dir || "",
      prompt: body.prompt || body.task || "",
      modelProvider: "",
      model: String(body.model || "").trim(),
      profile: body.agent || "",
      reasoningEffort: body.reasoning_effort || "",
    }).then((resp) => {
      const thread = resp.thread || {};
      return { ref: threadRef(thread), session_id: threadID(thread) };
    });
  }

  function readThread(sessionId, includeTurns, subscribe) {
    return request(METHOD.threadRead, { ref: refForSession(sessionId), includeTurns: !!includeTurns, itemsView: "full", subscribe: !!subscribe });
  }

  function tasks(sessionId) {
    return request(METHOD.tasksList, { ref: refForSession(sessionId) }).then((resp) => resp.data || []);
  }

  function inputItems(images) {
    return (images || []).map((img) => ({
      type: "input_image",
      mediaType: img.media_type || img.mediaType || "",
      url: img.url || "",
      data: img.data || "",
      name: img.name || "",
    }));
  }

  function startTurn(sessionId, text, images) {
    return request(METHOD.turnStart, {
      ref: refForSession(sessionId),
      prompt: text || "",
      items: inputItems(images),
    });
  }

  function steer(sessionId, turnId, text) {
    return request(METHOD.turnSteer, { ref: refForSession(sessionId), turnId: turnId || "", text: text || "" });
  }

  // queueTurn enqueues a user message while a turn is in flight (kata 111a).
  // The daemon returns Conflict when no turn is active; callers should fall
  // back to startTurn in that case.
  function queueTurn(sessionId, text) {
    return request(METHOD.turnQueue, { ref: refForSession(sessionId), text: text || "" });
  }

  // drainAsSteer drains the daemon's input queue into a single STEERING
  // injection on the active turn (kata 0bq1). Rejected when the queue is
  // empty or no turn is active.
  function drainAsSteer(sessionId) {
    return request(METHOD.turnDrainAsSteer, { ref: refForSession(sessionId) });
  }

  function action(sessionId, name, turnId) {
    const ref = refForSession(sessionId);
    if (name === "interrupt") return request(METHOD.turnInterrupt, { ref, turnId: turnId || "" });
    if (name === "compact") return request(METHOD.threadCompactStart, { ref });
    if (name === "clear") return request(METHOD.threadClear, { ref });
    if (name === "shutdown") return request(METHOD.threadShutdown, { ref });
    return Promise.reject(new Error("unknown action: " + name));
  }

  function setModel(sessionId, rawModel) {
    const model = splitProviderModel(rawModel);
    return request(METHOD.threadModelSet, {
      ref: refForSession(sessionId),
      modelProvider: model.provider,
      model: model.model,
    });
  }

  function forkThread(sessionId, body) {
    return request(METHOD.threadFork, {
      ref: refForSession(sessionId),
      sourceTurnId: String(body.turn || ""),
      editedInput: body.edited_message || "",
      label: body.label || "",
    }).then((resp) => {
      const thread = resp.thread || {};
      return { ref: threadRef(thread), session_id: threadID(thread) };
    });
  }

  function firstNonEmpty() {
    for (const value of arguments) {
      if (value) return value;
    }
    return "";
  }

  function transcriptEntryIndex(item) {
    const n = Number(item && item.transcriptEntryIndex);
    return Number.isFinite(n) && n > 0 ? n : 0;
  }

  function imagesForUserItem(item) {
    return (item.images || []).map((img) => ({
      media_type: img.mediaType || img.media_type || "",
      data: img.data || "",
      name: img.name || "",
      sha: img.metadata && img.metadata.sha,
      size: img.metadata && img.metadata.size,
    }));
  }

  function eventsFromItem(item) {
    if (!item) return [];
    if (item.type === "user_message") {
      const event = { text: item.text || "", images: imagesForUserItem(item) };
      const entryIndex = transcriptEntryIndex(item);
      if (entryIndex > 0) event.turn = entryIndex;
      return [["USER_INPUT", event]];
    }
    if (item.type === "steering") {
      return [["STEERING_INJECTED", { text: item.text || "" }]];
    }
    if (item.type === "agent_message") {
      if (!item.text) return [];
      return [["ASSISTANT_TEXT_START", {}], ["ASSISTANT_TEXT_END", { text: item.text }]];
    }
    if (item.type === "tool_call") {
      const callID = firstNonEmpty(item.callId, item.id);
      const itemID = item.id || "";
      const completed = item.status === "completed" || !!item.output || !!item.error;
      const out = [["TOOL_CALL_START", { call_id: callID, item_id: itemID, tool_name: item.toolName || "", arguments_json: item.argumentsJson || "" }]];
      if (!completed) return out;
      if (item.output) out.push(["TOOL_CALL_OUTPUT_DELTA", { call_id: callID, item_id: itemID, delta: item.output }]);
      out.push(["TOOL_CALL_END", { call_id: callID, item_id: itemID, tool_name: item.toolName || "", output: item.output || "", error: item.error || "", tool_state: item.raw || "" }]);
      return out;
    }
    return [];
  }

  function liveThreadKey(params, item) {
    params = params || {};
    item = item || {};
    return firstNonEmpty(params.ref, params.threadRef, params.threadId, item.ref, item.threadRef, item.threadId);
  }

  function liveItemKey(params, item) {
    params = params || {};
    item = item || {};
    const itemKey = firstNonEmpty(item.callId, item.id, item.itemId);
    if (!itemKey) return "";
    const threadKey = liveThreadKey(params, item);
    const turn = params.turn || {};
    const turnKey = firstNonEmpty(params.turnId, item.turnId, turn.id);
    const parts = [];
    if (threadKey) parts.push("thread:" + threadKey);
    if (turnKey) parts.push("turn:" + turnKey);
    parts.push("item:" + itemKey);
    return parts.join("\x00");
  }

  function markLiveItem(params, item, patch) {
    const key = liveItemKey(params, item);
    if (!key) return null;
    const state = liveItemState.get(key) || {};
    Object.assign(state, patch || {});
    liveItemState.set(key, state);
    return state;
  }

  function deleteLiveItem(params, item) {
    const key = liveItemKey(params, item);
    if (key) liveItemState.delete(key);
  }

  function eventsFromCompletedTurnItem(params, item) {
    const key = liveItemKey(params, item);
    const state = key ? liveItemState.get(key) : null;
    if (!state) return eventsFromItem(item);
    if (state.completed) return [];
    if (item.type === "agent_message") return [["ASSISTANT_TEXT_END", { text: item.text || "" }]];
    if (item.type === "tool_call") {
      const callID = firstNonEmpty(item.callId, item.id);
      const itemID = item.id || "";
      const out = [];
      if (!state.started) {
        out.push(["TOOL_CALL_START", {
          call_id: callID,
          item_id: itemID,
          tool_name: item.toolName || "",
          arguments_json: item.argumentsJson || "",
        }]);
      }
      out.push(["TOOL_CALL_END", {
        call_id: callID,
        item_id: itemID,
        tool_name: item.toolName || "",
        arguments_json: item.argumentsJson || "",
        output: item.output || "",
        error: item.error || "",
        tool_state: item.raw || "",
      }]);
      return out;
    }
    return [];
  }

  function eventsFromThread(thread) {
    const events = [["SESSION_START", {
      session_id: replaySessionID(thread),
      ref: threadRef(thread),
      model: thread.modelProvider || "",
      profile: thread.serf && thread.serf.profile || "",
      restored: true,
    }]];
    // Seed the renderer's queue state from the authoritative thread view
    // (kata r80p). Without this, a cold-load tab would render an empty
    // preview until the next mutation arrived as a notification.
    const queue = thread && thread.serf && thread.serf.queue;
    if (queue && (queue.depth || (Array.isArray(queue.preview) && queue.preview.length))) {
      events.push(["QUEUE_CHANGED", {
        depth: typeof queue.depth === "number" ? queue.depth : (queue.preview || []).length,
        preview: Array.isArray(queue.preview) ? queue.preview.slice() : [],
      }]);
    }
    const activeToolCalls = new Set();
    for (const turn of thread.turns || []) {
      for (const item of turn.items || []) {
        if (item && item.type === "tool_call") {
          const callID = firstNonEmpty(item.callId, item.id);
          const itemID = item.id || "";
          const completed = item.status === "completed" || !!item.output || !!item.error;
          if (completed && activeToolCalls.has(callID)) {
            events.push(["TOOL_CALL_END", {
              call_id: callID,
              item_id: itemID,
              tool_name: item.toolName || "",
              output: item.output || "",
              error: item.error || "",
              tool_state: item.raw || "",
            }]);
            activeToolCalls.delete(callID);
            continue;
          }
          events.push.apply(events, eventsFromItem(item));
          if (!completed && callID) activeToolCalls.add(callID);
          continue;
        }
        events.push.apply(events, eventsFromItem(item));
      }
      if (turn.status === "failed") {
        events.push(["ERROR", errorPayload(turn.error, "turn failed")]);
      }
    }
    return events;
  }

  function activeTurnIDFromThread(thread) {
    for (const turn of (thread && thread.turns) || []) {
      if (turn && turn.status === "running") return turn.id || "";
      if (turn && turn.status === "inProgress") return turn.id || "";
    }
    return "";
  }

  function errorPayload(error, fallback) {
    error = error || {};
    const payload = { error: error.message || fallback || "turn failed" };
    if (error.source) payload.source = error.source;
    if (error.title) payload.title = error.title;
    if (error.hint) payload.hint = error.hint;
    return payload;
  }

  function eventsFromNotification(method, params) {
    params = params || {};
    const item = params.item || {};
    if (method === "thread/status/changed") {
      return [["THREAD_STATUS_CHANGED", { status: params.status && params.status.type || "" }]];
    }
    if (method === "thread/queueChanged") {
      const q = params.queue || {};
      return [["QUEUE_CHANGED", {
        depth: typeof q.depth === "number" ? q.depth : (Array.isArray(q.preview) ? q.preview.length : 0),
        preview: Array.isArray(q.preview) ? q.preview.slice() : [],
      }]];
    }
    if (method === "turn/started") {
      const turn = params.turn || {};
      return [["TURN_STARTED", { turnId: firstNonEmpty(params.turnId, turn.id) }]];
    }
    if (method === "item/started") {
      markLiveItem(params, item, { started: true });
      if (item.type === "agent_message") return [["ASSISTANT_TEXT_START", {}]];
      if (item.type === "tool_call") return [["TOOL_CALL_START", {
        call_id: firstNonEmpty(item.callId, item.id),
        item_id: item.id || "",
        tool_name: item.toolName || "",
        arguments_json: item.argumentsJson || "",
      }]];
      return [];
    }
    if (method === "item/completed") {
      markLiveItem(params, item, { completed: true });
      if (item.type === "tool_call") return [["TOOL_CALL_END", {
        call_id: firstNonEmpty(item.callId, item.id),
        item_id: item.id || "",
        tool_name: item.toolName || "",
        arguments_json: item.argumentsJson || "",
        output: item.output || "",
        error: item.error || "",
        tool_state: item.raw || "",
      }]];
      if (item.type === "agent_message") return [["ASSISTANT_TEXT_END", { text: item.text || "" }]];
      return eventsFromItem(item);
    }
    if (method === "item/agentMessage/delta") {
      markLiveItem(params, { id: params.itemId }, { delta: true });
      return [["ASSISTANT_TEXT_DELTA", { delta: params.delta || "" }]];
    }
    if (method === "item/toolOutput/delta") {
      markLiveItem(params, { itemId: params.itemId, callId: params.callId }, { delta: true });
      return [["TOOL_CALL_OUTPUT_DELTA", {
        call_id: firstNonEmpty(params.callId, params.itemId),
        item_id: params.itemId || "",
        delta: params.delta || "",
      }]];
    }
    if (method === "turn/completed" && params.turn) {
      const turnId = firstNonEmpty(params.turnId, params.turn.id);
      if (params.turn.status === "failed") {
        for (const item of params.turn.items || []) deleteLiveItem(params, item);
        return [["TURN_COMPLETED", { turnId }], ["ERROR", errorPayload(params.turn.error, "turn failed")]];
      }
      const out = [["TURN_COMPLETED", { turnId }]];
      for (const item of params.turn.items || []) {
        out.push.apply(out, eventsFromCompletedTurnItem(params, item));
        deleteLiveItem(params, item);
      }
      return out;
    }
    if (method === "warning") {
      const warning = params.warning || "";
      let message = params.message || "";
      if (!message && warning && typeof warning.message === "string") message = warning.message;
      if (!message && typeof warning === "string") message = warning;
      const payload = { message };
      if (params.source) payload.source = params.source;
      if (params.title) payload.title = params.title;
      if (params.hint) payload.hint = params.hint;
      return [["WARNING", payload]];
    }
    if (method === "serf/steering/injected") return [["STEERING_INJECTED", { text: params.text || "" }]];
    if (method === "serf/subagent/started") return [["SUBAGENT_START", params.subagent || params]];
    if (method === "serf/subagent/completed") return [["SUBAGENT_END", params.subagent || params]];
    return [];
  }

  window.SerfAppwire = {
    request,
    onNotification,
    onConnectionLost,
    refForSession,
    listThreads,
    search,
    listModels,
    completeDirs,
    startThread,
    readThread,
    tasks,
    startTurn,
    steer,
    queueTurn,
    drainAsSteer,
    action,
    setModel,
    forkThread,
    eventsFromThread,
    activeTurnIDFromThread,
    eventsFromNotification,
    liveItemStateSize() { return liveItemState.size; },
  };
})();
