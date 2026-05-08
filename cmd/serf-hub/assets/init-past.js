(function () {
  "use strict";
  var transcript = document.getElementById("transcript");
  if (!transcript) return;
  var sessionId = transcript.getAttribute("data-session-id") || "";
  var replayUrl = transcript.getAttribute("data-replay-url") || "";
  window.SerfRenderer.init({
    sessionId: sessionId,
    transcript: transcript,
    statusBar: document.createElement("div"),
    input: document.createElement("textarea"),
    sendBtn: document.createElement("button"),
    steerBtn: document.createElement("button"),
    interruptBtn: document.createElement("button"),
    compactBtn: document.createElement("button"),
    clearBtn: document.createElement("button"),
    shutdownBtn: document.createElement("button"),
    replayUrl: replayUrl,
    readOnly: true,
  });
})();
