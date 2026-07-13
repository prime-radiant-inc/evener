// thread-state.js — the single definition of "is this thread busy?".
//
// The busy signal is Status.Type == "active" AND ActiveTurnID set. It is
// consulted in three places that each hold the two inputs differently: the
// composer's interrupt/steer buttons (renderer.js instance fields), the header
// model chip (model-switch.js cached busy object), and the command palette's
// model switch (search.js DOM attributes). They all call isBusy() here so the
// predicate lives in exactly one place. activeFlags is intentionally NOT
// consulted — serf daemons never populate it.
//
// Exposes window.SerfThreadState.isBusy(state, activeTurnId).
(function () {
  "use strict";

  window.SerfThreadState = {
    isBusy: function (state, activeTurnId) {
      return state === "active" && !!activeTurnId;
    },
  };
})();
