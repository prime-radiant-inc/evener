// pending.js — optimistic-rendering registry for the web hub
// renderer. Exposes window.SerfAppwirePending.create({...}) which
// returns an instance with register/fail/tryReconcile methods. The
// instance owns DOM nodes for each pending entry and animates them
// via the .optimistic-pending class (style.css).
//
// Architecture: the registry does not subscribe to events. It is
// called explicitly by the renderer's existing notification path
// (inside deliverNotification) after the authoritative reducer
// update applies. See spec §Architecture.
(function () {
  "use strict";

  function normalizeText(s) {
    return String(s || "").replace(/\s+/g, " ").trim();
  }

  function create(opts) {
    const conv = opts.conversation;
    const queueList = opts.queueList || null;
    const setTimeoutFn = opts.setTimeout || setTimeout;
    const clearTimeoutFn = opts.clearTimeout || clearTimeout;
    const timeoutMs = (typeof opts.timeoutMs === "number") ? opts.timeoutMs : 10000;
    const onRetry = opts.onRetry || function () {};

    let nextID = 0;
    const entries = new Map(); // id → {method, text, el, timerID}

    function chipForMethod(method, text) {
      const doc = conv.ownerDocument;
      // turn/queue chips render as <li> so they fit naturally inside the
      // queue-preview <ul> ([data-queue-list]); other chips remain <div>.
      const el = doc.createElement(method === "turn/queue" ? "li" : "div");
      switch (method) {
        case "turn/steer":
          el.className = "steering optimistic-pending";
          el.textContent = "↻ " + text;
          break;
        case "turn/drainAsSteer":
          el.className = "steering optimistic-pending";
          el.textContent = "↻ draining queue";
          break;
        case "turn/start":
          el.className = "user-message optimistic-pending";
          el.textContent = text;
          break;
        case "turn/queue":
          el.className = "queue-preview-item optimistic-pending";
          el.textContent = text;
          break;
        default:
          el.className = "optimistic-pending";
          el.textContent = text;
      }
      return el;
    }

    // containerFor picks the DOM parent for a given method's chip.
    // turn/queue chips belong in the queue-preview list; everything
    // else lands in the conversation pane. If a caller doesn't supply
    // a queueList, queue chips fall back to the conversation pane so
    // existing single-container callers keep working.
    function containerFor(method) {
      if (method === "turn/queue" && queueList) return queueList;
      return conv;
    }

    function register(intent) {
      nextID++;
      const id = nextID;
      const method = intent.method;
      const text = intent.text || "";
      const items = (intent.items || []).slice();
      const el = chipForMethod(method, text);
      containerFor(method).appendChild(el);

      const timerID = setTimeoutFn(() => {
        fail({ id }, "server did not confirm");
      }, timeoutMs);

      entries.set(id, { method, text, items, el, timerID });
      return { id };
    }

    function fail(handle, reason) {
      const ent = entries.get(handle.id);
      if (!ent) return;
      clearTimeoutFn(ent.timerID);
      ent.el.classList.remove("optimistic-pending");
      ent.el.classList.add("optimistic-failed");
      const doc = ent.el.ownerDocument;
      const reasonEl = doc.createElement("div");
      reasonEl.className = "optimistic-failed-reason";
      reasonEl.textContent = reason;
      ent.el.appendChild(reasonEl);
      const retry = doc.createElement("a");
      retry.className = "optimistic-retry";
      retry.textContent = "Retry";
      retry.href = "#";
      retry.addEventListener("click", (e) => {
        e.preventDefault();
        // Remove the failed chip before re-issuing so the user sees a
        // single fresh pending chip in its place rather than the failed
        // chip stacked beside the new optimistic one.
        if (ent.el.parentNode) ent.el.parentNode.removeChild(ent.el);
        onRetry({ method: ent.method, text: ent.text, items: ent.items.slice() });
      });
      ent.el.appendChild(retry);
      entries.delete(handle.id);
    }

    function removeEntry(id) {
      const ent = entries.get(id);
      if (!ent) return;
      clearTimeoutFn(ent.timerID);
      if (ent.el.parentNode) ent.el.parentNode.removeChild(ent.el);
      entries.delete(id);
    }

    function tryReconcile(method, params) {
      const want = normalizeText(params && params.text);
      for (const [id, ent] of entries) {
        if (ent.method !== method) continue;
        if (method === "turn/drainAsSteer") {
          // Drain matches first-come-first-served, no text comparison.
          removeEntry(id);
          return true;
        }
        if (!want) continue;
        if (normalizeText(ent.text) !== want) continue;
        removeEntry(id);
        return true;
      }
      return false;
    }

    // tryReconcileQueue reconciles pending turn/queue entries against the
    // authoritative queue-preview list emitted by thread/queueChanged.
    // Each entry in previewTexts is the .text field of a queue preview
    // entry. For every pending turn/queue chip whose normalized text
    // appears in the preview, the chip is removed. Chips whose text isn't
    // in the preview are left in flight (still pending, will time out if
    // never confirmed). Returns the number of chips reconciled.
    function tryReconcileQueue(previewTexts) {
      const wanted = new Set();
      if (Array.isArray(previewTexts)) {
        for (const t of previewTexts) {
          const n = normalizeText(t);
          if (n) wanted.add(n);
        }
      }
      let removed = 0;
      // Snapshot ids first since removeEntry mutates the map.
      const ids = [];
      for (const [id, ent] of entries) {
        if (ent.method !== "turn/queue") continue;
        if (wanted.has(normalizeText(ent.text))) ids.push(id);
      }
      for (const id of ids) {
        removeEntry(id);
        removed++;
      }
      return removed;
    }

    return { register, fail, tryReconcile, tryReconcileQueue };
  }

  window.SerfAppwirePending = { create };
})();
