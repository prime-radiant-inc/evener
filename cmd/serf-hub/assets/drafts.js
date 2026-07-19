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

  function sessionOf(form) {
    const id = form && form.dataset && form.dataset.sessionId
      ? String(form.dataset.sessionId).trim()
      : "";
    return id || FALLBACK_SESSION;
  }

  function keyFor(form) {
    return PREFIX + sessionOf(form);
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
  //
  // Session guard: the same form element normally gets replaced on a session
  // swap, but if it ever survives with the previous session's text still in
  // the textarea (morph/reuse, out-of-order swap), that text is the OLD
  // session's content — clear it before restoring, so a draft can never pose
  // as another session's input or be written under the wrong key.
  // isOtherSessionsDraft reports whether value is verbatim the stored draft
  // of a DIFFERENT session — the fingerprint of a leak (element survival or
  // browser form-state restore), as opposed to the user's fresh typing.
  function isOtherSessionsDraft(form, value) {
    const s = storage();
    if (!s || value === "") return false;
    const own = keyFor(form);
    try {
      for (let i = 0; i < s.length; i++) {
        const k = s.key(i);
        if (k && k.startsWith(PREFIX) && k !== own && s.getItem(k) === value) return true;
      }
    } catch (e) {
      return false;
    }
    return false;
  }

  function bind(form) {
    if (!form) return false;
    const ta = form.querySelector(".message-input");
    if (!ta) return false;
    const session = sessionOf(form);
    const bound = form.dataset.draftBoundSession || "";
    if (bound !== session && ta.value !== "") {
      if (bound !== "" || isOtherSessionsDraft(form, ta.value)) {
        ta.value = "";
      }
    }
    form.dataset.draftBoundSession = session;
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
