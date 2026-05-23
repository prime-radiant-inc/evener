// toast.js — top-center, aria-live="polite" transient notifications.
//
// Exposes window.SerfToast.show(message, kind, opts) and .dismiss(handle).
// Toasts are inserted into #toast-region (rendered by app.html). Default
// timeout is 3000ms; opts.timeout overrides. opts.timeout = 0 disables
// auto-dismiss. Kind defaults to "info"; unknown kinds also become "info".
//
// Handles are stable opaque numbers. dismiss() is a no-op for unknown
// handles, so callers don't need to guard.
(function () {
  "use strict";

  var DEFAULT_TIMEOUT = 3000;
  var EXIT_ANIMATION_MS = 160; // must match --motion-base in style.css
  var KINDS = { success: 1, error: 1, info: 1 };

  // Active toast records: handle -> { el, dismissTimer, exitTimer }.
  var active = Object.create(null);
  var nextHandle = 1;

  function region() {
    return document.getElementById("toast-region");
  }

  function show(message, kind, opts) {
    var host = region();
    if (!host) return null; // region not in the DOM yet — silently drop

    var k = (kind && KINDS[kind]) ? kind : "info";
    var o = opts || {};
    var timeout = (typeof o.timeout === "number") ? o.timeout : DEFAULT_TIMEOUT;

    var el = document.createElement("div");
    el.className = "toast toast-" + k;
    el.setAttribute("role", k === "error" ? "alert" : "status");

    var msg = document.createElement("span");
    msg.className = "toast-message";
    msg.textContent = String(message == null ? "" : message);
    el.appendChild(msg);

    var close = document.createElement("button");
    close.type = "button";
    close.className = "toast-close";
    close.setAttribute("aria-label", "dismiss notification");
    close.textContent = "×";
    el.appendChild(close);

    host.appendChild(el);

    var handle = nextHandle++;
    var record = { el: el, dismissTimer: 0, exitTimer: 0 };
    active[handle] = record;

    close.addEventListener("click", function () { dismiss(handle); });

    if (timeout > 0) {
      record.dismissTimer = setTimeout(function () { dismiss(handle); }, timeout);
    }
    return handle;
  }

  function dismiss(handle) {
    var record = active[handle];
    if (!record) return;
    if (record.dismissTimer) {
      clearTimeout(record.dismissTimer);
      record.dismissTimer = 0;
    }
    if (record.exitTimer) return; // already dismissing

    record.el.classList.add("toast-dismissing");
    record.exitTimer = setTimeout(function () {
      if (record.el && record.el.parentNode) {
        record.el.parentNode.removeChild(record.el);
      }
      delete active[handle];
    }, EXIT_ANIMATION_MS);
  }

  // Optional convenience for callers that prefer keyword args.
  function success(message, opts) { return show(message, "success", opts); }
  function error(message, opts) { return show(message, "error", opts); }
  function info(message, opts) { return show(message, "info", opts); }

  window.SerfToast = {
    show: show,
    dismiss: dismiss,
    success: success,
    error: error,
    info: info,
  };
})();
