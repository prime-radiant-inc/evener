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
    const setTimeoutFn = opts.setTimeout || setTimeout;
    const clearTimeoutFn = opts.clearTimeout || clearTimeout;
    const timeoutMs = (typeof opts.timeoutMs === "number") ? opts.timeoutMs : 10000;
    const onRetry = opts.onRetry || function () {};

    let nextID = 0;
    const entries = new Map(); // id → {method, text, el, timerID}

    function chipForMethod(method, text) {
      const doc = conv.ownerDocument;
      const el = doc.createElement("div");
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
          el.className = "queue-pending optimistic-pending";
          el.textContent = text;
          break;
        default:
          el.className = "optimistic-pending";
          el.textContent = text;
      }
      return el;
    }

    function register(intent) {
      nextID++;
      const id = nextID;
      const method = intent.method;
      const text = intent.text || "";
      const el = chipForMethod(method, text);
      conv.appendChild(el);

      const timerID = setTimeoutFn(() => {
        fail({ id }, "server did not confirm");
      }, timeoutMs);

      entries.set(id, { method, text, el, timerID });
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
        onRetry({ method: ent.method, text: ent.text });
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

    return { register, fail, tryReconcile };
  }

  window.SerfAppwirePending = { create };
})();
