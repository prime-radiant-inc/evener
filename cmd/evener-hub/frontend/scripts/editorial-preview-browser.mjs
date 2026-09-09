import assert from "node:assert/strict";
import { spawn } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";
import { measureEditorial, assertEditorialGeometry } from "./editorial-preview-measure.mjs";
import { startBrowserGuard } from "./browserGuardProcess.mjs";
import { connectPage, createStartupDeadline, waitForHttp, navigateTo, evaluate, applyViewport, waitForFonts } from "./browserGuardCdp.mjs";

const frontend = fileURLToPath(new URL("../", import.meta.url));
const evidence = path.resolve(process.env.EDITORIAL_EVIDENCE ?? path.join(frontend, "../../..", ".superpowers/sdd/2026-09-09-tufte-webui/task-4-evidence"));
fs.mkdirSync(evidence, { recursive: true });
const run = await startBrowserGuard({ frontend, profilePrefix: "editorial-preview-", spawnProcess(command, args, options) {
  if (command.endsWith("/vite")) {
    args = args.map(arg => arg === "scripts/browserguard.vite.config.mjs" ? "scripts/editorial-preview.vite.config.mjs" : arg === "127.0.0.1" ? "0.0.0.0" : arg);
  }
  const child=spawn(command, args, options);
  fs.appendFileSync(path.join(evidence,"processes.jsonl"),JSON.stringify({command,args,pid:child.pid})+"\n");
  return child;
} });
let page;
const observations = { requests: [], errors: [], tasks: [], geometry: [], geometryFailures: [], workflowFailures: [], rpc: [], rejectedRequests: [], runtimeErrors: [], websockets: [] };
try {
  const deadline = createStartupDeadline();
  const endpoint = await run.waitForChrome({signal: deadline.signal});
  await waitForHttp(`http://127.0.0.1:${run.vitePort}/`, "editorial preview", run.getViteLaunchError, {signal: deadline.signal});
  deadline.clear();
  page = await connectPage(endpoint);
  const {send, ws} = page;
  ws.addEventListener("message", event => {
    const message = JSON.parse(event.data);
    if (message.method === "Network.requestWillBeSent") observations.requests.push(message.params.request.url);
    if (message.method === "Network.webSocketCreated") observations.websockets.push(message.params.url);
    if (message.method === "Runtime.exceptionThrown") observations.errors.push(message.params.exceptionDetails);
    if (message.method === "Log.entryAdded" && message.params.entry.level === "error") observations.errors.push(message.params.entry);
  });
  await send("Network.enable"); await send("Runtime.enable"); await send("Log.enable");
  const origin = `http://127.0.0.1:${run.vitePort}`;
  const evalJS = expression => evaluate(send, expression);
  async function captureBoundary() {
    const state=await evalJS("window.editorialPreview ? {url:location.pathname,calls:window.editorialPreview.client.calls,rejected:window.editorialPreview.client.rejectedRequests,errors:window.editorialPreview.errors} : null");
    if (state) { observations.rpc.push({url:state.url,calls:state.calls}); observations.rejectedRequests.push(...state.rejected); observations.runtimeErrors.push(...state.errors); }
  }
  async function navigateFixture(url) { await captureBoundary(); await navigateTo(page,url); }
  async function until(expression) {
    const end = Date.now() + 15000;
    while (Date.now() < end) { if (await evalJS(expression)) return; await new Promise(resolve => setTimeout(resolve, 75)); }
    throw new Error(`Timed out: ${expression}\n${await evalJS("document.body.innerText")}\nWorkspace: ${JSON.stringify(await evalJS("window.editorialTrace??[]"))}`);
  }
  async function clickText(text) {
    await until(`Array.from(document.querySelectorAll('span,button,a,[role=menuitem]')).some(e => e.textContent.replace(/\\s*✓$/, '').trim() === ${JSON.stringify(text)})`);
    const box = await evalJS(`(() => { const e = Array.from(document.querySelectorAll('span,button,a,[role=menuitem]')).find(e => e.textContent.replace(/\\s*✓$/, '').trim() === ${JSON.stringify(text)}); e.scrollIntoView({block:'center'}); const r=e.getBoundingClientRect(); return {x:r.x+r.width/2,y:r.y+r.height/2}; })()`);
    await send("Input.dispatchMouseEvent", {type: "mousePressed", button: "left", clickCount: 1, ...box});
    await send("Input.dispatchMouseEvent", {type: "mouseReleased", button: "left", clickCount: 1, ...box});
  }
  async function closeAuxiliaryPanes() {
    // DockHost deliberately hides the main tab header. Only visible auxiliary
    // native tab-close controls are actionable; never mutate the workspace store.
    const selector = ".dv-default-tab-action";
    const visible = `Array.from(document.querySelectorAll('${selector}')).filter(e=>{const r=e.getBoundingClientRect();return r.width>0&&r.height>0;})`;
    while (await evalJS(`${visible}.length > 0`)) {
      const box = await evalJS(`(() => {const e=${visible}[0];const r=e.getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};})()`);
      assert(await evalJS(`document.elementFromPoint(${box.x},${box.y})?.closest('${selector}') !== null`), "Auxiliary Close target hit-test");
      await send("Input.dispatchMouseEvent", {type:"mousePressed",button:"left",clickCount:1,...box});
      await send("Input.dispatchMouseEvent", {type:"mouseReleased",button:"left",clickCount:1,...box});
      await evalJS("new Promise(resolve=>requestAnimationFrame(()=>requestAnimationFrame(resolve)))");
    }
  }
  function assertWideSetup(m, viewport, label) {
    if (viewport.width !== 1440) return;
    assert(m.transcript.width >= 880, `${label}: required wide timestamp rail width not established`);
    assert(m.stamps.length > 0, `${label}: required wide timestamp sample missing`);
  }
  await applyViewport(send, {width: 1440, height: 1000});
  await navigateFixture(`${origin}/`);
  await until("window.editorialPreview?.fixtureOnly && document.body.innerText.includes('Editorial fixture parent')");
  await clickText("Editorial fixture parent");
  await until("document.body.innerText.includes('Parent analysis: fixture-only evidence.')");
  await clickText("Editorial fixture child");
  await until("document.body.innerText.includes('Child report: independent transcript, not the parent snapshot.')");
  assert.equal(await evalJS("decodeURIComponent(location.pathname)"), "/s/local:editorial-child");
  await clickText("Editorial fixture parent");
  await until("decodeURIComponent(location.pathname) === '/s/local:editorial-parent'");
  observations.tasks.push("Actual rail parent → distinct child transcript/ref → parent");
  await until("document.querySelectorAll('[data-testid=delegate-lifecycle]').length === 3");
  assert.deepEqual(await evalJS("Array.from(document.querySelectorAll('[data-testid=delegate-lifecycle]')).map(e=>e.innerText)"), ["Idle · reported", "Running", "Status unavailable"]);
  observations.tasks.push("Owner projections override historical running/completed receipts; unknown remains unavailable");
  await evalJS("document.querySelector('[aria-label=Message]').focus()");
  await send("Input.insertText", {text: "Browser fixture message"});
  await evalJS("document.querySelector('[data-testid=composer-submit]').click()");
  await until("document.body.innerText.includes('Fixture acknowledged: Browser fixture message')");
  observations.tasks.push("Typed and sent deterministic fixture-only message; authored acknowledgment rendered");
  await clickText("Editorial fixture question");
  await until("!!document.querySelector('[data-ask-response-dock]')");
  fs.writeFileSync(path.join(evidence, "question.html"), await evalJS("document.body.innerHTML"));
  fs.writeFileSync(path.join(evidence, "question.txt"), await evalJS("document.body.innerText"));
  await evalJS("document.querySelector('input[aria-label=\"Tool evidence\"]').click()");
  await clickText("Send answers");
  await until("document.body.innerText.includes('Fixture acknowledged:') && !document.querySelector('[data-ask-response-dock]')");
  observations.tasks.push("Answered real AskDock option and sent answer; pending dock cleared");
  observations.questionRPC = await evalJS("window.editorialPreview.client.calls.filter(c => c.method === 'turn/steer' || c.method === 'turn/start')");
  assert.deepEqual(await evalJS("window.editorialPreview.client.rejectedRequests"), []);
  await navigateFixture(`${origin}/settings/credentials`);
  await until("document.body.innerText.includes('+ Add provider instance')");
  await clickText("+ Add provider instance");
  await until("!!document.querySelector('[role=dialog]')");
  fs.writeFileSync(path.join(evidence, "provider.txt"), await evalJS("document.body.innerText"));
  fs.writeFileSync(path.join(evidence, "provider.html"), await evalJS("document.body.innerHTML"));
  await evalJS("Array.from(document.querySelectorAll('[role=dialog] button')).find(e=>e.textContent==='Create').click()");
  await until("document.body.innerText.includes('Base provider is required.')");
  await send("Input.dispatchKeyEvent", {type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27});
  await send("Input.dispatchKeyEvent", {type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27});
  await until("!document.querySelector('[role=dialog]')");
  assert.equal(await evalJS("document.activeElement.textContent"), "+ Add provider instance");
  observations.tasks.push("Provider instance form empty-submit validation; Escape closes and restores trigger focus; no secrets");
  await navigateFixture(`${origin}/settings/theme`);
  await until("document.body.innerText.includes('Font size')");
  await clickText("Light"); await clickText("XL");
  await navigateFixture(`${origin}/s/local%3Aeditorial-parent`);
  await until("document.body.innerText.includes('Parent analysis: fixture-only evidence.')");
  assert.equal(await evalJS("document.documentElement.dataset.theme"), "light");
  assert.equal(await evalJS("document.body.dataset.fontSize"), "xl");
  observations.tasks.push("Real theme/font controls persist Light + XL across actual app-route reload");
  await evalJS("Array.from(document.querySelectorAll('button')).find(e=>e.textContent.includes('Session actions')).focus()");
  await send("Input.dispatchKeyEvent", {type: "keyDown", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13, text: "\r"});
  await send("Input.dispatchKeyEvent", {type: "keyUp", key: "Enter", code: "Enter", windowsVirtualKeyCode: 13});
  await until("!!document.querySelector('[role=menu]')");
  await send("Input.dispatchKeyEvent", {type: "keyDown", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27});
  await send("Input.dispatchKeyEvent", {type: "keyUp", key: "Escape", code: "Escape", windowsVirtualKeyCode: 27});
  await until("!document.querySelector('[role=menu]')");
  assert(await evalJS("document.activeElement.tagName === 'BUTTON' && document.activeElement.textContent.includes('Session actions')"));
  observations.tasks.push("Keyboard Enter opens session menu; Escape restores trigger focus");
  try {
  await applyViewport(send,{width:390,height:844,mobile:true,touch:true});
  await navigateFixture(`${origin}/s/local%3Aeditorial-parent`);
  await until("!!document.querySelector('[data-testid=delegate-lifecycle]')");
  await evalJS("(async()=>{const {workspaceStore}=await import('/src/shell/workspace.ts');window.editorialTrace=[];workspaceStore.subscribe(s=>window.editorialTrace.push({focused:s.focusedPaneId,panes:s.panes.map(p=>({id:p.id,type:p.type,params:p.params}))}));})()");
  await evalJS("(async()=>{const e=Array.from(document.querySelectorAll('[data-tool-name=delegate]')).find(e=>e.innerText.includes('Inspect the independent child')).querySelector('[aria-label=\"Open transcript\"]');e.scrollIntoView({block:'center'});await new Promise(requestAnimationFrame);await new Promise(requestAnimationFrame);})()");
  const openBox=await evalJS("(() => {const e=Array.from(document.querySelectorAll('[data-tool-name=delegate]')).find(e=>e.innerText.includes('Inspect the independent child')).querySelector('[aria-label=\"Open transcript\"]');const r=e.getBoundingClientRect();return {x:r.x+r.width/2,y:r.y+r.height/2};})()");
  assert(await evalJS(`document.elementFromPoint(${openBox.x},${openBox.y})?.closest('[aria-label="Open transcript"]') !== null`), "Independent Open target hit-test");
  await send("Input.dispatchMouseEvent",{type:"mousePressed",button:"left",clickCount:1,...openBox});
  await send("Input.dispatchMouseEvent",{type:"mouseReleased",button:"left",clickCount:1,...openBox});
  await until("document.querySelector('[data-testid=topbar-title]')?.textContent==='Editorial fixture child' && !document.querySelector('textarea[aria-label=Message]')");
  const childSurface = await evalJS("document.querySelector('[data-testid=transcript-view-announcement]').parentElement.innerText");
  assert(childSurface.includes('Child report: independent transcript, not the parent snapshot.'));
  assert(!childSurface.includes('Parent analysis: fixture-only evidence.'));
  fs.writeFileSync(path.join(evidence,"phone-independent-child.png"),Buffer.from((await send("Page.captureScreenshot")).result.data,"base64"));
  await evalJS("document.querySelector('[aria-label=Back]').click()");
  await until("document.querySelector('[data-testid=topbar-title]')?.textContent==='Editorial fixture parent' && !!document.querySelector('textarea[aria-label=Message]') && document.body.innerText.includes('Parent analysis: fixture-only evidence.')");
  observations.tasks.push("Phone independent Open transcript displays distinct read-only child surface; actual Back returns parent");
  } catch(error) {
    observations.workflowFailures.push(error.message);
    observations.childOpenTrace=await evalJS("window.editorialTrace??[]");
    fs.writeFileSync(path.join(evidence,"phone-independent-child-failure.png"),Buffer.from((await send("Page.captureScreenshot")).result.data,"base64"));
  }
  await captureBoundary();
  // Independent panel/matrix test setup in this private Chrome profile only.
  // Earlier real preference persistence assertions already completed. This
  // does not erase the preceding failure, which keeps the final gate red.
  await send("Storage.clearDataForOrigin",{origin,storageTypes:"local_storage,indexeddb"});
  await applyViewport(send,{width:1440,height:1000});
  await send("Emulation.setTouchEmulationEnabled",{enabled:false});
  await navigateFixture(`${origin}/s/local%3Aeditorial-parent`);
  await until("document.body.innerText.includes('Session actions')");
  await clickText("Session actions"); await clickText("Tasks");
  await until("document.body.innerText.includes('Review fixture evidence')");
  fs.writeFileSync(path.join(evidence,"tasks.png"),Buffer.from((await send("Page.captureScreenshot")).result.data,"base64"));
  await closeAuxiliaryPanes();
  await until("!document.body.innerText.includes('Review fixture evidence')");
  await clickText("Session actions"); await clickText("Activity");
  await until("document.body.innerText.includes('Delegate dlg_editorial_resumed · send')");
  fs.writeFileSync(path.join(evidence,"activity.png"),Buffer.from((await send("Page.captureScreenshot")).result.data,"base64"));
  await closeAuxiliaryPanes();
  await until("!document.body.innerText.includes('Delegate dlg_editorial_resumed · send')");
  observations.tasks.push("Actual Tasks and Activity panel selection; fixture tasks and partial owner-derived activity; Details split exercised in matrix");
  for (const viewport of [{width:360,height:800,mobile:true,touch:true}, {width:390,height:844,mobile:true,touch:true}, {width:1280,height:1000,narrow:true}, {width:1440,height:1000}]) {
    for (const theme of ["light","dark"]) for (const size of ["m","xl"]) {
      await applyViewport(send, viewport);
      if (!viewport.touch) await send("Emulation.setTouchEmulationEnabled", {enabled:false});
      await navigateFixture(`${origin}/settings/theme`);
      await until("document.body.innerText.includes('Font size')");
      await clickText(theme === "light" ? "Light" : "Dark"); await clickText(size.toUpperCase());
      await navigateFixture(`${origin}/s/local%3Aeditorial-parent`);
      await until("document.body.innerText.includes('Parent analysis: fixture-only evidence.') && !!document.querySelector('[data-testid=composer-input-card]')");
      assert.equal(await evalJS("document.documentElement.dataset.theme"),theme);
      assert.equal(await evalJS("document.body.dataset.fontSize"),size);
      if (!viewport.touch) await closeAuxiliaryPanes();
      if (viewport.narrow) { await clickText("Session actions"); await clickText("Details"); }
      await until(viewport.narrow ? "document.body.innerText.includes('session id')" : "!document.body.innerText.includes('session id')");
      const label = `${viewport.width}-${theme}-${size}`;
      await waitForFonts(send);
      assertWideSetup(await evalJS(`(${measureEditorial.toString()})()`), viewport, label);
      await evalJS("document.querySelector('[data-testid=transcript-virtual-list]').scrollTop=0");
      await until("!!document.querySelector('[data-tool-name=read_file] [data-testid=tool-row-trigger]')");
      await evalJS("(() => { const row=document.querySelector('[data-tool-name=read_file]'); if (!row.querySelector('[data-testid=tool-row-body-trigger]')) row.querySelector('[data-testid=tool-row-trigger]').click(); })()");
      await until("!!document.querySelector('[data-tool-name=read_file] [data-testid=tool-row-body-trigger]')");
      await evalJS("document.querySelector('[data-tool-name=read_file] [data-testid=tool-row-body-trigger]').click()");
      await until("!!document.querySelector('[data-tool-name=read_file] [data-testid=tool-call-body]')");
      const native = await evalJS("document.querySelector('[data-tool-name=read_file] [data-testid=tool-call-body]').innerText");
      assert(native.includes('retained native source evidence'), `${label}: native evidence missing`);
      fs.writeFileSync(path.join(evidence, `${label}-evidence.png`), Buffer.from((await send("Page.captureScreenshot")).result.data,"base64"));
      const geometry = await evalJS(`(${measureEditorial.toString()})()`);
      observations.geometry.push({label, ...geometry});
      assertWideSetup(geometry, viewport, label);
      try { assertEditorialGeometry(assert,geometry,label); } catch(error) { observations.geometryFailures.push(error.message); }
      if (viewport.narrow) assert(geometry.transcript.width<=600, `${label}: did not create a narrow desktop pane`);
      await evalJS("(() => { const row=document.querySelector('[data-tool-name=read_file]'); if (!row.querySelector('[data-testid=tool-row-body-trigger]')) row.querySelector('[data-testid=tool-row-trigger]').click(); })()");
      await until("!!document.querySelector('[data-tool-name=read_file] [data-testid=tool-row-body-trigger]')");
      await evalJS("document.querySelector('[data-tool-name=read_file] [data-testid=tool-row-body-trigger]').click()");
      await evalJS("const list=document.querySelector('[data-testid=transcript-virtual-list]');list.scrollTop=list.scrollHeight");
      await until("document.querySelectorAll('[data-testid=delegate-lifecycle]').length===3");
      const collaborators = await evalJS(`(${measureEditorial.toString()})()`);
      observations.geometry.push({label:`${label}-collaborators`,...collaborators});
      assertWideSetup(collaborators, viewport, `${label}-collaborators`);
      try { assertEditorialGeometry(assert,collaborators,label); } catch(error) { observations.geometryFailures.push(error.message); }
      fs.writeFileSync(path.join(evidence, `${label}-collaborators.png`), Buffer.from((await send("Page.captureScreenshot")).result.data,"base64"));
      assert.deepEqual(await evalJS("window.editorialPreview.client.rejectedRequests"),[]);
    }
  }
  observations.tasks.push("360/390 phone, narrow desktop split and wide desktop; both themes; M/XL; native evidence + collaborators; computed geometry");
  await captureBoundary();
  assert.deepEqual(observations.rejectedRequests, []);
  await waitForFonts(send);
  fs.writeFileSync(path.join(evidence, "smoke.png"), Buffer.from((await send("Page.captureScreenshot")).result.data, "base64"));
  fs.writeFileSync(path.join(evidence, "body.txt"), await evalJS("document.body.innerText"));
  await navigateFixture(`${origin}/index.html`);
  await until("window.editorialPreview?.fixtureOnly && document.body.innerText.includes('Page not found')");
  await clickText("Go home");
  await until("document.body.innerText.includes('Editorial fixture parent')");
  observations.tasks.push("Accidental normal /index.html reload mounts fixture-backed NotFound; actual Go home recovers; no live backend requests");
  await captureBoundary();
  assert.deepEqual(observations.errors, []);
  assert.deepEqual(observations.runtimeErrors, []);
  assert.deepEqual(observations.rejectedRequests, []);
  // Vite 8 client unconditionally connects its same-origin development channel
  // even with HMR updates disabled. No /rpc or external socket is permitted.
  assert(observations.websockets.every(url => { const socket=new URL(url); return socket.host===new URL(origin).host && socket.pathname==='/' && socket.protocol==='ws:'; }), "No hub/external websocket connections");
  assert(observations.requests.every(url => !/^\/(rpc|api|auth|doc)(\/|$)/.test(new URL(url).pathname)), "No backend route requests");
  assert(observations.requests.every(url => url.startsWith(`${origin}/`) || url.startsWith("data:")), "No external requests");
  assert.deepEqual(observations.workflowFailures, [], "Full-AppShell required workflows");
  assert.deepEqual(observations.geometryFailures, [], "Full-AppShell geometry matrix");
  console.log("PASS editorial-preview full-AppShell workflows and geometry matrix");
} finally {
  fs.writeFileSync(path.join(evidence, "browser.json"), JSON.stringify(observations, null, 2));
  page?.close();
  await run.cleanup();
}
