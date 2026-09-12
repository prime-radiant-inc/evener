import assert from "node:assert/strict";

// All workspace access is observational. Every Open, Back, and new-route link
// below is activated through Chrome's trusted pointer input on the actual UI.
export async function exerciseNestedTranscripts({ evalJS, send, until, navigateFixture, origin, screenshot, observations }) {
  const root = "local:editorial-parent";
  const child = "local:editorial-child";
  const grandchild = "local:editorial-grandchild";
  const deeper = "local:editorial-great-grandchild";
  const contents = {
    [child]: "Child report: independent transcript, not the parent snapshot.",
    [grandchild]: "Grandchild report: evidence authored by the independent grandchild.",
    [deeper]: "Great-grandchild report: deeper independent evidence.",
  };
  const state = () => evalJS(`(async()=>{const {workspaceStore}=await import('/src/shell/workspace.ts');const s=workspaceStore.getState();return {route:decodeURIComponent(location.pathname),main:s.mainPane(),focused:s.panes.find(p=>p.id===s.focusedPaneId),panes:s.panes,content:document.querySelector('[data-testid=transcript-view-announcement]')?.parentElement.innerText,composer:!!document.querySelector('textarea[aria-label=Message]'),authored:Array.from(document.querySelectorAll('[data-testid=agent-bubble]')).filter(e=>!e.closest('[data-tool-name]')).map(e=>e.innerText),calls:window.editorialPreview.client.calls};})()`);
  async function pointer(expression, label) {
    await until(`!!(${expression})`);
    await evalJS(`(${expression}).scrollIntoView({block:'center'})`);
    // Await actual target visibility/hit-test, not a guessed number of frames.
    await until(`(()=>{const e=${expression};const r=e.getBoundingClientRect();return r.width>0&&r.height>0&&r.top>=0&&r.bottom<=innerHeight&&e.contains(document.elementFromPoint(r.x+r.width/2,r.y+r.height/2));})()`);
    const target = await evalJS(`(()=>{const e=${expression};window.editorialPointerEvents=[];for(const type of ['pointerdown','click'])e.addEventListener(type,event=>window.editorialPointerEvents.push({type,trusted:event.isTrusted,label:e.getAttribute('aria-label'),text:e.innerText}),{once:true});const r=e.getBoundingClientRect();return {label:e.getAttribute('aria-label'),text:e.innerText,row:e.closest('[data-tool-name]')?.innerText,x:r.x+r.width/2,y:r.y+r.height/2};})()`);
    await send("Input.dispatchMouseEvent", { type: "mousePressed", button: "left", clickCount: 1, x: target.x, y: target.y });
    await send("Input.dispatchMouseEvent", { type: "mouseReleased", button: "left", clickCount: 1, x: target.x, y: target.y });
    const events = await evalJS("window.editorialPointerEvents");
    assert.deepEqual(events.map(event => [event.type, event.trusted]), [["pointerdown", true], ["click", true]], `${label}: trusted actual target`);
    observations.nestedPointers.push({ label, target, events });
  }
  const open = description => pointer(`Array.from(document.querySelectorAll('[data-tool-name=delegate]')).find(e=>e.innerText.includes(${JSON.stringify(description)}))?.querySelector('[aria-label="Open transcript"]')`, description);
  async function assertFocused(ref, parentRef, owner, route, label) {
    await until(`document.querySelector('[data-testid=transcript-view-announcement]')?.parentElement.innerText.includes(${JSON.stringify(contents[ref])}) && !document.querySelector('textarea[aria-label=Message]')`);
    const observed = await state();
    observations.nested.push({ label, ...observed });
    assert.equal(observed.route, route, `${label}: route retained`);
    assert.equal(observed.focused.type, "transcript");
    assert.deepEqual(observed.focused.params, { ref, parentRef });
    assert.equal(observed.focused.slot, "secondary");
    assert.deepEqual(observed.main, owner, `${label}: original owner unchanged`);
    assert.equal(observed.main.params.ref, root);
    assert.equal(observed.composer, false);
    assert(observed.content.includes(contents[ref]));
    // Inline collaborator reports intentionally contain descendant text. Only
    // the transcript's own agent bubbles establish the authored identity.
    assert.deepEqual(observed.authored, [contents[ref]]);
    assert(!observed.content.includes("Parent analysis: fixture-only evidence."));
    assert(observed.calls.some(call=>call.method==="thread/read"&&call.params.ref===ref));
    await screenshot(label);
    return observed;
  }
  observations.nested = [];
  observations.nestedPointers = [];
  for (const routeRef of [root, child]) {
    const prefix = routeRef === root ? "root-route" : "explicit-child-route";
    await navigateFixture(`${origin}/s/${encodeURIComponent(routeRef)}`);
    await until(`document.body.innerText.includes(${JSON.stringify(routeRef === root ? "Inspect the independent child" : contents[child])})`);
    const initial = await state();
    const owner = initial.main;
    assert.equal(owner.type, "session");
    assert.equal(owner.params.ref, root);
    if (routeRef === root) await open("Inspect the independent child");
    const route = `/s/${routeRef}`;
    async function assertParent(label) {
      if (routeRef === root) return assertFocused(child, root, owner, route, label);
      // Explicit /s/child is a SESSION pane (including its composer), not the
      // contextual read-only pane created by Open. Keep the distinction exact.
      await until(`document.querySelector('textarea[aria-label=Message]') && document.body.innerText.includes(${JSON.stringify(contents[child])})`);
      const observed = await state();
      observations.nested.push({ label, ...observed });
      assert.equal(observed.route, route);
      assert.deepEqual(observed.main, owner);
      assert.equal(observed.focused.type, "session");
      assert.equal(observed.focused.slot, "secondary");
      assert.deepEqual(observed.focused.params, { ref: child });
      assert.deepEqual(observed.authored, [contents[child]]);
      assert.equal(observed.composer, true);
      await screenshot(label);
      return observed;
    }
    const parent = await assertParent(`${prefix}-child`);
    await open("Inspect editorial-grandchild independently");
    const grand = await assertFocused(grandchild, child, owner, route, `${prefix}-grandchild`);
    await pointer(`document.querySelector('[aria-label="Back"]')`, `${prefix}-grandchild-back`);
    const back = await assertParent(`${prefix}-back-child`);
    assert.equal(back.focused.id, parent.focused.id);
    await open("Inspect editorial-grandchild independently");
    const reopened = await assertFocused(grandchild, child, owner, route, `${prefix}-reopened-grandchild`);
    assert.equal(reopened.focused.id, grand.focused.id);
    assert.equal(reopened.panes.filter(p=>p.type==="transcript"&&p.params.ref===grandchild).length, 1);
    // R1b: no location prefetch for the intermediate descendant. The explicit
    // child route must not promote it to main when opening one level further.
    assert(!reopened.calls.some(call=>call.method==="evener/navigation/read"&&call.params.resource==="location"&&call.params.ref===grandchild));
    await open("Inspect editorial-great-grandchild independently");
    await assertFocused(deeper, grandchild, owner, route, `${prefix}-great-grandchild`);
    await pointer(`document.querySelector('[aria-label="Back"]')`, `${prefix}-deeper-back`);
    const deeperBack = await assertFocused(grandchild, child, owner, route, `${prefix}-back-grandchild`);
    assert.equal(deeperBack.focused.id, grand.focused.id);
    // Use the real Sessions drawer/link: a new nested route with the SAME owner
    // must win over the retained transcript context without a document reload.
    await pointer(`document.querySelector('[aria-label="Sessions"]')`, `${prefix}-sessions`);
    await pointer(`Array.from(document.querySelectorAll('[role=treeitem] span')).find(e=>e.textContent.trim()==='Editorial fixture resumed')`, `${prefix}-new-route`);
    await until(`decodeURIComponent(location.pathname)==='/s/local:editorial-resumed'&&document.body.innerText.includes('Previous report: first review completed.')`);
    const next = await state();
    assert.deepEqual(next.main, owner);
    assert.equal(next.focused.params.ref, "local:editorial-resumed");
    assert.notEqual(next.focused.id, grand.focused.id);
    observations.nested.push({ label: `${prefix}-new-route-wins`, ...next });
    await screenshot(`${prefix}-new-route-wins`);
  }
  observations.tasks.push("Trusted actual Open root→child→grandchild and explicit child route→grandchild→great-grandchild; exact read-only focus, same original owner, immediate Back, deduplication, no prefetch, real new-route precedence");
}
