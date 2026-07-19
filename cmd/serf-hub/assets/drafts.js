// drafts.js — sticky per-session composer drafts (issue #21, local phase).
//
// Unsent textarea content in the workspace composer (form[data-input-form])
// is mirrored to localStorage under a per-session key so it survives session
// swaps and full page reloads. The renderer (bindInputForm) calls
// SerfDrafts.bind(form) when a composer mounts to restore + keep the draft in
// sync, and SerfDrafts.clear(form) when a send / queue / steer / drain
// succeeds. Cross-host sync is explicitly out of scope for now; the storage
// shape is a flat per-session key so a future server-backed layer can sit
// behind this same interface.
//
// Keys: "serf-hub.draft.<sessionId>". A composer without data-session-id
// (e.g. a home / new-session surface) falls back to "serf-hub.draft.new".
// Empty or whitespace-only content removes the key — a draft that would
// never send is never stored.
(function () {
  "use strict";

  const PREFIX = "serf-hub.draft.";
  const FALLBACK_SESSION = "new";

  function keyFor(form) {
    const id = form && form.dataset && form.dataset.sessionId
      ? String(form.dataset.sessionId).trim()
      : "";
    return PREFIX + (id || FALLBACK_SESSION);
  }

  // localStorage can throw (private mode, disabled storage, quota). Drafts
  // are a convenience, never worth breaking the composer over — every access
  // is guarded and failures degrade to "no draft".
  function storage() {
    try {
      return window.localStorage;
    } catch (e) {
      return null;
    }
  }

  function read(form) {
    const s = storage();
    if (!s) return "";
    try {
      return s.getItem(keyFor(form)) || "";
    } catch (e) {
      return "";
    }
  }

  function write(form, value) {
    const s = storage();
    if (!s) return;
    try {
      if (String(value || "").trim() === "") {
        s.removeItem(keyFor(form));
      } else {
        s.setItem(keyFor(form), String(value));
      }
    } catch (e) {
      /* storage unavailable or full — keep typing, lose stickiness */
    }
  }

  function clear(form) {
    const s = storage();
    if (!s) return;
    try {
      s.removeItem(keyFor(form));
    } catch (e) {
      /* see write() */
    }
  }

  // bind restores any stored draft into the composer's textarea and mirrors
  // subsequent edits into storage. Never overwrites content the textarea
  // already has (e.g. a re-bound composer mid-typing). Returns true when a
  // draft was restored so the caller can re-autosize the textarea.
  function bind(form) {
    if (!form) return false;
    const ta = form.querySelector(".message-input");
    if (!ta) return false;
    let restored = false;
    const draft = read(form);
    if (draft !== "" && ta.value === "") {
      ta.value = draft;
      restored = true;
    }
    ta.addEventListener("input", () => write(form, ta.value));
    return restored;
  }

  window.SerfDrafts = { keyFor, read, write, clear, bind };
})();
