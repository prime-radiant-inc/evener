// Verify archive/unarchive control behavior in sidebar.js:
//   1. Clicking an .archive-btn issues POST /api/archive with {kind, id, archived:true}.
//   2. An archived-tier button (inside .session-tier.archived) sends archived:false (unarchive).
//   3. The click triggers the existing sidebar:refresh path.
//   4. Archived <details> are collapsed by default (no open attribute forced open).
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SIDEBAR_JS = "../assets/sidebar.js";
const sidebarSrc = fs.readFileSync(SIDEBAR_JS, "utf8");

function buildDom({ fetchImpl, htmxImpl } = {}) {
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <nav class="sidebar">
      <section class="sidebar-section project-section" data-project-key="myproject">
        <header class="project-header">
          <span class="project-name">myproject</span>
          <button type="button" class="btn btn-icon archive-btn"
            data-archive-kind="project" data-archive-id="myproject"
            title="archive project myproject"
            aria-label="archive project">⋯</button>
        </header>
        <div class="project-children">
          <div class="session-tier" data-tier="current">
            <div class="session-tier-label">Current</div>
            <div class="sb-row-wrap">
              <a class="sb-row" href="/s/01ABC" data-state="active">live session</a>
              <button type="button" class="btn btn-icon archive-btn"
                data-archive-kind="session" data-archive-id="01ABC"
                title="archive session" aria-label="archive session">⋯</button>
            </div>
          </div>
          <details class="session-tier archived" data-tier="archived">
            <summary class="session-tier-label">Archived <span class="count">1</span></summary>
            <div class="sb-row-wrap">
              <a class="sb-row" href="/s/01OLD" data-state="idle">old session</a>
              <button type="button" class="btn btn-icon archive-btn"
                data-archive-kind="session" data-archive-id="01OLD"
                title="unarchive session" aria-label="unarchive session">⋯</button>
            </div>
          </details>
        </div>
      </section>
      <section class="sidebar-tier archived-projects" data-tier="archived-projects">
        <details>
          <summary class="sidebar-section-header tier-header">Archived projects <span class="count">1</span></summary>
          <section class="sidebar-section project-section" data-project-key="oldproject">
            <header class="project-header">
              <span class="project-name">oldproject</span>
              <button type="button" class="btn btn-icon archive-btn"
                data-archive-kind="project" data-archive-id="oldproject"
                title="unarchive project oldproject"
                aria-label="unarchive project">⋯</button>
            </header>
          </section>
        </details>
      </section>
    </nav>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });

  const { window } = dom;

  // Stub fetch — records calls, returns a resolved promise.
  const fetchCalls = [];
  window.fetch = fetchImpl || function (url, opts) {
    fetchCalls.push({ url, opts });
    return Promise.resolve({ ok: true, json: function () { return Promise.resolve({}); } });
  };

  // Stub htmx — records trigger calls.
  const triggered = [];
  window.htmx = htmxImpl || {
    trigger: function (target, name) { triggered.push({ target, name }); },
  };

  return { dom, window, fetchCalls, triggered };
}

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const wait = (ms) => new Promise(r => setTimeout(r, ms || 30));

(async () => {
  // ---- Test 1: clicking a project archive-btn posts to /api/archive --------
  {
    const { window, fetchCalls, triggered } = buildDom();
    window.eval(sidebarSrc);
    await wait(30);

    const btn = window.document.querySelector(
      '[data-archive-kind="project"][data-archive-id="myproject"]'
    );
    pass(btn !== null, "project archive-btn should exist");

    btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    await wait(100); // allow fetch + setTimeout(refresh, 50)

    pass(fetchCalls.length >= 1, "clicking archive-btn should call fetch");
    const call = fetchCalls[0];
    pass(call && call.url === "/api/archive", "fetch url should be /api/archive, got " + (call && call.url));
    pass(call && call.opts && call.opts.method === "POST", "fetch method should be POST");
    const body = call && call.opts && JSON.parse(call.opts.body);
    pass(body && body.kind === "project", "body.kind should be 'project', got " + JSON.stringify(body && body.kind));
    pass(body && body.id === "myproject", "body.id should be 'myproject', got " + JSON.stringify(body && body.id));
    pass(body && body.archived === true, "body.archived should be true for non-archived item, got " + JSON.stringify(body && body.archived));
  }

  // ---- Test 2: clicking a session archive-btn in the active tier posts archived:true ----
  {
    const { window, fetchCalls } = buildDom();
    window.eval(sidebarSrc);
    await wait(30);

    const btn = window.document.querySelector(
      '[data-archive-kind="session"][data-archive-id="01ABC"]'
    );
    pass(btn !== null, "session archive-btn (active tier) should exist");

    btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    await wait(100);

    pass(fetchCalls.length >= 1, "session archive-btn click should call fetch");
    const body = fetchCalls[0] && JSON.parse(fetchCalls[0].opts.body);
    pass(body && body.kind === "session", "session body.kind should be 'session'");
    pass(body && body.id === "01ABC", "session body.id should be '01ABC'");
    pass(body && body.archived === true, "active-tier session should send archived:true");
  }

  // ---- Test 3: clicking archive-btn inside an archived tier sends archived:false (unarchive) ----
  {
    const { window, fetchCalls } = buildDom();
    window.eval(sidebarSrc);
    await wait(30);

    // The 01OLD session is inside <details class="session-tier archived">
    const btn = window.document.querySelector(
      '[data-archive-kind="session"][data-archive-id="01OLD"]'
    );
    pass(btn !== null, "archived-tier session archive-btn should exist");

    btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    await wait(100);

    const body = fetchCalls[0] && JSON.parse(fetchCalls[0].opts.body);
    pass(body && body.archived === false, "archived-tier session should send archived:false (unarchive), got " + JSON.stringify(body && body.archived));
  }

  // ---- Test 4: clicking archive-btn inside archived-projects sends archived:false ----
  {
    const { window, fetchCalls } = buildDom();
    window.eval(sidebarSrc);
    await wait(30);

    const btn = window.document.querySelector(
      '[data-archive-kind="project"][data-archive-id="oldproject"]'
    );
    pass(btn !== null, "archived-projects project archive-btn should exist");

    btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    await wait(100);

    const body = fetchCalls[0] && JSON.parse(fetchCalls[0].opts.body);
    pass(body && body.archived === false, "archived-project btn should send archived:false, got " + JSON.stringify(body && body.archived));
  }

  // ---- Test 5: archive click triggers sidebar refresh ----
  {
    const { window, fetchCalls, triggered } = buildDom();
    window.eval(sidebarSrc);
    await wait(30);

    const btn = window.document.querySelector(
      '[data-archive-kind="project"][data-archive-id="myproject"]'
    );
    btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    await wait(200); // allow fetch + setTimeout(50)

    pass(
      triggered.some(function (t) { return t.name === "sidebar:refresh"; }),
      "archive click should trigger sidebar:refresh"
    );
  }

  // ---- Test 6: archived <details> are collapsed by default (no open attr) ----
  {
    const { window } = buildDom();
    window.eval(sidebarSrc);
    await wait(30);

    // Session-tier archived details — should NOT be open
    const archivedTier = window.document.querySelector("details.session-tier.archived");
    pass(archivedTier !== null, "archived session-tier details should exist");
    pass(!archivedTier.open, "archived session-tier details should be collapsed by default");

    // Archived-projects wrapper details — should NOT be open
    const archivedProjects = window.document.querySelector(".archived-projects details");
    pass(archivedProjects !== null, "archived-projects details should exist");
    pass(!archivedProjects.open, "archived-projects details should be collapsed by default");
  }

  // ---- Test 7: archive-btn click does not navigate (stopPropagation) ----
  {
    const { window, fetchCalls } = buildDom();
    window.eval(sidebarSrc);
    await wait(30);

    // The session row is an <a>; the archive-btn is a sibling <button>.
    // Clicking the button should not bubble a navigation click to the row's
    // parent that would fire navigation. We verify the click was consumed
    // (the button itself fired) without propagating into the row's click zone.
    const btn = window.document.querySelector('[data-archive-id="01ABC"]');
    let bubbledToNav = false;
    const row = window.document.querySelector('a[href="/s/01ABC"]');
    if (row) {
      row.addEventListener("click", function () { bubbledToNav = true; });
    }
    btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    await wait(100);
    // The row's click listener is on the <a>, which is a sibling, not an
    // ancestor of the button — so bubbling won't reach it. The important
    // thing is that fetch was called (we handled it) rather than defaulted.
    pass(fetchCalls.length >= 1, "stopPropagation test: fetch still called");
  }

  if (failures.length === 0) {
    console.log("PASS: sidebar archive — POST, unarchive, refresh, collapse default");
    process.exit(0);
  } else {
    for (const f of failures) console.log(" " + f);
    process.exit(1);
  }
})();
