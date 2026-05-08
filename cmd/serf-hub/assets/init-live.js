(function () {
  "use strict";
  var transcript = document.getElementById("transcript");
  if (!transcript) return;
  var sessionId = transcript.getAttribute("data-session-id") || "";
  window.SerfRenderer.init({
    sessionId: sessionId,
    transcript: transcript,
    statusBar: document.getElementById("status-bar"),
    input: document.getElementById("input-text"),
    sendBtn: document.getElementById("btn-send"),
    steerBtn: document.getElementById("btn-steer"),
    interruptBtn: document.getElementById("btn-interrupt"),
    compactBtn: document.getElementById("btn-compact"),
    clearBtn: document.getElementById("btn-clear"),
    shutdownBtn: document.getElementById("btn-shutdown"),
  });
})();
