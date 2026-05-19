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

  function itemCount(items) {
    return Array.isArray(items) ? items.length : 0;
  }

  function imageItemCount(items) {
    if (!Array.isArray(items)) return 0;
    let n = 0;
    for (const item of items) {
      if (item && item.type === "image") n++;
    }
    return n;
  }

  function imagePlaceholder(n) {
    if (n === 1) return "[image]";
    if (n > 1) return "[" + n + " images]";
    return "";
  }

  function queuePreviewText(text, items) {
    const n = normalizeText(text);
    if (n) return n;
    return imagePlaceholder(imageItemCount(items));
  }

  function create(opts) {
    const conv = opts.conversation;
    const queueList = opts.queueList || null;
    const queueWrap = queueList ? queueList.closest("[data-queue-preview]") : null;
    const setTimeoutFn = opts.setTimeout || setTimeout;
    const clearTimeoutFn = opts.clearTimeout || clearTimeout;
    const timeoutMs = (typeof opts.timeoutMs === "number") ? opts.timeoutMs : 10000;
    const onRetry = opts.onRetry || function () {};
    let queuePendingList = null;

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

    function ensureQueuePendingList() {
      if (!queueList) return null;
      if (queuePendingList && queuePendingList.isConnected) return queuePendingList;
      const doc = queueList.ownerDocument;
      queuePendingList = doc.createElement("ul");
      queuePendingList.className = "queue-preview-list queue-preview-pending-list";
      queuePendingList.setAttribute("data-queue-pending-list", "");
      if (queueList.parentNode) {
        queueList.parentNode.insertBefore(queuePendingList, queueList.nextSibling);
      }
      return queuePendingList;
    }

    function setQueuePendingVisible(visible) {
      if (!queueWrap) return;
      if (visible) {
        queueWrap.hidden = false;
        return;
      }
      if (queueList && queueList.children.length > 0) return;
      queueWrap.hidden = true;
    }

    function hasQueueEntries() {
      for (const ent of entries.values()) {
        if (ent.method === "turn/queue") return true;
      }
      return false;
    }

    // containerFor picks the DOM parent for a given method's chip.
    // turn/queue chips belong in the queue-preview list; everything
    // else lands in the conversation pane. If a caller doesn't supply
    // a queueList, queue chips fall back to the conversation pane so
    // existing single-container callers keep working.
    function containerFor(method) {
      if (method === "turn/queue" && queueList) return ensureQueuePendingList() || queueList;
      return conv;
    }

    function register(intent) {
      nextID++;
      const id = nextID;
      const method = intent.method;
      const text = intent.text || "";
      const items = (intent.items || []).slice();
      const previewText = method === "turn/queue" ? queuePreviewText(text, items) : text;
      const el = chipForMethod(method, previewText);
      containerFor(method).appendChild(el);
      if (method === "turn/queue") setQueuePendingVisible(true);

      const timerID = setTimeoutFn(() => {
        fail({ id }, "server did not confirm");
      }, timeoutMs);

      entries.set(id, { method, text, items, previewText, el, timerID, failed: false });
      return { id };
    }

    function fail(handle, reason) {
      const ent = entries.get(handle.id);
      if (!ent) return;
      if (ent.failed) return;
      clearTimeoutFn(ent.timerID);
      ent.el.classList.remove("optimistic-pending");
      ent.el.classList.add("optimistic-failed");
      ent.failed = true;
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
        entries.delete(handle.id);
        onRetry({ method: ent.method, text: ent.text, items: ent.items.slice() });
      });
      ent.el.appendChild(retry);
    }

    function removeEntry(id) {
      const ent = entries.get(id);
      if (!ent) return;
      clearTimeoutFn(ent.timerID);
      if (ent.el.parentNode) ent.el.parentNode.removeChild(ent.el);
      entries.delete(id);
      if (ent.method === "turn/queue" && !hasQueueEntries()) setQueuePendingVisible(false);
    }

    function tryReconcile(method, params) {
      const want = normalizeText(params && params.text);
      const wantItems = itemCount(params && (params.items || params.images));
      const matches = (ent) => {
        if (ent.method !== method) return false;
        if (method === "turn/drainAsSteer") {
          // Drain matches first-come-first-served, no text comparison.
          return true;
        }
        if (!want) {
          return method === "turn/start" && !normalizeText(ent.text) && itemCount(ent.items) > 0 && wantItems > 0;
        }
        return normalizeText(ent.text) === want;
      };
      for (const preferFailed of [false, true]) {
        for (const [id, ent] of entries) {
          if (!!ent.failed !== preferFailed) continue;
          if (!matches(ent)) continue;
          removeEntry(id);
          return true;
        }
      }
      return false;
    }

    // tryReconcileQueue reconciles pending turn/queue entries against the
    // authoritative queue-preview list emitted by thread/queueChanged.
    // Each entry in previewTexts is the .text field of a queue preview
    // entry. For every pending turn/queue chip matched by a preview entry,
    // the chip is removed. Duplicate texts are consumed one-for-one so a
    // single preview entry cannot confirm multiple pending chips. Chips
    // whose text isn't in the preview are left in flight (still pending,
    // will time out if never confirmed). Returns the number reconciled.
    function tryReconcileQueue(previewTexts) {
      const wanted = new Map();
      if (Array.isArray(previewTexts)) {
        for (const t of previewTexts) {
          const n = normalizeText(t);
          if (n) wanted.set(n, (wanted.get(n) || 0) + 1);
        }
      }
      let removed = 0;
      // Snapshot ids first since removeEntry mutates the map.
      const ids = [];
      for (const [id, ent] of entries) {
        if (ent.method !== "turn/queue") continue;
        const n = normalizeText(ent.previewText || ent.text);
        const count = wanted.get(n) || 0;
        if (count <= 0) continue;
        ids.push(id);
        if (count === 1) {
          wanted.delete(n);
        } else {
          wanted.set(n, count - 1);
        }
      }
      for (const id of ids) {
        removeEntry(id);
        removed++;
      }
      return removed;
    }

    return { register, fail, tryReconcile, tryReconcileQueue, hasQueueEntries };
  }

  window.SerfAppwirePending = { create };
})();
