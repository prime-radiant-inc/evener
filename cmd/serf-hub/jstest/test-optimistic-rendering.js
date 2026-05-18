const fs = require("fs");
const path = require("path");
const assert = require("assert");
const { JSDOM } = require("jsdom");

const PENDING = fs.readFileSync(path.resolve(__dirname, "../assets/pending.js"), "utf8");
const APPWIRE = fs.readFileSync(path.resolve(__dirname, "../assets/appwire.js"), "utf8");

function build() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><div id="conv"></div></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://test/",
  });
  const { window } = dom;
  let lastSock = null;
  window.WebSocket = class FakeWS {
    constructor(url) {
      this.url = url;
      this.readyState = 1;
      this.listeners = {};
      this.sent = [];
      lastSock = this;
      setTimeout(() => {
        const cb = this.listeners.open && this.listeners.open[0];
        if (cb) cb({});
      }, 0);
    }
    addEventListener(name, cb) {
      (this.listeners[name] = this.listeners[name] || []).push(cb);
    }
    removeEventListener(name, cb) {
      const arr = this.listeners[name] || [];
      const idx = arr.indexOf(cb);
      if (idx >= 0) arr.splice(idx, 1);
    }
    send(payload) {
      const msg = JSON.parse(payload);
      this.lastSent = msg;
      this.sent.push(msg);
    }
    close() {}
  };
  window.eval(PENDING);
  window.eval(APPWIRE);
  return { window, getSock: () => lastSock };
}

// settleInit waits for the initialize handshake to complete by responding OK
// to it. After this returns, subsequent .lastSent reflects the caller's RPC.
async function settleInit(sock) {
  // Spin a few microtasks until initialize has been sent.
  for (let i = 0; i < 20; i++) {
    await new Promise(r => setTimeout(r, 1));
    if (sock.sent.some(m => m.method === "initialize")) break;
  }
  const init = sock.sent.find(m => m.method === "initialize");
  if (!init) throw new Error("initialize never sent");
  const cb = sock.listeners.message && sock.listeners.message[0];
  cb({ data: JSON.stringify({ jsonrpc: "2.0", id: init.id, result: {} }) });
}

async function waitForMethod(sock, method) {
  for (let i = 0; i < 50; i++) {
    await new Promise(r => setTimeout(r, 1));
    const m = sock.sent.find(x => x.method === method);
    if (m) return m;
  }
  throw new Error("never saw " + method);
}

function respondError(sock, id, code, message) {
  const cb = sock.listeners.message && sock.listeners.message[0];
  cb({ data: JSON.stringify({ jsonrpc: "2.0", id, error: { code, message } }) });
}

function respondOK(sock, id, result) {
  const cb = sock.listeners.message && sock.listeners.message[0];
  cb({ data: JSON.stringify({ jsonrpc: "2.0", id, result: result || {} }) });
}

async function runTests() {
  // Test 1: steer reject → pending chip flips to failed.
  {
    const { window, getSock } = build();
    await new Promise(r => setTimeout(r, 5));
    const conv = window.document.getElementById("conv");
    const pending = window.SerfAppwirePending.create({ conversation: conv });
    window.SerfAppwire.setPendingRegistry(pending);

    const promise = window.SerfAppwire.steer("sess-1", "turn-1", "look here").catch(e => e);
    const sock = getSock();
    await settleInit(sock);
    const steerMsg = await waitForMethod(sock, "turn/steer");
    respondError(sock, steerMsg.id, 32008, "steer is not available for this session");
    await promise;
    await new Promise(r => setTimeout(r, 5));

    const failed = conv.querySelector(".optimistic-failed");
    assert.ok(failed, "expected failed chip after Unavailable");
    console.log("ok steer_rejects_flips_pending_to_failed");
  }

  // Test 2: steer success → pending chip stays until renderer reconciles.
  {
    const { window, getSock } = build();
    await new Promise(r => setTimeout(r, 5));
    const conv = window.document.getElementById("conv");
    const pending = window.SerfAppwirePending.create({ conversation: conv });
    window.SerfAppwire.setPendingRegistry(pending);

    const promise = window.SerfAppwire.steer("sess-1", "turn-1", "go ahead");
    const sock = getSock();
    await settleInit(sock);
    const steerMsg = await waitForMethod(sock, "turn/steer");
    respondOK(sock, steerMsg.id, {});
    await promise;
    await new Promise(r => setTimeout(r, 5));

    // RPC success: pending chip is still rendered; reconciliation
    // happens when the renderer (Task 11) calls pending.tryReconcile
    // after the daemon's STEERING_INJECTED notification arrives.
    const stillPending = conv.querySelector(".optimistic-pending");
    assert.ok(stillPending, "pending chip should remain after RPC success (reconcile fires on the notification)");
    assert.ok(!conv.querySelector(".optimistic-failed"));
    console.log("ok steer_succeeds_keeps_pending_until_reconcile");
  }

  // Test 3: no registry installed → wrapper passes through and propagates errors.
  {
    const { window, getSock } = build();
    await new Promise(r => setTimeout(r, 5));
    // No registry registered.
    const promise = window.SerfAppwire.steer("sess-1", "turn-1", "x").catch(e => e);
    const sock = getSock();
    await settleInit(sock);
    const steerMsg = await waitForMethod(sock, "turn/steer");
    respondError(sock, steerMsg.id, 32008, "nope");
    const err = await promise;
    assert.ok(err && /nope/.test(String(err.message || err)), "error should propagate");
    console.log("ok no_registry_passes_through");
  }

  // Test 4: steer success then matching STEERING_INJECTED event reconciles
  // and removes the pending chip via registry.tryReconcile.
  {
    const { window, getSock } = build();
    await new Promise(r => setTimeout(r, 5));
    const conv = window.document.getElementById("conv");
    const pending = window.SerfAppwirePending.create({ conversation: conv });
    window.SerfAppwire.setPendingRegistry(pending);

    const promise = window.SerfAppwire.steer("sess-1", "turn-1", "look at this");
    const sock = getSock();
    await settleInit(sock);
    const steerMsg = await waitForMethod(sock, "turn/steer");
    respondOK(sock, steerMsg.id, {});
    await promise;
    await new Promise(r => setTimeout(r, 5));

    // Pending chip should still be there (RPC success doesn't reconcile).
    assert.ok(conv.querySelector(".optimistic-pending"), "pending chip should remain after RPC success");

    // Simulate the daemon's STEERING_INJECTED notification by calling
    // tryReconcile directly — the renderer-side hook in Task 11 will
    // forward this from deliverNotification. The boundary test just
    // confirms the registry's tryReconcile removes the placeholder.
    const matched = pending.tryReconcile("turn/steer", { text: "look at this" });
    assert.equal(matched, true);
    assert.equal(conv.querySelectorAll(".optimistic-pending").length, 0);
    console.log("ok pending_reconciles_after_steering_injected_event");
  }

  console.log("PASS test-optimistic-rendering.js");
}

runTests().catch(e => { console.error(e); process.exit(1); });
