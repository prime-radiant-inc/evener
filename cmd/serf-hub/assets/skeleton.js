// skeleton.js — toggles data-loading on htmx swap targets so .skeleton
// pseudo-content (declared in the swapped partial) shimmers during the
// request. The attribute is set on htmx:beforeRequest and cleared on
// htmx:afterSwap or htmx:responseError / htmx:sendError. Targets are taken
// from event.detail.target when present; otherwise the event target is used.
(function () {
  "use strict";

  function targetOf(e) {
    if (e && e.detail && e.detail.target instanceof Element) return e.detail.target;
    if (e && e.target instanceof Element) return e.target;
    return null;
  }

  function set(e) {
    var t = targetOf(e);
    if (!t || !t.setAttribute) return;
    if (t.id === "sidebar") return;
    t.setAttribute("data-loading", "");
  }

  function clear(e) {
    var t = targetOf(e);
    if (!t || !t.removeAttribute) return;
    t.removeAttribute("data-loading");
  }

  document.body.addEventListener("htmx:beforeRequest", set);
  document.body.addEventListener("htmx:afterSwap", clear);
  document.body.addEventListener("htmx:responseError", clear);
  document.body.addEventListener("htmx:sendError", clear);
  document.body.addEventListener("htmx:swapError", clear);
})();
