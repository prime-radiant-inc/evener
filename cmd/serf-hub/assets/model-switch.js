// model-switch.js — wires the workspace header's model chip
// (data-model-trigger) into a working picker (kata model-switching, task 7).
// Reuses the anchored chip-picker pattern from settings-pickers.js: a
// provider-tab + model-list popover positioned under the trigger.
//
// Run-state gating uses the same signal the composer's interrupt/steer
// buttons key off (workspace.html): Status.Type == "active" AND
// ActiveTurnID set — NOT activeFlags, which serf daemons never populate.
// This module tracks that signal itself (from turn/started, turn/completed,
// thread/status/changed) rather than depending on renderer.js subscription
// ordering.
(function () {
  "use strict";

  // reasoningEffortLevels/supportsReasoning ride the thread/model/changed
  // notification because a model switch invalidates the previously cached
  // effort vocabulary. Exposed via SerfModelSwitch.getEffortState() as the
  // seam Task 8 (the effort chip) reads from. supportsReasoning starts
  // undefined (distinct from false): until a snapshot or notification tells
  // us otherwise, the model is UNKNOWN, not known-non-reasoning.
  var effortState = { reasoningEffortLevels: [], supportsReasoning: undefined };
  var currentEffort = "";
  var busy = { status: "", activeTurnId: "" };
  var modelsCache = null;

  // Fallback effort levels for a model whose ladder the hub doesn't know
  // (task 8, G8) — ported verbatim from spawn.js:1605 DEFAULT_EFFORT_LEVELS
  // so the live picker and the spawn form agree. The daemon clamps to what
  // the model actually accepts, so an over-broad list here is safe.
  var DEFAULT_EFFORT_LEVELS = ["minimal", "low", "medium", "high"];

  // effortLevels ports the spawn-form option logic (spawn.js:1608-1633):
  // supportsReasoning === false is a KNOWN answer of "no levels" (the model
  // doesn't reason at all); an absent/empty ladder on a model that DOES
  // reason (or whose support is still unknown) falls back to the default
  // vocabulary, since the daemon clamps to what's actually accepted.
  function effortLevels() {
    if (effortState.supportsReasoning === false) return [];
    if (Array.isArray(effortState.reasoningEffortLevels) && effortState.reasoningEffortLevels.length > 0) {
      return effortState.reasoningEffortLevels.slice();
    }
    return DEFAULT_EFFORT_LEVELS.slice();
  }

  function conversationEl() {
    return document.getElementById("conversation");
  }

  function sessionIdFromDom() {
    var conv = conversationEl();
    return (conv && conv.getAttribute("data-session-id")) || "";
  }

  function isBusy() {
    return busy.status === "active" && !!busy.activeTurnId;
  }

  function syncTriggerDisabled() {
    var disabled = isBusy();
    document.querySelectorAll("[data-model-trigger]").forEach(function (btn) {
      btn.disabled = disabled;
    });
  }

  function initBusyFromDom() {
    var conv = conversationEl();
    busy.status = (conv && conv.getAttribute("data-state")) || "";
    busy.activeTurnId = (conv && conv.getAttribute("data-active-turn-id")) || "";
  }

  function fetchModels() {
    if (modelsCache) return modelsCache;
    if (!window.SerfAppwire || typeof window.SerfAppwire.listModels !== "function") {
      return Promise.reject(new Error("appwire unavailable"));
    }
    modelsCache = window.SerfAppwire.listModels().then(function (list) {
      return Array.isArray(list) ? list : [];
    }).catch(function (err) {
      modelsCache = null; // allow retry on next open
      throw err;
    });
    return modelsCache;
  }

  function notice(message) {
    if (window.SerfToast) window.SerfToast.show(message, "error");
  }

  function closePicker() {
    var existing = document.querySelector(".model-switch-picker");
    if (existing) existing.remove();
  }

  function attachDismiss(picker) {
    function dismiss() {
      picker.remove();
      document.removeEventListener("click", offClick);
      document.removeEventListener("keydown", onKey);
    }
    function offClick(e) {
      var path = (e.composedPath && e.composedPath()) || [];
      if (!picker.isConnected || path.indexOf(picker) === -1) dismiss();
    }
    function onKey(e) {
      if (e.key === "Escape") { e.preventDefault(); dismiss(); }
    }
    setTimeout(function () {
      document.addEventListener("click", offClick);
      document.addEventListener("keydown", onKey);
    }, 0);
  }

  function currentModelId(trigger) {
    var el = trigger.querySelector("[data-model-display]");
    var full = (el && (el.dataset.fullModel || el.textContent)) || "";
    return full.trim();
  }

  function renderError(body, trigger, sessionId, picker) {
    body.innerHTML = "";
    var err = document.createElement("div");
    err.className = "chip-picker-error";
    err.textContent = "couldn't load models";
    body.appendChild(err);
    var retry = document.createElement("button");
    retry.type = "button";
    retry.className = "btn btn-ghost chip-picker-retry";
    retry.textContent = "retry";
    retry.addEventListener("click", function () {
      modelsCache = null;
      loadInto(body, trigger, sessionId, picker);
    });
    body.appendChild(retry);
  }

  function renderModels(body, models, trigger, sessionId, picker) {
    body.innerHTML = "";
    if (!models.length) {
      renderError(body, trigger, sessionId, picker);
      return;
    }
    var byProvider = {};
    models.forEach(function (m) {
      if (!byProvider[m.provider]) byProvider[m.provider] = [];
      byProvider[m.provider].push(m);
    });
    var providers = Object.keys(byProvider).sort();
    var current = currentModelId(trigger);

    var providerCol = document.createElement("div");
    providerCol.className = "chip-picker-providers";
    var modelCol = document.createElement("div");
    modelCol.className = "chip-picker-models";
    body.appendChild(providerCol);
    body.appendChild(modelCol);

    var activeProvider = providers[0] || "";
    providers.forEach(function (p) {
      if (byProvider[p].some(function (m) { return (p + "/" + m.model) === current; })) activeProvider = p;
    });

    function renderProviders() {
      providerCol.innerHTML = "";
      providers.forEach(function (p) {
        var el = document.createElement("div");
        el.className = "chip-picker-provider" + (p === activeProvider ? " active" : "");
        el.textContent = p;
        el.addEventListener("click", function () {
          activeProvider = p;
          renderProviders();
          renderList();
        });
        providerCol.appendChild(el);
      });
    }

    function renderList() {
      modelCol.innerHTML = "";
      (byProvider[activeProvider] || []).forEach(function (m) {
        var id = m.provider + "/" + m.model;
        var el = document.createElement("div");
        el.className = "chip-picker-model" + (id === current ? " active" : "");
        var name = document.createElement("div");
        name.className = "chip-picker-model-name";
        name.textContent = m.display_name || m.model;
        el.appendChild(name);
        el.addEventListener("click", function () {
          picker.remove();
          window.SerfAppwire.setModel(sessionId, id).catch(function (err) {
            notice((err && err.message) || "model change failed");
          });
        });
        modelCol.appendChild(el);
      });
    }

    renderProviders();
    renderList();
  }

  function loadInto(body, trigger, sessionId, picker) {
    body.innerHTML = "";
    var loading = document.createElement("div");
    loading.className = "chip-picker-loading";
    loading.textContent = "loading models…";
    body.appendChild(loading);
    fetchModels().then(function (models) {
      if (!picker.isConnected) return;
      renderModels(body, models, trigger, sessionId, picker);
    }).catch(function () {
      if (!picker.isConnected) return;
      renderError(body, trigger, sessionId, picker);
    });
  }

  function openPicker(trigger) {
    closePicker();
    if (isBusy()) return;
    var sessionId = sessionIdFromDom();
    var picker = document.createElement("div");
    picker.className = "model-switch-picker chip-picker chip-picker-wide";
    picker.style.position = "absolute";
    picker.style.zIndex = "50";
    var body = document.createElement("div");
    body.className = "chip-picker-body";
    picker.appendChild(body);

    var anchor = trigger.parentNode;
    anchor.style.position = "relative";
    anchor.appendChild(picker);
    picker.style.top = (trigger.offsetTop + trigger.offsetHeight + 4) + "px";
    picker.style.left = trigger.offsetLeft + "px";

    loadInto(body, trigger, sessionId, picker);
    attachDismiss(picker);
  }

  function renderEffortChip() {
    document.querySelectorAll("[data-effort-display]").forEach(function (el) {
      if (!currentEffort) {
        el.textContent = "";
        el.hidden = true;
        return;
      }
      el.textContent = currentEffort;
      el.hidden = false;
    });
  }

  // applySnapshot seeds effortState/currentEffort/the chip from a thread
  // snapshot (either the cold-attach thread/read below, or, in the future,
  // any other snapshot source) — the SerfThread.serf fields Task 4 added,
  // never /api/models or the appwire model/list (which carries only
  // provider+model).
  function applySnapshot(serf) {
    serf = serf || {};
    effortState.reasoningEffortLevels = Array.isArray(serf.reasoningEffortLevels) ? serf.reasoningEffortLevels.slice() : [];
    effortState.supportsReasoning = typeof serf.supportsReasoning === "boolean" ? serf.supportsReasoning : undefined;
    currentEffort = serf.reasoningEffort || "";
    renderEffortChip();
  }

  // loadEffortSnapshot cold-attaches the effort chip + vocabulary: a client
  // that just loaded the page has received no notifications yet, so the
  // only way to know the current effort/levels is to read the thread
  // snapshot once at init (task 8; spec N6 "cold-attached clients must be
  // able to render both settings ... with no prior notification").
  function loadEffortSnapshot() {
    if (!window.SerfAppwire || typeof window.SerfAppwire.readThread !== "function") return;
    var sessionId = sessionIdFromDom();
    if (!sessionId) return;
    window.SerfAppwire.readThread(sessionId, false, false).then(function (resp) {
      if (sessionIdFromDom() !== sessionId) return; // navigated away while in flight
      var thread = (resp && resp.thread) || {};
      applySnapshot(thread.serf);
    }).catch(function () {
      // No snapshot available (e.g. transport hiccup): leave the chip hidden
      // rather than showing a stale/wrong value.
    });
  }

  function applyModelChanged(data) {
    var full = data.modelProvider && data.model ? (data.modelProvider + "/" + data.model) : (data.model || "");
    document.querySelectorAll("[data-model-display]").forEach(function (el) {
      el.dataset.fullModel = full;
      el.textContent = (window.SerfSpawn && window.SerfSpawn.abbreviateModel) ? window.SerfSpawn.abbreviateModel(full) : full;
      var trigger = el.closest("[data-model-trigger]");
      if (trigger && full) trigger.setAttribute("title", full);
    });
    effortState.reasoningEffortLevels = Array.isArray(data.reasoningEffortLevels) ? data.reasoningEffortLevels.slice() : [];
    effortState.supportsReasoning = !!data.supportsReasoning;
    renderEffortChip();
  }

  function paramsMatchSession(params, sessionId) {
    if (!sessionId) return true;
    if (params.ref && window.SerfAppwire && typeof window.SerfAppwire.refForSession === "function") {
      return params.ref === window.SerfAppwire.refForSession(sessionId);
    }
    if (params.threadId) return params.threadId === sessionId;
    return true;
  }

  function handleNotification(method, params) {
    params = params || {};
    var sessionId = sessionIdFromDom();
    if (!paramsMatchSession(params, sessionId)) return;
    if (method === "thread/status/changed") {
      busy.status = (params.status && params.status.type) || "";
      syncTriggerDisabled();
      return;
    }
    if (method === "turn/started") {
      busy.activeTurnId = params.turnId || (params.turn && params.turn.id) || busy.activeTurnId || "started";
      syncTriggerDisabled();
      return;
    }
    if (method === "turn/completed") {
      busy.activeTurnId = "";
      syncTriggerDisabled();
      return;
    }
    if (method === "thread/model/changed") {
      closePicker();
      applyModelChanged(params);
      return;
    }
    if (method === "thread/reasoning-effort/changed") {
      currentEffort = params.reasoningEffort || "";
      renderEffortChip();
      return;
    }
  }

  // Sibling nav (renderer.js autoInit, model-display.js) swaps
  // #workspace/#conversation in place via htmx:afterSwap rather than a full
  // page reload; without resyncing here, busy/picker/cache state stays keyed
  // to the session that was on-screen before the swap.
  function resyncAfterSwap() {
    initBusyFromDom();
    syncTriggerDisabled();
    closePicker();
    modelsCache = null;
    loadEffortSnapshot();
  }

  var afterSwapHandlerInstalled = false;
  var inited = false;

  function init() {
    if (inited) return;
    inited = true;
    initBusyFromDom();
    syncTriggerDisabled();
    loadEffortSnapshot();
    document.addEventListener("click", function (e) {
      var trigger = e.target.closest && e.target.closest("[data-model-trigger]");
      if (!trigger) return;
      e.preventDefault();
      if (trigger.disabled || isBusy()) return;
      openPicker(trigger);
    });
    if (window.SerfAppwire && typeof window.SerfAppwire.onNotification === "function") {
      window.SerfAppwire.onNotification(handleNotification);
    }
    if (!afterSwapHandlerInstalled) {
      afterSwapHandlerInstalled = true;
      document.body.addEventListener("htmx:afterSwap", resyncAfterSwap);
    }
  }

  window.SerfModelSwitch = {
    getEffortState: function () {
      return { levels: effortState.reasoningEffortLevels.slice(), supportsReasoning: effortState.supportsReasoning };
    },
    // effortLevels is the seam search.js's "Set reasoning effort" palette
    // command reads from (task 8, G8) instead of a hardcoded vocabulary.
    effortLevels: effortLevels,
  };

  if (document.readyState === "loading") {
    document.addEventListener("DOMContentLoaded", init);
  } else {
    init();
  }
})();
