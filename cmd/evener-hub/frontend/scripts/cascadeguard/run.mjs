// Real production SPA only. Go creates the delegates through actual tools and
// passes the public identities; this driver never seeds frontend stores or RPCs.
import assert from "node:assert/strict";
import { existsSync, readFileSync, watch, writeFileSync } from "node:fs";
import path from "node:path";
import { Driver } from "../skillguard/run.mjs";
import { evaluate, navigateTo } from "../browserGuardCdp.mjs";

const fixture = JSON.parse(readFileSync(0, "utf8"));
const driver = new Driver(fixture);
const q = JSON.stringify;
const column = (ref) => `[data-testid="cascade-column"][data-scope-ref=${q(ref)}]`;
const row = (edge) => `[data-activity-anchor=${q(`delegate:${JSON.stringify([edge.childRef, edge.delegateId])}`)}]`;
const wait = (expression, label) => {
  new Function(`return (${expression})`);
  return driver.waitPage(expression, { label });
};
const read = (expression) => evaluate(driver.send, expression);
const scope = (ref) => `[data-scope-ref=${q(ref)}]`;
const peekChip = (ref, kind) => `${scope(ref)} button[aria-label^=${q(`${kind},`)}][aria-label$=${q(`- peek at ${ref}`)}]`;
const scroll = (ref) => `${column(ref)} [data-testid="transcript-virtual-list"] > div`;
const stagedPNG = 'button[aria-label="View staged.png"] img';
const errors = [];
const consoleEvents = [];
const frames = [];
let failed = false;

async function capture(name) {
  const paint = await read(`([...document.querySelectorAll('[data-testid="transcript-virtual-list"]')]).map(list => {
    const box = node => { const r = node.getBoundingClientRect(); return { x:r.x, y:r.y, width:r.width, height:r.height, top:r.top, bottom:r.bottom }; };
    const port = list.firstElementChild;
    const bounds = port.getBoundingClientRect();
    const chain = [];
    for (let node = port; node && chain.length < 12; node = node.parentElement) {
      const style = getComputedStyle(node);
      chain.push({ tag:node.tagName, class:node.className, box:box(node), display:style.display, height:style.height, overflow:style.overflow, opacity:style.opacity, visibility:style.visibility, transform:style.transform });
    }
    return { ref:list.closest('[data-scope-ref]')?.dataset.scopeRef, scrollTop:port.scrollTop, scrollHeight:port.scrollHeight, clientHeight:port.clientHeight, chain,
      rows:[...port.querySelectorAll('[data-index]')].map(node => { const r = node.getBoundingClientRect(); return { index:node.dataset.index, box:box(node), intersects:r.bottom > bounds.top && r.top < bounds.bottom, textLength:node.textContent.length }; }) };
  })`);
  writeFileSync(path.join(fixture.artifactDir, `${name}-paint.json`), JSON.stringify(paint, null, 2));
  writeFileSync(path.join(fixture.artifactDir, `${name}-native-hits.json`), JSON.stringify(await read('window.__cascadeNativeHits'), null, 2));
  writeFileSync(path.join(fixture.artifactDir, `${name}.html`), await read("document.documentElement.outerHTML"));
  const screenshot = await driver.send("Page.captureScreenshot", { format: "png" });
  writeFileSync(path.join(fixture.artifactDir, `${name}.png`), Buffer.from(screenshot.result.data, "base64"));
  return paint;
}

async function key(key, keyCode) {
  for (const type of ["keyDown", "keyUp"]) {
    await driver.send("Input.dispatchKeyEvent", { type, key, code: key, windowsVirtualKeyCode: keyCode, nativeVirtualKeyCode: keyCode,
      ...(key === "Enter" && type === "keyDown" ? { text: "\r", unmodifiedText: "\r" } : {}) });
  }
}

async function upload(name) {
  const tree = await driver.send("DOM.getDocument");
  const node = await driver.send("DOM.querySelector", { nodeId: tree.result.root.nodeId, selector: 'input[type="file"]' });
  assert.ok(node.result.nodeId, "real source file picker");
  await driver.send("DOM.setFileInputFiles", { nodeId: node.result.nodeId, files: [path.join(fixture.artifactDir, name)] });
}

async function holdCanvasCompletion() {
  await read(`(() => {
    const native = HTMLCanvasElement.prototype.toBlob;
    window.__cascadeEncode = { release: null, encoded: false };
    HTMLCanvasElement.prototype.toBlob = function(callback, ...args) {
      HTMLCanvasElement.prototype.toBlob = native;
      return native.call(this, blob => {
        window.__cascadeEncode.encoded = blob !== null;
        window.__cascadeEncode.release = success => callback(success ? blob : null);
      }, ...args);
    };
  })()`);
}

async function openPeek(ref, kind) {
  await driver.click(peekChip(ref, kind));
  await wait('document.querySelector("[data-testid=activity-peek]") !== null', `${kind} ancestor peek`);
}

async function pendingImage(name, success) {
  const rejectedMarker = `[image ${(await driver.composerState(fixture.rootRef)).tiles + 1}]`;
  await holdCanvasCompletion();
  await upload(name);
  await wait("window.__cascadeEncode?.encoded && window.__cascadeEncode.release !== null", "native canvas encoded before completion hold");
  await wait(`document.querySelector('[role="img"][aria-label=${q(`${name} (still processing)`)}]') !== null`, "original pending tile");
  await driver.focusComposer(fixture.rootRef);
  await driver.typeText(fixture.rootRef, ` CASCADE_NEWER_IMAGE_${success ? "SUCCESS" : "FAILURE"}`);
  const newerDraft = await driver.composerState(fixture.rootRef);
  await driver.click('[data-testid="statusbar"] button[aria-label^="Agents,"]');
  await drill(fixture.edges[0], 1);
  assert.equal(await read('document.querySelectorAll("input[type=file]").length'), 0, "cascade is read only while source encode is pending");
  await read(`window.__cascadeEncode.release(${success})`);
  await driver.clickByText("Return to previous view");
  await wait(`document.querySelector(${q(driver.composerSelector(fixture.rootRef))}) !== null`, "return pending source recipient");
  if (success) {
    await wait(`document.querySelector('button[aria-label=${q(`View ${name}`)}] img')?.src.startsWith('data:image/png;base64,')`, "late real PNG completion in original source");
  } else {
    await wait(`${driver.toastExpr()}.includes(${q(`${name} (image decode failed)` )})`, "explicit native canvas failure toast");
    assert.equal(await read(`document.querySelector('button[aria-label=${q(`Remove ${name}`)}]') !== null`), false, "failed original tile removed");
  }
  const expectedText = success ? newerDraft.text : newerDraft.text.replace(rejectedMarker, "");
  assert.equal((await driver.composerState(fixture.rootRef)).text, expectedText, "late image completion never rolls back a newer source draft");
  driver.milestone(success ? "pending-image-success" : "pending-image-failure", { name, recipient: fixture.rootRef });
}

async function holdStorageAcknowledgement(ref, input) {
  await read(`(() => {
    const nativeAdd = IDBObjectStore.prototype.add;
    const nativeListen = IDBTransaction.prototype.addEventListener;
    const held = window.__cascadeStorage = { committed: false, record: null, release: null };
    let selected;
    IDBTransaction.prototype.addEventListener = function(type, listener, options) {
      if (type !== 'complete' || this.mode !== 'readwrite' || this.db.name !== 'evener-mutation-outbox' || !this.objectStoreNames.contains('outbox')) return nativeListen.call(this, type, listener, options);
      return nativeListen.call(this, type, event => {
        if (this !== selected) return listener.call(this, event);
        held.committed = true;
        held.release = () => listener.call(this, event);
      }, options);
    };
    IDBObjectStore.prototype.add = function(record, ...args) {
      if (this.name === 'outbox' && this.transaction.db.name === 'evener-mutation-outbox' && record.targetRef === ${q(ref)} && (record.composerText === ${q(input)} || record.payload?.input?.some(item => item.text === ${q(input)}))) {
        selected = this.transaction;
        held.record = structuredClone(record);
        IDBObjectStore.prototype.add = nativeAdd;
        IDBTransaction.prototype.addEventListener = nativeListen;
      }
      return nativeAdd.call(this, record, ...args);
    };
  })()`);
}

async function providerHeld() {
  await new Promise((resolve, reject) => {
    const file = path.join(fixture.artifactDir, "provider-held.json");
    const watcher = watch(fixture.artifactDir, () => check());
    const timer = setTimeout(() => finish(new Error("real provider never acknowledged its held source input")), 15000);
    const finish = (error) => { clearTimeout(timer); watcher.close(); error ? reject(error) : resolve(); };
    const check = () => {
      if (!existsSync(file)) return;
      try {
        const value = JSON.parse(readFileSync(file, "utf8"));
        assert.deepEqual(value, { role: 0, input: "CASCADE_BUSY_INPUT" });
        finish();
      } catch (error) { if (!(error instanceof SyntaxError)) finish(error); }
    };
    watcher.on("error", finish);
    check();
  });
}

const readableGeometrySettled = `(() => {
  const columns = [...document.querySelectorAll('[data-testid="cascade-column"]')];
  return columns.length === 2 && Math.abs(columns[0].getBoundingClientRect().width - 400) < .75 && columns[1].getBoundingClientRect().width >= 439.5
    && columns.every(node => getComputedStyle(node).transform === 'none');
})()`;

async function observeGeometryAndFocus(ref) {
  await wait(readableGeometrySettled, "settled geometry before live status updates");
  await read(`(() => {
    document.querySelector(${q(peekChip(ref, "Tasks"))}).focus();
    window.__cascadeFocused = document.activeElement;
    window.__cascadeGeometryChanges = [];
    window.__cascadeGeometryObserver = new MutationObserver(records => {
      for (const record of records) if (record.target.matches('[data-scope-ref]')) window.__cascadeGeometryChanges.push({ ref: record.target.dataset.scopeRef, style: record.target.getAttribute('style') });
    });
    window.__cascadeGeometryObserver.observe(document.querySelector('[data-pane-scaffold="cascade"]'), { subtree: true, attributes: true, attributeFilter: ['style'] });
  })()`);
}

async function sourceMutationJourney() {
  const ref = fixture.rootRef;
  const busyInput = "CASCADE_ROLE_0_SENTINEL CASCADE_BUSY_INPUT";
  const queueInput = "CASCADE_ROLE_0_SENTINEL CASCADE_QUEUE_INPUT";
  await driver.clearComposerDraft(ref);
  await driver.focusComposer(ref);
  await driver.typeText(ref, busyInput);
  await holdStorageAcknowledgement(ref, busyInput);
  const after = frames.length;
  await driver.clickSubmit(ref, { text: busyInput, chips: [], tiles: 0 });
  await wait("window.__cascadeStorage.committed && window.__cascadeStorage.release !== null", "real outbox commit with application acknowledgement held");
  await providerHeld();
  const held = await read("window.__cascadeStorage.record");
  assert.ok(held?.clientMutationId, "real committed source mutation retains its identity");
  assert.ok(held.payload.input.some(item => item.text === busyInput), "actual committed record contains the original source input");
  assert.equal(held.targetRef, ref);
  assert.equal(held.method, "turn/start");
  await driver.click('[data-testid="statusbar"] button[aria-label^="Agents,"]');
  await drill(fixture.edges[0], 1);
  await driver.clickByText("Return to previous view");
  await wait(`document.querySelector(${q(driver.composerSelector(ref))}) !== null`, "return source while storage receipt is unresolved");
  await driver.focusComposer(ref);
  await driver.typeText(ref, " CASCADE_NEWER_STORAGE_DRAFT");
  const newerDraft = await driver.composerState(ref);
  await read("window.__cascadeStorage.release()");
  await providerHeld();
  await waitFrames(() => sent(after).some(frame => frame.method === "turn/start" && frame.params.clientMutationId === held.clientMutationId && answered(frame)), "original mutation accepted once");
  assert.equal((await driver.composerState(ref)).text, newerDraft.text, "receipt cannot clear a newer returned draft");
  driver.milestone("unresolved-storage-source", { clientMutationId: held.clientMutationId, recipient: ref });

  await driver.clearComposerDraft(ref);
  await driver.focusComposer(ref);
  await driver.typeText(ref, queueInput);
  await driver.clickSubmit(ref, { text: queueInput, chips: [], tiles: 0 });
  await wait(`(() => { const strip = ${driver.queueStripExpr(ref)}; return strip?.rows.some(row => row.text.includes('CASCADE_QUEUE_INPUT')); })()`, "real source queue while original provider call is held");
  await waitFrames(() => sent(after).some(frame => frame.method === "turn/queue" && frame.params.ref === ref && answered(frame)), "real queue acknowledgement");
  const queued = sent(after).find(frame => frame.method === "turn/queue" && frame.params.ref === ref);
  assert.ok(queued.params.clientMutationId && queued.params.clientMutationId !== held.clientMutationId);
  await wait(`document.querySelector(${q(driver.composerSelector(ref))})?.querySelector('[role="textbox"]').textContent === ''`, "queue receipt clears only the submitted source draft");
  await driver.focusComposer(ref);
  await driver.typeText(ref, "CASCADE_UNSENT_AFTER_QUEUE");
  await driver.click('[data-testid="statusbar"] button[aria-label^="Agents,"]');
  await drill(fixture.edges[0], 1);
  await observeGeometryAndFocus(ref);
  const releasedAfter = frames.length;
  driver.control("release-provider");
  await waitFrames(() => frames.slice(releasedAfter).some(frame => frame.direction === "Network.webSocketFrameReceived" && frame.method === "thread/status/changed" && frame.params.ref === ref && frame.params.status.type === "idle"), "actual source status settles after queued provider delivery");
  assert.equal(await read("document.activeElement === window.__cascadeFocused"), true, "actual source status changes preserve focused element");
  assert.deepEqual(await read("window.__cascadeGeometryChanges"), [], "actual provider completion never changes geometry");
  await read("window.__cascadeGeometryObserver.disconnect()");
  driver.milestone("live-status-stable-geometry");
  await driver.clickByText("Return to previous view");
  await wait(`document.querySelector(${q(driver.composerSelector(ref))}) !== null`, "return original queued source");
  await wait(`(() => { const strip = ${driver.queueStripExpr(ref)}; return !strip || strip.rows.length === 0; })()`, "original queue drains");
  assert.equal((await driver.composerState(ref)).text, "CASCADE_UNSENT_AFTER_QUEUE");
  for (const mutation of [held, queued.params]) {
    const requests = sent(after).filter(frame => frame.params?.clientMutationId === mutation.clientMutationId);
    assert.equal(requests.length, 1, "promotion and return never duplicate original mutation identity");
    assert.equal(requests[0].params.ref, ref, "original recipient is retained");
  }
  driver.milestone("queued-source-single-delivery", { clientMutationId: queued.params.clientMutationId, recipient: ref });
  await capture("queued-source-single-delivery");
}

// Both rows come from the root's real project catalog. A native row click,
// selected by its description, distinguishes identities with identical labels.
async function completeOverlap(ref, kind) {
  const description = `Cascade overlap ${kind} fixture`;
  const point = await wait(`(() => {
    const button = [...document.querySelectorAll('[data-testid="composer-slash-menu"] button')].find(node => node.textContent.includes(${q(description)}));
    if (!button) return null;
    const r = button.getBoundingClientRect(); return { x:r.x + r.width / 2, y:r.y + r.height / 2 };
  })()`, `real overlapping ${kind} catalog row`);
  await driver.clickAt(point.x, point.y);
  await wait(`document.querySelector(${q(driver.composerSelector(ref))})?.querySelector('[data-${kind}-name="cascade-overlap"]') !== null && document.querySelector('[data-testid="composer-slash-menu"]') === null`, `selected overlapping ${kind} atom`);
}

function overlapMentions(text) {
  const first = text.indexOf('/cascade-overlap');
  const second = text.indexOf('/cascade-overlap', first + 1);
  assert.ok(first >= 0 && second > first && text.indexOf('/cascade-overlap', second + 1) > second, 'two atoms plus same-spelling inert prose');
  return [{ kind: 'command', name: 'cascade-overlap', offset: first }, { kind: 'skill', name: 'cascade-overlap', offset: second }];
}

async function assertOverlap(ref, text, label) {
  const state = await driver.composerState(ref);
  assert.equal(state.text, text, `${label}, exact UTF-16 text and inert prose`);
  assert.deepEqual(state.chips, ['/cascade-overlap'], `${label}, only the selected skill is a skill atom`);
  const atoms = await read(`(() => {
    const editor = ${driver.editorExpr(ref)};
    return [...editor.querySelectorAll('[data-command-name], [data-skill-name]')].map(node => {
      const range = document.createRange(); range.selectNodeContents(editor); range.setEndBefore(node);
      return { kind:node.hasAttribute('data-command-name') ? 'command' : 'skill', name:node.dataset.commandName ?? node.dataset.skillName, offset:range.toString().length };
    });
  })()`);
  assert.deepEqual(atoms, overlapMentions(text), `${label}, atom kind and exact text-owned offsets`);
  const stored = await read(`JSON.parse(localStorage.getItem(${q(`evener.composer.draft.v2.${ref}`)}))`);
  assert.deepEqual(stored, { text, skillNames:['cascade-overlap'], commandNames:['cascade-overlap'], mentions:overlapMentions(text) }, `${label}, complete real persisted draft`);
}

async function mixedSourceJourney() {
  const ref = fixture.rootRef;
  await driver.clearComposerDraft(ref);
  await driver.focusComposer(ref);
  await driver.typeText(ref, '🙂 CASCADE_ROLE_0_SENTINEL CASCADE_MIXED_INPUT ');
  await upload('mixed-first.png');
  await wait("document.querySelector('button[aria-label=\"View mixed-first.png\"] img')?.src.startsWith('data:image/png;base64,')", 'first real overlap image');
  await driver.focusComposer(ref);
  await driver.typeText(ref, ' /cascade-overlap');
  await completeOverlap(ref, 'command');
  await holdCanvasCompletion();
  await upload('mixed-failure.png');
  await wait('window.__cascadeEncode?.encoded && window.__cascadeEncode.release !== null', 'real middle-image native callback held');
  await driver.focusComposer(ref);
  await driver.typeText(ref, ' /cascade-overlap');
  await completeOverlap(ref, 'skill');
  await upload('mixed-last.png');
  await wait("document.querySelector('button[aria-label=\"View mixed-last.png\"] img')?.src.startsWith('data:image/png;base64,')", 'last real overlap image');
  await driver.focusComposer(ref);
  await driver.typeText(ref, ' /cascade-overlap');
  await key('Escape', 27);
  const before = await driver.composerState(ref);
  const markers = before.text.match(/\[image \d+\]/g) ?? [];
  assert.equal(markers.length, 3, 'each real upload inserted its own marker');
  assert.equal(new Set(markers).size, 3, 'retained source allocates unique image markers');
  const [firstMarker, failedMarker, lastMarker] = markers;
  await assertOverlap(ref, before.text, 'before detached image settlement');
  const imageBytes = await read("['mixed-first.png','mixed-last.png'].map(name => document.querySelector(`button[aria-label=\"View ${name}\"] img`).src)");
  await driver.click('[data-testid="statusbar"] button[aria-label^="Agents,"]');
  await drill(fixture.edges[0], 1);
  await read('window.__cascadeEncode.release(false)');
  await wait(`${driver.toastExpr()}.includes('mixed-failure.png (image decode failed)')`, 'actual failed decode settles while source editor is absent');
  await driver.clickByText('Return to previous view');
  await wait(`document.querySelector(${q(driver.composerSelector(ref))}) !== null`, 'return overlapping detached image draft');
  const settledText = before.text.replace(failedMarker, '');
  await assertOverlap(ref, settledText, 'after detached image settlement and Return');
  assert.equal((await driver.composerState(ref)).tiles, 2, 'only failed middle image is removed');
  assert.deepEqual(await read("['mixed-first.png','mixed-last.png'].map(name => document.querySelector(`button[aria-label=\"View ${name}\"] img`).src)"), imageBytes, 'exact retained PNG bytes');
  driver.milestone('mixed-detached-image-return', { recipient:ref, mentions:overlapMentions(settledText) });

  await holdStorageAcknowledgement(ref, settledText);
  const after = frames.length;
  await driver.clickSubmit(ref, { text:settledText, chips:['/cascade-overlap'], tiles:2 });
  await wait('window.__cascadeStorage.committed && window.__cascadeStorage.release !== null', 'real mixed native outbox acknowledgement held');
  const held = await read('window.__cascadeStorage.record');
  assert.equal(held.targetRef, ref, 'mixed record retains original recipient');
  assert.equal(held.method, 'turn/start');
  assert.equal(held.composerText, settledText);
  assert.deepEqual(held.composerMentions, overlapMentions(settledText));
  assert.deepEqual(held.payload.input.filter(item => ['command','skill'].includes(item.type)).map(({type,name}) => ({type,name})), [{type:'skill',name:'cascade-overlap'}, {type:'command',name:'cascade-overlap'}], 'actual input keeps both identities in public wire order');
  assert.equal(held.attachments.length, 2, 'actual outbox retains both image payloads');
  assert.ok(held.clientMutationId, 'native committed mutation has an identity');
  await driver.click('[data-testid="statusbar"] button[aria-label^="Agents,"]');
  await drill(fixture.edges[0], 1);
  await driver.clickByText('Return to previous view');
  await wait(`document.querySelector(${q(driver.composerSelector(ref))}) !== null`, 'return mixed source while receipt is held');
  await driver.focusComposer(ref);
  await driver.typeText(ref, ' CASCADE_NEWER_MIXED_DRAFT');
  const newer = await driver.composerState(ref);
  await driver.click('[data-testid="statusbar"] button[aria-label^="Agents,"]');
  await drill(fixture.edges[0], 1);
  await read('window.__cascadeStorage.release()');
  await waitFrames(() => sent(after).some(frame => frame.params?.clientMutationId === held.clientMutationId && answered(frame)), 'mixed original mutation acknowledged exactly once');
  await driver.clickByText('Return to previous view');
  await wait(`document.querySelector(${q(driver.composerSelector(ref))}) !== null`, 'return after detached mixed receipt');
  await wait(`document.querySelector(${q(driver.composerSelector(ref))})?.querySelectorAll('[data-testid="attachment-tile"]').length === 0`, 'submitted images retire after native acknowledgement');
  const cleaned = newer.text.replace(firstMarker, '').replace(lastMarker, '');
  await assertOverlap(ref, cleaned, 'after independent submitted marker cleanup and Return');
  const requests = sent(after).filter(frame => frame.params?.clientMutationId === held.clientMutationId);
  assert.equal(requests.length, 1, 'mixed promotion and Return never duplicate delivery');
  assert.equal(requests[0].params.ref, ref);
  driver.milestone('mixed-held-storage-return', { recipient:ref, clientMutationId:held.clientMutationId, mentions:overlapMentions(cleaned) });
  await capture('mixed-held-storage-return');
}

const layoutExpr = "JSON.parse(localStorage.getItem('evener.workspace.layout.v2') || 'null')";

function placement(layout, id) {
  function find(node, path = []) {
    if (node.type === "leaf") return node.data.views.includes(id) ? { path, size: node.size, group: node.data } : null;
    for (let index = 0; index < node.data.length; index++) {
      const found = find(node.data[index], [...path, index]);
      if (found) return found;
    }
    return null;
  }
  const found = find(layout.grid.root);
  assert.ok(found, `actual saved grid contains ${id}`);
  return found;
}

async function clickColumnAction(ref, text) {
  const point = await wait(`(() => {
    const button = [...document.querySelectorAll(${q(`${column(ref)} button`)})].find(node => node.textContent.trim() === ${q(text)});
    if (!button) return null;
    button.scrollIntoView({ block: 'center', inline: 'nearest' });
    const r = button.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  })()`, `${text} in actual ${ref} column`);
  await driver.clickAt(point.x, point.y);
}

async function installLateAncestryHold() {
  await driver.send("Page.addScriptToEvaluateOnNewDocument", { source: `(() => {
    const Native = window.WebSocket;
    const hold = window.__cascadeLate = { active: true, replies: [], requests: new Set() };
    const methods = new Set(['evener/thread/activity/read', 'evener/thread/delegates/list', 'evener/thread/jobs/list', 'evener/thread/watches/list']);
    window.WebSocket = class extends Native {
      send(data) {
        const request = JSON.parse(data);
        if (hold.active && methods.has(request.method) && request.params.ref === ${q(fixture.refs[6])}) hold.requests.add(request.id);
        return super.send(data);
      }
      set onmessage(listener) {
        super.onmessage = listener === null ? null : event => {
          const response = JSON.parse(event.data);
          if (hold.active && hold.requests.has(response.id)) {
            hold.replies.push({ id: response.id, context: response.result?.context, release: () => listener.call(this, event) });
          } else listener.call(this, event);
        };
      }
      get onmessage() { return super.onmessage; }
    };
  })()` });
}

async function reloadAndMobileJourney() {
  await driver.clearComposerDraft(fixture.rootRef);
  await driver.focusComposer(fixture.rootRef);
  await driver.typeText(fixture.rootRef, "CASCADE_UNSENT_RELOAD_SOURCE /cascade-source");
  await driver.completeSkill(fixture.rootRef, "cascade-source");
  const source = await driver.composerState(fixture.rootRef);
  assert.deepEqual(source.chips, ["/cascade-source"]);
  await driver.click('[data-testid="statusbar"] button[aria-label^="Agents,"]');
  await drill(fixture.edges[0], 1);
  await clickColumnAction(fixture.childRef, "Open conversation");
  await wait(`document.querySelector(${q(driver.composerSelector(fixture.childRef))}) !== null`, "explicit independent child conversation");
  await driver.focusComposer(fixture.childRef);
  await driver.typeText(fixture.childRef, "CASCADE_UNSENT_UNRELATED_DRAFT");
  const unrelatedId = await read(`${driver.paneScopeExpr(fixture.childRef)}.querySelector('[data-testid="statusbar"] [data-pane-id]').dataset.paneId`);
  assert.ok(unrelatedId && unrelatedId !== fixture.sourcePaneId);
  assert.equal(await read("document.querySelectorAll('.dv-tab').length"), fixture.sourceTabCount + 1, "only explicit Open creates an independent tab");
  fixture.sourceTabCount++;
  await clickColumnAction(fixture.rootRef, "Open conversation");
  await wait(`document.querySelector(${q(driver.composerSelector(fixture.rootRef))}) !== null`, "source ordinary view is focused in its existing panel");
  const baseline = await wait(`(() => {
    const saved = ${layoutExpr};
    return saved?.panels[${q(fixture.sourcePaneId)}]?.params?.paneType === 'session' && saved.panels[${q(unrelatedId)}] ? saved : null;
  })()`, "native debounce saved both actual ordinary panels");
  const unrelatedPanel = baseline.panels[unrelatedId];
  const unrelatedPlacement = placement(baseline, unrelatedId);
  await driver.click('[data-testid="statusbar"] button[aria-label^="Agents,"]');
  await drill(fixture.edges[0], 1);
  for (let level = 2; level <= 6; level++) await drill(fixture.edges[level - 1], level);
  const saved = await wait(`(() => {
    const saved = ${layoutExpr}, panel = saved?.panels[${q(fixture.sourcePaneId)}];
    return panel?.params?.paneType === 'sessionZoom' && panel.params.paneParams.ref === ${q(fixture.refs[6])} ? saved : null;
  })()`, "native debounce persisted selected leaf intent");
  const expectedIntent = {
    ref: fixture.refs[6], source: { type: "session", params: { ref: fixture.rootRef } },
    edges: fixture.edges.map(({ ownerRef, childRef, delegateId }) => ({ ownerRef, childRef, delegateId })),
  };
  assert.deepEqual(saved.panels[fixture.sourcePaneId].params.paneParams, expectedIntent, "saved intent matches independently supplied real edges and original source");
  assert.deepEqual(saved.panels[unrelatedId], unrelatedPanel);
  assert.deepEqual(placement(saved, unrelatedId), unrelatedPlacement);
  assert.equal(JSON.stringify(saved).includes("data:image"), false, "layout never stores source image bytes");
  writeFileSync(path.join(fixture.artifactDir, "before-reload-layout.json"), JSON.stringify(saved, null, 2));
  await installLateAncestryHold();
  await driver.send("Page.reload", { ignoreCache: true });
  await wait(`document.querySelector(${q(column(fixture.refs[6]))}) !== null && document.querySelector('[data-pane-scaffold="cascade"]').textContent.includes('Earlier ancestry is incomplete')`, "saved path paints before actual ancestry delivery");
  const actualContext = await wait(`window.__cascadeLate.replies.find(reply => reply.context?.ancestryKnown && reply.context.ancestors.length === 6)?.context`, "actual known leaf ancestry reply is held at native message delivery");
  assert.deepEqual(actualContext.ancestors.map(ancestor => ancestor.ref), fixture.refs.slice(0, 6));
  assert.deepEqual(await read('[...document.querySelectorAll("[data-scope-ref]")].map(node => node.dataset.scopeRef)'), fixture.refs, "unknown ancestry preserves six saved edges");
  assert.equal(await read("document.querySelectorAll('.dv-tab').length"), fixture.sourceTabCount);
  await observeGeometryAndFocus(fixture.refs[5]);
  await read("(() => { const hold = window.__cascadeLate; hold.active = false; for (const reply of hold.replies) reply.release(); })()");
  await wait("!document.querySelector('[data-pane-scaffold=\"cascade\"]').textContent.includes('Earlier ancestry is incomplete')", "unchanged actual reply establishes authoritative ancestry");
  assert.equal(await read("document.activeElement === window.__cascadeFocused"), true, "late ancestry preserves exact focus");
  assert.deepEqual(await read("window.__cascadeGeometryChanges"), [], "late ancestry never animates geometry");
  await read("window.__cascadeGeometryObserver.disconnect()");
  driver.milestone("late-ancestry-stable-geometry");
  const restored = await read(layoutExpr);
  assert.deepEqual(restored.panels[fixture.sourcePaneId].params.paneParams, expectedIntent);
  assert.deepEqual(restored.panels[unrelatedId], unrelatedPanel);
  assert.deepEqual(placement(restored, unrelatedId), unrelatedPlacement);
  // The restored pane's React content hydrates after the layout asserts above
  // on a loaded machine (its tab exists, its body still shows Loading), so the
  // draft read must wait for the composer to actually mount instead of
  // crashing on a null state. Pre-fix, the wiped pane never re-mounted and the
  // same missing wait crashed the guard as a TypeError; the wait turns both
  // into an honest failure naming the composer.
  await wait(`document.querySelector(${q(driver.composerSelector(fixture.childRef))}) !== null`, "restored unrelated pane's composer mounts");
  assert.equal((await driver.composerState(fixture.childRef)).text, "CASCADE_UNSENT_UNRELATED_DRAFT");
  driver.milestone("unrelated-pane-reload", { sourcePaneId: fixture.sourcePaneId, unrelatedPaneId: unrelatedId, selectedRef: fixture.refs[6], edges: expectedIntent.edges });
  await capture("unrelated-pane-reload");
  await key("Escape", 27);
  await wait('document.querySelector("[data-testid=activity-sidebar]") === null', "close desktop sidebar before phone scene");
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 390, height: 844, deviceScaleFactor: 1, mobile: false });
  await wait(`document.querySelectorAll('[data-testid="cascade-column"]').length === 1 && document.querySelector(${q(column(fixture.refs[6]))}) !== null && document.querySelectorAll('[data-testid="cascade-spine"]').length === 0`, "saved phone cascade contains only selected readonly leaf");
  assert.equal(await read('document.querySelectorAll("[role=textbox]").length'), 0, "saved phone cascade has no composer");
  await wait(`document.querySelector(${q(column(fixture.refs[6]))}).textContent.includes('CASCADE_ROLE_6_SENTINEL')`, "real selected phone transcript");
  driver.milestone("mobile-saved-cascade");
  await capture("mobile-saved-cascade");
  await driver.clickByText("Return to previous view");
  await wait(`document.querySelector(${q(driver.composerSelector(fixture.rootRef))}) !== null`, "phone Return restores original source");
  const returned = await driver.composerState(fixture.rootRef);
  assert.equal(returned.text, source.text);
  assert.deepEqual(returned.chips, source.chips);
  await capture("phone-source-return");
  await key("Escape", 27);
  await wait('document.querySelector("[data-testid=activity-sidebar]") === null', "dismiss restored source Overview before phone menu gesture");
  await driver.click(`${driver.composerSelector(fixture.rootRef)} [data-testid="session-chrome-inline"] button[aria-haspopup="menu"]`);
  const overview = await wait(`(() => {
    const button = [...document.querySelectorAll('[role="menuitem"]')].find(node => node.textContent.trim().startsWith('Overview'));
    if (!button) return null; const r = button.getBoundingClientRect(); return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
  })()`, "existing phone Overview menu action");
  await driver.clickAt(overview.x, overview.y);
  await wait(`(() => {
    const sidebar = document.querySelector('[data-testid="activity-sidebar"]');
    return sidebar && getComputedStyle(sidebar).transform === 'none';
  })()`, "phone Overview entrance settled before physical row tap");
  const mobileRow = await reveal(fixture.edges[0], '[data-testid="activity-sidebar"]');
  await driver.click(mobileRow);
  await wait('document.querySelectorAll("[data-testid=transcript-virtual-list]").length === 1 && document.querySelectorAll("[role=textbox]").length === 0 && document.querySelector("[data-pane-scaffold=cascade]") === null', "ordinary mobile Agents transcript action stays readonly without cascade");
  await wait("document.body.textContent.includes('CASCADE_ROLE_1_SENTINEL')", "real ordinary phone child transcript");
  assert.equal(await read('document.querySelectorAll("[data-testid=cascade-column], [data-testid=cascade-spine]").length'), 0);
  assert.deepEqual((await read(layoutExpr)).panels[unrelatedId], unrelatedPanel, "phone action retains independent pane identity");
  driver.milestone("mobile-agents-transcript", { childRef: fixture.childRef, unrelatedPaneId: unrelatedId });
  await capture("mobile-agents-transcript");
}

async function reveal(edge, container) {
  const selector = `${container} ${row(edge)}`;
  await wait(`document.querySelector(${q(selector)}) !== null || [...document.querySelectorAll(${q(`${container} button`)})].some(b => b.textContent.trim().startsWith('Inactive subagents ('))`, "real direct delegate collection");
  for (let boundary = 0; boundary < 10; boundary++) {
    if (await read(`document.querySelector(${q(selector)}) !== null`)) return selector;
    const visible = await read(`document.querySelectorAll(${q(`${container} [data-activity-anchor]`)}).length`);
    const before = await read(`document.querySelector(${q(container)}).textContent`);
    const button = await wait(`(() => {
      if (document.querySelector(${q(selector)})) return { revealed: true };
      const buttons = [...document.querySelectorAll(${q(`${container} button`)})];
      const b = buttons.find(n => !n.disabled && (${visible} === 0 ? n.textContent.trim().startsWith('Inactive subagents (') : n.textContent.trim().startsWith('Show ') || n.textContent.trim() === 'Load more subagents'));
      if (!b) return null;
      b.scrollIntoView({ block: 'center' }); const r = b.getBoundingClientRect();
      return { x: r.x + r.width / 2, y: r.y + r.height / 2 };
    })()`, "real disclosure or direct page boundary");
    if (button.revealed) return selector;
    // EXPERIMENT (#3804, reverted by the next commit): widen the gap between
    // measuring the control and pressing it, so a page the boundary loads on
    // its own lands first, as it does on a loaded runner.
    await new Promise((resolve) => setTimeout(resolve, 1500));
    await driver.clickAt(button.x, button.y);
    await wait(`document.querySelector(${q(selector)}) !== null || document.querySelector(${q(container)}).textContent !== ${q(before)}`, "direct collection progresses");
  }
  throw new Error(`real delegate did not become visible through ten boundaries: ${edge.delegateId}`);
}

async function waitFrames(predicate, label) {
  if (predicate()) return;
  await new Promise((resolve, reject) => {
    const timer = setTimeout(() => finish(new Error(`actual RPC condition did not arrive: ${label}`)), 15000);
    const check = () => { if (predicate()) finish(); };
    const finish = (error) => {
      clearTimeout(timer);
      driver.page.ws.removeEventListener("message", check);
      error ? reject(error) : resolve();
    };
    driver.page.ws.addEventListener("message", check);
    check();
  });
}

const sent = (after = 0) => frames.slice(after).filter(f => f.direction === "Network.webSocketFrameSent");
const answered = (request) => frames.some(f => f.direction === "Network.webSocketFrameReceived" && f.id === request.id && Object.hasOwn(f, "result") && !f.error);

async function reconnect() {
  const after = frames.length;
  const sockets = await read(`(() => {
    const sockets = window.__cascadeSockets.filter(s => s.readyState === WebSocket.OPEN && new URL(s.url).pathname === '/rpc');
    for (const socket of sockets) socket.close(4000, 'cascade guard transport boundary');
    return sockets.length;
  })()`);
  assert.equal(sockets, 1, "one actual shared hub socket is closed");
  await waitFrames(() => fixture.refs.every(ref => sent(after).some(f => f.method === "thread/read" && f.params.ref === ref && f.params.subscribe && answered(f))), "all seven real subscription owners recover");
  const subscriptions = sent(after).filter(f => f.method === "thread/read" && f.params.subscribe);
  assert.ok(subscriptions.length >= 7);
  assert.ok(subscriptions.every(f => f.params.replaceSubscription === false), "every admitted thread membership is additive");
  return after;
}

async function drill(edge, level) {
  const selector = await reveal(edge, '[data-testid="activity-sidebar"]');
  await driver.click(selector);
  await wait(`document.querySelector(${q(column(edge.childRef))}) !== null`, `selected column at level ${level}`);
  const expected = fixture.refs.slice(Math.max(0, level - 1), level + 1);
  await wait(`JSON.stringify([...document.querySelectorAll('[data-testid="cascade-column"]')].map(n => n.dataset.scopeRef)) === ${q(JSON.stringify(expected))}`, `exact column pair ${level}`);
  await wait(`document.querySelector(${q(column(edge.childRef))}).textContent.includes(${q(`CASCADE_ROLE_${level}_SENTINEL`)})`, `real retained transcript ${level}`);
  assert.equal(await read("document.querySelector('[data-testid=\"statusbar\"] [data-pane-id]')?.dataset.paneId"), fixture.sourcePaneId);
  assert.equal(await read("document.querySelectorAll('.dv-tab').length"), fixture.sourceTabCount);
}

try {
  await driver.start();
  driver.page.ws.addEventListener("message", (event) => {
    const value = JSON.parse(event.data);
    if (value.method === "Runtime.exceptionThrown") errors.push(value.params);
    if (value.method === "Runtime.consoleAPICalled") {
      consoleEvents.push(value.params);
      if (["error", "warning"].includes(value.params.type)) errors.push(value.params);
    }
    if (["Network.webSocketFrameSent", "Network.webSocketFrameReceived"].includes(value.method)) {
      try { frames.push({ direction: value.method, requestId: value.params.requestId, ...JSON.parse(value.params.response.payloadData) }); } catch {}
    }
  });
  await driver.send("Page.addScriptToEvaluateOnNewDocument", { source: `(() => {
    const Native = window.WebSocket;
    window.__cascadeSockets = [];
    window.__cascadeNativeHits = [];
    for (const type of ['mousedown', 'mouseup', 'click']) document.addEventListener(type, event => {
      window.__cascadeNativeHits.push({ type, x:event.clientX, y:event.clientY, viewportWidth:innerWidth,
        hitAnchor:event.target.closest('[data-activity-anchor]')?.dataset.activityAnchor ?? null,
        button:event.target.closest('button')?.textContent.slice(0, 100) ?? null });
    }, true);
    window.WebSocket = class extends Native {
      constructor(...args) { super(...args); window.__cascadeSockets.push(this); }
    };
  })()` });
  await navigateTo(driver.page, fixture.url);
  await driver.openSession(fixture.rootRef);
  fixture.sourcePaneId = await read("document.querySelector('[data-testid=\"statusbar\"] [data-pane-id]')?.dataset.paneId");
  assert.ok(fixture.sourcePaneId, "source has a committed Dockview pane ID");
  fixture.sourceTabCount = await read("document.querySelectorAll('.dv-tab').length");
  await driver.focusComposer(fixture.rootRef);
  await driver.typeText(fixture.rootRef, "CASCADE_UNSENT_SOURCE_DRAFT");
  await driver.typeText(fixture.rootRef, " /cascade-source");
  await driver.completeSkill(fixture.rootRef, "cascade-source");
  await upload("staged.png");
  await wait(`document.querySelector(${q(stagedPNG)})?.src.startsWith("data:image/png;base64,")`, "staged real PNG bytes");
  const sourcePNG = await read(`document.querySelector(${q(stagedPNG)}).src`);
  const sourceDraft = await driver.composerState(fixture.rootRef);
  assert.deepEqual(sourceDraft.chips, ["/cascade-source"], "skill comes from real daemon catalog");
  await driver.click('[data-testid="statusbar"] button[aria-label^="Agents,"]');
  await drill(fixture.edges[0], 1);
  driver.milestone("root-child");
  for (let level = 2; level <= 6; level++) await drill(fixture.edges[level - 1], level);
  await wait(`(() => {
    const spines = [...document.querySelectorAll('[data-testid="cascade-spine"]')];
    const columns = [...document.querySelectorAll('[data-testid="cascade-column"]')];
    return spines.length === 5 && spines.every(n => Math.abs(n.getBoundingClientRect().width - 52) < 0.75)
      && columns.length === 2 && Math.abs(columns[0].getBoundingClientRect().width - 400) < 0.75
      && columns[1].getBoundingClientRect().width >= 439.5;
  })()`, "settled six-edge geometry");
  const boxes = await read(`([...document.querySelectorAll('[data-testid="cascade-spine"], [data-testid="cascade-column"]')]).map(n => {
    const r = n.getBoundingClientRect(); return { ref: n.dataset.scopeRef, kind: n.dataset.testid, x:r.x, y:r.y, width:r.width, height:r.height };
  })`);
  assert.deepEqual(boxes.map(n => n.ref), fixture.refs);
  driver.milestone("six-edges", { boxes, sourcePaneId: fixture.sourcePaneId, tabs: fixture.sourceTabCount });
  const paint = await capture("six-edges");
  assert.ok(paint[0].chain[0].box.width >= 398.5, `parent transcript fills its readable column, got ${paint[0].chain[0].box.width}px`);
  assert.ok(paint[1].chain[0].box.width >= 439.5, `leaf transcript fills its readable column, got ${paint[1].chain[0].box.width}px`);
  assert.ok(paint.every(view => view.rows.some(row => row.intersects && row.box.width > 0 && row.textLength > 0)), "both readable transcript viewports contain rendered text rows");
  await driver.send("Emulation.setDeviceMetricsOverride", { width: 1000, height: 900, deviceScaleFactor: 1, mobile: false });
  // A user pop/drill after the resize must reveal the selected leaf. Resizing
  // itself is not a drill and must not move the retained reader's position.
  await wait("window.innerWidth === 1000", "native narrow desktop viewport applied");
  const parentCrumb = 'nav[aria-label="Agent path"] button:nth-last-child(2)';
  await read(`document.querySelector(${q(parentCrumb)}).focus()`);
  await wait(`document.activeElement === document.querySelector(${q(parentCrumb)})`, "parent breadcrumb owns keyboard focus");
  await key("Enter", 13);
  await wait(`document.querySelector(${q(`${column(fixture.refs[5])}[data-leaf]`)}) !== null && document.querySelector(${q(column(fixture.refs[6]))}) === null`, "actual narrow-desktop parent pop");
  await drill(fixture.edges[5], 6);
  const narrow = await wait(`(() => {
    const leaf = document.querySelector(${q(column(fixture.refs[6]))});
    const track = leaf?.parentElement;
    if (!track || track.scrollWidth <= track.clientWidth) return null;
    const a = track.getBoundingClientRect(), b = leaf.getBoundingClientRect();
    return b.right <= a.right + 1 && b.right > a.left ? { width: track.clientWidth, extent: track.scrollWidth, left: track.scrollLeft, leafRight: b.right, trackRight: a.right } : null;
  })()`, "narrow desktop overflow and selected leaf revealed");
  // Both columns are virtualized, and under 2-core load a late row-height
  // correction can leave a column a few pixels off the bottom it was pinned
  // to: the app holds the reader's visual position, so the numeric gap never
  // closes on its own (sighted once as a steady 32px gap with a quiet RPC
  // stream and an empty console). Retained bottom is the precondition the
  // independence check below scrolls FROM, not the behavior under test, so
  // the guard establishes it the same way that check sets its explicit
  // offsets: settle the heights, re-pin both columns, then require the
  // retained-bottom condition to hold on its own.
  await wait(`(() => {
    const nodes = [${q(scroll(fixture.refs[5]))}, ${q(scroll(fixture.refs[6]))}].map((s) => document.querySelector(s));
    if (nodes.includes(null)) return null;
    const stamp = nodes.map((node) => node.scrollHeight).join(",");
    const settled = window.__cascadeScrollHeights === stamp;
    window.__cascadeScrollHeights = stamp;
    return settled;
  })()`, "both cascade columns' scroll geometry settled before the independence check");
  await read(`[${q(scroll(fixture.refs[5]))},${q(scroll(fixture.refs[6]))}].map((s) => { const port = document.querySelector(s); if (port) port.scrollTop = port.scrollHeight; })`);
  await wait(`${readableGeometrySettled} && (() => {
    const ports = [${q(scroll(fixture.refs[5]))},${q(scroll(fixture.refs[6]))}].map(selector => document.querySelector(selector));
    return ports.every(port => port && port.clientHeight > 0 && port.scrollHeight > port.clientHeight
      && Math.abs(port.scrollHeight - port.clientHeight - port.scrollTop) <= 1);
  })()`, "settled readable geometry and retained bottom before independent scrolling");
  const beforeScroll = await read(`[${q(scroll(fixture.refs[5]))},${q(scroll(fixture.refs[6]))}].map(s => document.querySelector(s).scrollTop)`);
  await read(`document.querySelector(${q(scroll(fixture.refs[5]))}).scrollTop = 200`);
  await wait(`document.querySelector(${q(scroll(fixture.refs[5]))}).scrollTop === 200`, "parent scrolled independently");
  assert.equal(await read(`document.querySelector(${q(scroll(fixture.refs[6]))}).scrollTop`), beforeScroll[1]);
  await read(`document.querySelector(${q(scroll(fixture.refs[6]))}).scrollTop = 320`);
  await wait(`document.querySelector(${q(scroll(fixture.refs[6]))}).scrollTop === 320`, "leaf scrolled independently");
  assert.equal(await read(`document.querySelector(${q(scroll(fixture.refs[5]))}).scrollTop`), 200);
  driver.milestone("narrow-independent-scroll", narrow);

  const leafChrome = await read(`({ footer: document.querySelector('[data-testid="statusbar"]').textContent, sidebar: document.querySelector('[data-testid="activity-sidebar"]').textContent })`);
  await read(`(() => { const node = document.querySelector(${q(column(fixture.refs[5]))}).querySelector('[data-testid="turn-block"]'); const range = document.createRange(); range.selectNodeContents(node); const selection = window.getSelection(); selection.removeAllRanges(); selection.addRange(range); })()`);
  assert.ok(await read("window.getSelection().toString().length > 0"));
  assert.equal(await read('document.querySelector("[data-testid=cascade-column][data-leaf]").dataset.scopeRef'), fixture.refs[6]);
  assert.deepEqual(await read(`({ footer: document.querySelector('[data-testid="statusbar"]').textContent, sidebar: document.querySelector('[data-testid="activity-sidebar"]').textContent })`), leafChrome);
  driver.milestone("parent-selection-leaf-scope");

  await openPeek(fixture.rootRef, "Tasks");
  await wait('document.querySelector("[data-testid=activity-peek]").textContent.includes("No tasks")', "Tasks is rendered inside ancestor peek");
  await key("Escape", 27);
  await wait('document.querySelector("[data-testid=activity-peek]") === null', "single Escape closes only peek");
  assert.ok(await read('document.querySelector("[data-testid=activity-sidebar]") !== null'));
  assert.equal(await read('document.querySelectorAll("[data-testid=cascade-spine]").length'), 5);
  driver.milestone("peek-tasks-single-escape");
  await driver.send("Emulation.setEmulatedMedia", { features: [{ name: "prefers-reduced-motion", value: "reduce" }] });
  assert.equal(await read('matchMedia("(prefers-reduced-motion: reduce)").matches'), true);
  const beforePeek = frames.length;
  await openPeek(fixture.rootRef, "Agents");
  const peekRow = await reveal(fixture.branch, '[data-testid="activity-peek"]');
  const paging = sent(beforePeek).filter(f => f.method === 'evener/thread/delegates/list' && f.params.ref === fixture.rootRef && f.params.cursor);
  assert.ok(paging.length > 0 && paging.every(answered), "open ancestor peek advances the real direct-page cursor");
  await read(`document.querySelector(${q(peekRow)}).focus()`);
  await wait(`document.activeElement === document.querySelector(${q(peekRow)})`, "real peek row owns keyboard focus");
  await key("Enter", 13);
  await wait(`document.querySelector(${q(column(fixture.branch.childRef))}) !== null && document.querySelectorAll('[data-scope-ref]').length === 2`, "keyboard ancestor branch truncation");
  assert.deepEqual(await read('[...document.querySelectorAll("[data-scope-ref]")].map(n => n.dataset.scopeRef)'), [fixture.rootRef, fixture.branch.childRef], "the sibling replaces the old six-edge suffix");
  assert.equal(await read('document.querySelectorAll("[data-testid=activity-peek]").length'), 0);
  assert.equal(await read('document.getAnimations().filter(a => a.effect?.target?.matches("[data-scope-ref]") && a.playState === "running").length'), 0);
  await driver.click('nav[aria-label="Agent path"] button:first-child');
  await wait(`document.querySelectorAll('[data-testid="cascade-column"]').length === 1 && document.querySelector(${q(column(fixture.rootRef))}) !== null`, "pop to root before restoring real branch");
  await drill(fixture.edges[0], 1);
  for (let level = 2; level <= 6; level++) await drill(fixture.edges[level - 1], level);
  assert.equal(await read('document.getAnimations().filter(a => a.effect?.target?.matches("[data-scope-ref]") && a.playState === "running").length'), 0);
  driver.milestone("keyboard-branch-reduced-motion");

  await openPeek(fixture.rootRef, "Agents");
  const rootPeekRow = await reveal(fixture.branch, '[data-testid="activity-peek"]');
  await read(`(() => {
    document.querySelector(${q(rootPeekRow)}).focus();
    window.__cascadeFocused = document.activeElement;
    window.__cascadeGeometryChanges = [];
    window.__cascadeGeometryObserver = new MutationObserver(records => {
      for (const record of records) if (record.target.matches('[data-scope-ref]')) window.__cascadeGeometryChanges.push({ ref: record.target.dataset.scopeRef, style: record.target.getAttribute('style') });
    });
    window.__cascadeGeometryObserver.observe(document.querySelector('[data-pane-scaffold="cascade"]'), { subtree: true, attributes: true, attributeFilter: ['style'] });
  })()`);
  await driver.send("Emulation.setEmulatedMedia", { features: [] });
  const openAfter = await reconnect();
  await waitFrames(() => sent(openAfter).some(f => f.method === "evener/thread/delegates/list" && f.params.ref === fixture.rootRef && f.params.cursor && answered(f)), "open peek recovers its full observed page extent");
  assert.ok(await read(`document.querySelector(${q(rootPeekRow)}) !== null`), "retained older-page row remains useful after reconnect");
  assert.equal(await read("document.activeElement === window.__cascadeFocused"), true, "reconnect and republished context keep keyboard focus");
  assert.deepEqual(await read("window.__cascadeGeometryChanges"), [], "reconnect and runtime/context refresh never animate cascade geometry");
  await key("Escape", 27);
  await wait('document.querySelector("[data-testid=activity-peek]") === null', "closed peek relinquishes direct collection demand");
  const closedAfter = await reconnect();
  await waitFrames(() => sent(closedAfter).some(f => f.method === "evener/thread/delegates/list" && f.params.ref === fixture.refs[6] && answered(f)), "selected-leaf sidebar resumes its actual direct collection");
  assert.equal(sent(closedAfter).filter(f => f.method === 'evener/thread/delegates/list' && f.params.ref === fixture.rootRef).length, 0, "closed root peek causes no delegate-page read on recovery");
  assert.deepEqual(await read("window.__cascadeGeometryChanges"), []);
  await read("window.__cascadeGeometryObserver.disconnect()");
  driver.milestone("reconnect-extent-closed-peek", { extent: fixture.rootDelegateCount, subscriptions: sent(openAfter).filter(f => f.method === 'thread/read' && f.params.subscribe).length });

  await driver.clickByText("Return to previous view");
  await wait(`document.querySelector(${q(driver.composerSelector(fixture.rootRef))}) !== null`, "return original source");
  assert.equal((await driver.composerState(fixture.rootRef)).text, sourceDraft.text);
  assert.deepEqual((await driver.composerState(fixture.rootRef)).chips, sourceDraft.chips);
  assert.equal(await read(`document.querySelector(${q(stagedPNG)}).src`), sourcePNG, "exact processed PNG bytes survive promotion");
  assert.equal(await read("document.querySelector('[data-testid=\"statusbar\"] [data-pane-id]')?.dataset.paneId"), fixture.sourcePaneId);
  assert.equal(await read("document.querySelectorAll('.dv-tab').length"), fixture.sourceTabCount);
  driver.milestone("return-source");
  await capture("return-source");
  await pendingImage("pending-success.png", true);
  await pendingImage("pending-failure.png", false);
  await mixedSourceJourney();
  await sourceMutationJourney();
  await reloadAndMobileJourney();
  assert.deepEqual(errors, [], "unexpected browser errors or warnings");
} catch (error) {
  failed = true;
  console.error(error.stack ?? String(error));
  if (driver.page) await capture("failure").catch((failure) => console.error(failure));
} finally {
  writeFileSync(path.join(fixture.artifactDir, "console.json"), JSON.stringify(consoleEvents, null, 2));
  writeFileSync(path.join(fixture.artifactDir, "rpc-frames.json"), JSON.stringify(frames, null, 2));
  await driver.stop();
}
if (failed) process.exitCode = 1;
