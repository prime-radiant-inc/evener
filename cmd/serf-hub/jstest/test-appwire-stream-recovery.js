const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");


function createWindow(overrides) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <div class="workspace-actions">
      <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
      <button data-details-trigger><span class="panel-toggle-label">details</span></button>
    </div>
    <header class="workspace-header" data-session-id="sess-live"></header>
    <div id="conversation"
         data-session-id="sess-live"
         data-state="idle"></div>
    <form data-input-form data-session-id="sess-live">
      <textarea class="message-input"></textarea>
      <button class="send-btn" type="submit">Send</button>
    </form>
  </body></html>`, {
    runScripts: "outside-only",
    pretendToBeVisual: true,
    url: "http://127.0.0.1:9180/s/sess-live",
  });

  const { window } = dom;
  window.marked = { parse: (t) => t };
  window.confirm = () => true;
  window.SerfDiagnostics = {
    classify: (payload) => payload,
    render: (payload, actions) => {
      const el = window.document.createElement("div");
      el.className = "diagnostic " + (payload.severity || "");
      el.dataset.source = payload.source || "";
      el.dataset.title = payload.title || "";
      const message = window.document.createElement("div");
      message.className = "diagnostic-message";
      message.textContent = payload.message || "";
      el.appendChild(message);
      for (const action of actions || []) {
        const button = window.document.createElement("button");
        button.className = "diagnostic-action";
        button.textContent = action.label;
        button.onclick = action.onclick;
        el.appendChild(button);
      }
      return el;
    },
  };
  window.SerfAppwire = Object.assign({
    tasks: () => Promise.resolve([]),
    refForSession: (sessionId) => "local:" + sessionId,
    eventsFromThread: () => [],
    eventsFromNotification: () => [],
  }, overrides);
  require("./load-renderer").evalRenderer(window);
  window.SerfRenderer.init(window.document.getElementById("conversation"));
  return window;
}

function wait(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function assert(cond, msg) {
  if (!cond) throw new Error(msg);
}

async function testConnectionLossClearsAndReconnects() {
  let lostHandler = null;
  let readThreadCalls = 0;
  let subscriptions = 0;
  let unsubscribes = 0;
  const window = createWindow({
    onNotification: () => {
      subscriptions++;
      return () => { unsubscribes++; };
    },
    onConnectionLost: (handler) => {
      lostHandler = handler;
      return () => {};
    },
    readThread: () => {
      readThreadCalls++;
      return Promise.resolve({
        thread: {
          id: "thread-live",
          sessionId: "sess-live",
          serf: { ref: "local:thread-live" },
          turns: [],
        },
      });
    },
  });

  await wait(30);
  assert(typeof lostHandler === "function", "renderer did not register AppWire connection loss callback");
  window.SerfRenderer.lastUserText = "retry me";
  window.SerfRenderer.updateThreadState("active");
  lostHandler(new Error("closed"));
  await wait(30);
  assert(window.SerfRenderer.liveStream === null, "connection loss should clear the AppWire stream sentinel");
  assert(window.SerfRenderer.state === "closed", "connection loss should mark renderer closed");
  // Transport drop belongs in the chrome (mockup #15 Alt A), NOT the transcript.
  const dropBanner = window.document.querySelector(".connection-banner");
  assert(dropBanner, "connection loss should dock a chrome reconnect banner");
  assert(dropBanner.classList.contains("reconnecting"), "the chrome banner is amber 'reconnecting' while recovering");
  assert(window.document.querySelector(".diagnostic") === null, "connection loss must NOT pollute the transcript with a diagnostic");
  assert(unsubscribes === 1, "connection loss should unsubscribe live notifications");
  await wait(300);
  assert(readThreadCalls >= 2, "connection loss should schedule a new thread/read");
  assert(subscriptions >= 2, "connection loss should resubscribe to live notifications");
}

async function testReadFailureClearsSentinel() {
  let readThreadCalls = 0;
  let unsubscribes = 0;
  const window = createWindow({
    onNotification: () => () => { unsubscribes++; },
    onConnectionLost: () => () => {},
    readThread: () => {
      readThreadCalls++;
      if (readThreadCalls === 1) return Promise.reject(new Error("read failed"));
      return Promise.resolve({ thread: { id: "thread-live", sessionId: "sess-live", turns: [] } });
    },
  });

  await wait(30);
  assert(window.SerfRenderer.liveStream === null, "failed thread/read should clear the AppWire stream sentinel");
  assert(unsubscribes === 1, "failed thread/read should unsubscribe live notifications");
  window.SerfRenderer.ensureLiveStream();
  await wait(30);
  assert(readThreadCalls === 2, "ensureLiveStream should retry after failed thread/read");
}

async function testReconnectDoesNotDuplicateReplay() {
  let lostHandler = null;
  let readThreadCalls = 0;
  const window = createWindow({
    onNotification: () => () => {},
    onConnectionLost: (handler) => {
      lostHandler = handler;
      return () => {};
    },
    readThread: () => {
      readThreadCalls++;
      return Promise.resolve({
        thread: {
          id: "thread-live",
          sessionId: "sess-live",
          serf: { ref: "local:thread-live" },
          turns: [{
            id: "turn_1",
            status: "completed",
            items: [
              { type: "userMessage", id: "user_1", turnId: "turn_1", text: "hello" },
              { type: "agentMessage", id: "msg_1", turnId: "turn_1", text: "hi there" },
            ],
          }],
        },
      });
    },
    eventsFromThread: (thread) => {
      const events = [["SESSION_START", { session_id: "local:thread-live", restored: true }]];
      for (const turn of thread.turns || []) {
        for (const item of turn.items || []) {
          if (item.type === "userMessage") events.push(["USER_INPUT", { text: item.text || "" }]);
          if (item.type === "agentMessage") events.push(["ASSISTANT_TEXT_START", {}], ["ASSISTANT_TEXT_END", { text: item.text || "" }]);
        }
      }
      return events;
    },
  });

  await wait(30);
  assert(readThreadCalls === 1, "initial AppWire read should run once");
  lostHandler(new Error("closed"));
  await wait(300);
  assert(readThreadCalls >= 2, "connection loss should re-read the thread");
  const userMessages = Array.from(window.document.querySelectorAll(".user-message"))
    .filter((el) => el.textContent.includes("hello"));
  const assistantMessages = Array.from(window.document.querySelectorAll(".assistant-message"))
    .filter((el) => el.textContent.includes("hi there"));
  assert(userMessages.length === 1, "reconnect should not duplicate user transcript items");
  assert(assistantMessages.length === 1, "reconnect should not duplicate assistant transcript items");
}

async function testStaleHydrationDoesNotRenderIntoNewSession() {
	let resolveRead = null;
	const window = createWindow({
		onNotification: () => () => {},
		onConnectionLost: () => () => {},
    readThread: () => new Promise((resolve) => { resolveRead = resolve; }),
    eventsFromThread: () => [["USER_INPUT", { text: "old session message" }]],
  });

  await wait(10);
  const oldConversation = window.document.getElementById("conversation");
  const newConversation = window.document.createElement("div");
  newConversation.id = "conversation";
  newConversation.dataset.sessionId = "sess-new";
  oldConversation.replaceWith(newConversation);
  window.SerfRenderer.conversation = newConversation;
  window.SerfRenderer.sessionId = "sess-new";

  resolveRead({
    thread: {
      id: "thread-old",
      sessionId: "sess-live",
      serf: { ref: "local:thread-old" },
      turns: [],
    },
  });
  await wait(30);

	assert(!newConversation.textContent.includes("old session message"), "stale AppWire hydration rendered into the new session");
}

async function testEarlyNotificationWaitsForHydratedThreadIdentity() {
	let notificationHandler = null;
	let resolveRead = null;
	const window = createWindow({
		onNotification: (handler) => {
			notificationHandler = handler;
			return () => {};
		},
		onConnectionLost: () => () => {},
		readThread: () => new Promise((resolve) => { resolveRead = resolve; }),
		eventsFromThread: () => [],
		eventsFromNotification: () => [["USER_INPUT", { text: "early codex update" }]],
	});

	await wait(10);
	notificationHandler("item/completed", {
		ref: "codex:thread-live",
		threadId: "thread-live",
	});
	resolveRead({
		thread: {
			id: "thread-live",
			sessionId: "sess-live",
			serf: { ref: "codex:thread-live" },
			turns: [],
		},
	});
	await wait(30);

	assert(window.document.body.textContent.includes("early codex update"), "early AppWire notification was dropped before hydration");
}

async function testEarlyMatchingNotificationDoesNotDuplicateHydratedItem() {
	let notificationHandler = null;
	let resolveRead = null;
	const window = createWindow({
		onNotification: (handler) => {
			notificationHandler = handler;
			return () => {};
		},
		onConnectionLost: () => () => {},
		readThread: () => new Promise((resolve) => { resolveRead = resolve; }),
		eventsFromThread: (thread) => {
			const events = [];
			for (const turn of thread.turns || []) {
				for (const item of turn.items || []) {
					if (item.type === "userMessage") events.push(["USER_INPUT", { text: item.text || "" }]);
				}
			}
			return events;
		},
		eventsFromNotification: () => [["USER_INPUT", { text: "same hydrated text" }]],
	});

	await wait(10);
	notificationHandler("item/completed", {
		ref: "local:sess-live",
		threadId: "sess-live",
		turnId: "turn_1",
		item: { id: "item_user", type: "userMessage", text: "same hydrated text" },
	});
	assert(!window.document.body.textContent.includes("same hydrated text"), "matching notification rendered before hydration");
	resolveRead({
		thread: {
			id: "sess-live",
			sessionId: "sess-live",
			serf: { ref: "local:sess-live" },
			turns: [{
				id: "turn_1",
				status: "completed",
				items: [{ id: "item_user", type: "userMessage", text: "same hydrated text" }],
			}],
		},
	});
	await wait(30);

	const rendered = Array.from(window.document.querySelectorAll(".user-message-text"))
		.filter((el) => el.textContent === "same hydrated text");
	assert(rendered.length === 1, "hydration plus buffered notification duplicated the same user item");
}

async function testBufferedDeltaReplaysForInProgressHydratedItem() {
	let notificationHandler = null;
	let resolveRead = null;
	const window = createWindow({
		onNotification: (handler) => {
			notificationHandler = handler;
			return () => {};
		},
		onConnectionLost: () => () => {},
		readThread: () => new Promise((resolve) => { resolveRead = resolve; }),
		eventsFromThread: (thread) => {
			const events = [];
			for (const turn of thread.turns || []) {
				for (const item of turn.items || []) {
					if (item.type === "agentMessage") {
						events.push(["ASSISTANT_TEXT_START", {}]);
						if (item.text) events.push(["ASSISTANT_TEXT_DELTA", { delta: item.text }]);
					}
				}
			}
			return events;
		},
		eventsFromNotification: (method, params) => {
			if (method !== "item/agentMessage/delta") return [];
			return [["ASSISTANT_TEXT_DELTA", { delta: params.delta || "" }]];
		},
	});

	await wait(10);
	notificationHandler("item/agentMessage/delta", {
		ref: "local:sess-live",
		threadId: "sess-live",
		turnId: "turn_1",
		itemId: "assistant_1",
		delta: " tail",
	});
	resolveRead({
		thread: {
			id: "sess-live",
			sessionId: "sess-live",
			serf: { ref: "local:sess-live" },
			turns: [{
				id: "turn_1",
				status: "inProgress",
				items: [{ id: "assistant_1", type: "agentMessage", status: "inProgress", text: "partial" }],
			}],
		},
	});
	await wait(30);

	const text = window.document.querySelector(".assistant-message").textContent;
	assert(text === "partial tail", "buffered delta for in-progress hydrated item was not replayed; got " + JSON.stringify(text));
}

async function testHydrationDedupeDoesNotDropReusedItemIDOnLaterTurn() {
	let notificationHandler = null;
	let resolveRead = null;
	const window = createWindow({
		onNotification: (handler) => {
			notificationHandler = handler;
			return () => {};
		},
		onConnectionLost: () => () => {},
		readThread: () => new Promise((resolve) => { resolveRead = resolve; }),
		eventsFromThread: (thread) => {
			const events = [];
			for (const turn of thread.turns || []) {
				for (const item of turn.items || []) {
					if (item.type === "userMessage") events.push(["USER_INPUT", { text: item.text || "" }]);
				}
			}
			return events;
		},
		eventsFromNotification: () => [["USER_INPUT", { text: "later turn text" }]],
	});

	await wait(10);
	notificationHandler("item/completed", {
		ref: "local:sess-live",
		threadId: "sess-live",
		turnId: "turn_2",
		item: { id: "reused_id", turnId: "turn_2", type: "userMessage", text: "later turn text" },
	});
	resolveRead({
		thread: {
			id: "sess-live",
			sessionId: "sess-live",
			serf: { ref: "local:sess-live" },
			turns: [{
				id: "turn_1",
				status: "completed",
				items: [{ id: "reused_id", turnId: "turn_1", type: "userMessage", text: "hydrated turn text" }],
			}],
		},
	});
	await wait(30);

	const rendered = Array.from(window.document.querySelectorAll(".user-message-text"))
		.map((el) => el.textContent);
	assert(rendered.includes("hydrated turn text"), "hydrated reused-id item missing");
	assert(rendered.includes("later turn text"), "buffered later-turn reused-id notification was dropped");
}

async function testBufferedTurnCompletedKeepsCompletionButSkipsHydratedItems() {
	let notificationHandler = null;
	let resolveRead = null;
	let completed = 0;
	const window = createWindow({
		onNotification: (handler) => {
			notificationHandler = handler;
			return () => {};
		},
		onConnectionLost: () => () => {},
		activeTurnIDFromThread: () => "turn_1",
		readThread: () => new Promise((resolve) => { resolveRead = resolve; }),
		eventsFromThread: (thread) => {
			const events = [["TURN_STARTED", { turnId: "turn_1" }]];
			for (const turn of thread.turns || []) {
				for (const item of turn.items || []) {
					if (item.type === "agentMessage") {
						events.push(["ASSISTANT_TEXT_START", {}], ["ASSISTANT_TEXT_END", { text: item.text || "" }]);
					}
				}
			}
			return events;
		},
		eventsFromNotification: (method, params) => {
			if (method !== "turn/completed") return [];
			const events = [["TURN_COMPLETED", { turnId: "turn_1" }]];
			completed++;
			for (const item of (params.turn && params.turn.items) || []) {
				if (item.type === "agentMessage") {
					events.push(["ASSISTANT_TEXT_START", {}], ["ASSISTANT_TEXT_END", { text: item.text || "" }]);
				}
			}
			return events;
		},
	});

	await wait(10);
	notificationHandler("turn/completed", {
		ref: "local:sess-live",
		threadId: "sess-live",
		turnId: "turn_1",
		turn: {
			id: "turn_1",
			status: "completed",
			items: [{ id: "assistant_1", type: "agentMessage", text: "hydrated answer" }],
		},
	});
	resolveRead({
		thread: {
			id: "sess-live",
			sessionId: "sess-live",
			serf: { ref: "local:sess-live" },
			turns: [{
				id: "turn_1",
				status: "completed",
				items: [{ id: "assistant_1", type: "agentMessage", status: "completed", text: "hydrated answer" }],
			}],
		},
	});
	await wait(30);

	const rendered = Array.from(window.document.querySelectorAll(".assistant-message"))
		.filter((el) => el.textContent === "hydrated answer");
	assert(completed === 1, "buffered turn/completed should still be delivered once");
	assert(window.SerfRenderer.activeTurnId === "", "buffered turn/completed did not clear active turn");
	assert(rendered.length === 1, "buffered turn/completed duplicated hydrated assistant item");
}

async function testBufferedTurnCompletedReplaysInProgressHydratedItems() {
	let notificationHandler = null;
	let resolveRead = null;
	let completed = 0;
	const window = createWindow({
		onNotification: (handler) => {
			notificationHandler = handler;
			return () => {};
		},
		onConnectionLost: () => () => {},
		activeTurnIDFromThread: () => "turn_1",
		readThread: () => new Promise((resolve) => { resolveRead = resolve; }),
		eventsFromThread: (thread) => {
			const events = [["TURN_STARTED", { turnId: "turn_1" }]];
			for (const turn of thread.turns || []) {
				for (const item of turn.items || []) {
					if (item.type === "agentMessage") {
						events.push(["ASSISTANT_TEXT_START", {}]);
						if (item.text) events.push(["ASSISTANT_TEXT_DELTA", { delta: item.text }]);
					}
				}
			}
			return events;
		},
		eventsFromNotification: (method, params) => {
			if (method !== "turn/completed") return [];
			const events = [["TURN_COMPLETED", { turnId: "turn_1" }]];
			completed++;
			for (const item of (params.turn && params.turn.items) || []) {
				if (item.type === "agentMessage") {
					events.push(["ASSISTANT_TEXT_START", {}], ["ASSISTANT_TEXT_END", { text: item.text || "" }]);
				}
			}
			return events;
		},
	});

	await wait(10);
	notificationHandler("turn/completed", {
		ref: "local:sess-live",
		threadId: "sess-live",
		turnId: "turn_1",
		turn: {
			id: "turn_1",
			status: "completed",
			items: [{ id: "assistant_1", type: "agentMessage", status: "completed", text: "final answer" }],
		},
	});
	resolveRead({
		thread: {
			id: "sess-live",
			sessionId: "sess-live",
			serf: { ref: "local:sess-live" },
			turns: [{
				id: "turn_1",
				status: "inProgress",
				items: [{ id: "assistant_1", type: "agentMessage", status: "inProgress", text: "partial" }],
			}],
		},
	});
	await wait(30);

	const rendered = Array.from(window.document.querySelectorAll(".assistant-message"))
		.map((el) => el.textContent);
	assert(completed === 1, "buffered in-progress turn/completed should be delivered once");
	assert(window.SerfRenderer.activeTurnId === "", "buffered in-progress turn/completed did not clear active turn");
	assert(rendered.includes("final answer"), "buffered turn/completed item body was dropped for in-progress hydrated item");
}

async function testStaleCapabilityRefreshErrorDoesNotUpdateNewSession() {
  let rejectRead = null;
  let readThreadCalls = 0;
  const window = createWindow({
    onNotification: () => () => {},
    onConnectionLost: () => () => {},
    readThread: () => {
      readThreadCalls++;
      if (readThreadCalls === 1) {
        return Promise.resolve({
          thread: {
            id: "thread-live",
            sessionId: "sess-live",
            serf: { ref: "local:thread-live" },
            turns: [],
          },
        });
      }
      return new Promise((_, reject) => { rejectRead = reject; });
    },
  });

  await wait(10);
  const seq = ++window.SerfRenderer.statusUpdateSeq;
  assert(window.SerfRenderer.refreshCapabilitiesForStatus("running", seq), "capability refresh should start");
  const oldConversation = window.document.getElementById("conversation");
  const newConversation = window.document.createElement("div");
  newConversation.id = "conversation";
  newConversation.dataset.sessionId = "sess-new";
  newConversation.dataset.state = "idle";
  oldConversation.replaceWith(newConversation);
  window.SerfRenderer.conversation = newConversation;
  window.SerfRenderer.sessionId = "sess-new";

  rejectRead(new Error("lost"));
  await wait(30);

  assert(newConversation.dataset.state === "idle", "stale failed capability refresh updated the new session state");
}

async function testTranscriptStatusPreferenceChangeRerendersThread() {
  let readThreadCalls = 0;
  const window = createWindow({
    onNotification: () => () => {},
    onConnectionLost: () => () => {},
    eventsFromThread: () => [
      ["SESSION_START", { session_id: "sess-live", ref: "local:sess-live" }],
      ["SYSTEM_MESSAGE", { title: "System prompt", text: "You are Serf." }],
      ["SYSTEM_LINE", { text: "SessionStart hook superpowers using-superpowers command exit 0" }],
    ],
    readThread: () => {
      readThreadCalls++;
      return Promise.resolve({
        thread: {
          id: "sess-live",
          sessionId: "sess-live",
          serf: { ref: "local:sess-live" },
          turns: [],
        },
      });
    },
  });

  await wait(30);
  assert(window.document.querySelectorAll(".system-message").length === 0, "prompt should be hidden before preference change");
  assert(window.document.querySelectorAll(".system-line").length === 0, "hook exit should be hidden before preference change");

  window.localStorage.setItem("serf-hub.transcript.systemStatus", JSON.stringify({
    promptLoaded: true,
    hookExitsNormal: true,
  }));
  window.document.dispatchEvent(new window.CustomEvent("serf-hub:transcript-system-status-changed"));
  await wait(30);

  const prompt = window.document.querySelector(".system-message");
  const hookLines = Array.from(window.document.querySelectorAll(".system-line")).map((el) => el.textContent);
  assert(readThreadCalls >= 2, "preference change should re-read the current thread");
  assert(prompt, "preference change should rerender prompt disclosure");
  assert(prompt.querySelector("summary").textContent.includes("Prompt Loaded"), "prompt disclosure should use Prompt Loaded label");
  assert(prompt.querySelector(".steering-body").textContent.includes("You are Serf."), "prompt disclosure should include full prompt");
  assert(hookLines.length === 1 && hookLines[0].includes("exit 0"), "preference change should rerender normal hook exit");
}

(async () => {
	await testConnectionLossClearsAndReconnects();
	await testReadFailureClearsSentinel();
	await testReconnectDoesNotDuplicateReplay();
	await testStaleHydrationDoesNotRenderIntoNewSession();
	await testEarlyNotificationWaitsForHydratedThreadIdentity();
	await testEarlyMatchingNotificationDoesNotDuplicateHydratedItem();
	await testBufferedDeltaReplaysForInProgressHydratedItem();
	await testHydrationDedupeDoesNotDropReusedItemIDOnLaterTurn();
	await testBufferedTurnCompletedKeepsCompletionButSkipsHydratedItems();
	await testBufferedTurnCompletedReplaysInProgressHydratedItems();
	await testStaleCapabilityRefreshErrorDoesNotUpdateNewSession();
	await testTranscriptStatusPreferenceChangeRerendersThread();
	console.log("PASS: appwire renderer stream recovery");
	process.exit(0);
})().catch((err) => {
  console.error("FAIL: " + err.message);
  process.exit(1);
});
