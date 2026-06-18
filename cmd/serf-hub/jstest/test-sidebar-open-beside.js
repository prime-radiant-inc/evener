// Sidebar subagent open-beside ⇲ control:
//   1. A subagent row renders an .open-beside-btn inside .subagent-row-wrap.
//   2. Clicking it calls window.SerfPanes.open("/s/<ref>", title).
//   3. The click does NOT trigger the row's <a> navigation (stopPropagation).
//   4. When window.SerfPanes is absent the click is a no-op (iframe guard).
//   5. Ordinary session rows (.sb-row-wrap without .subagent-row-wrap) do NOT
//      get an open-beside-btn via the sidebar.js handler.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SIDEBAR_JS = "../assets/sidebar.js";
const sidebarSrc = fs.readFileSync(SIDEBAR_JS, "utf8");

// buildDom constructs a minimal sidebar fragment with one session row and
// one nested subagent row, mirroring the sidebar.html template output.
function buildDom({ panesOpen, withPanes } = {}) {
  const openCalls = [];
  const dom = new JSDOM(`<!DOCTYPE html><html><body>
    <nav class="sidebar" id="sidebar">
      <section class="sidebar-section project-section" data-project-key="myproject">
        <div class="project-children">
          <div class="session-tier" data-tier="current">
            <div class="sb-row-wrap">
              <a class="sb-row"
                 href="/s/01SESSION"
                 hx-get="/_partials/s/01SESSION/workspace"
                 hx-target="#workspace"
                 hx-swap="innerHTML"
                 hx-push-url="/s/01SESSION"
                 data-state="active">parent session</a>
              <button type="button" class="btn btn-icon archive-btn"
                data-archive-kind="session" data-archive-id="01SESSION"
                title="archive session" aria-label="archive session">archive</button>
            </div>
            <div class="subagent-row-wrap" data-ref="01SUBAGENT" data-title="do some work">
              <a class="sb-row sub subagent-row"
                 data-state="idle"
                 href="/s/01SUBAGENT"
                 hx-get="/_partials/s/01SUBAGENT/workspace"
                 hx-target="#workspace"
                 hx-swap="innerHTML"
                 hx-push-url="/s/01SUBAGENT">
                <span class="subagent-glyph" data-state="idle" aria-hidden="true">✓</span>
                <span class="subagent-title">do some work</span>
              </a>
              <span role="button" tabindex="0" class="open-beside-btn"
                    aria-label="open subagent beside" title="open beside">⇲</span>
            </div>
          </div>
        </div>
      </section>
    </nav>
    <div id="workspace"></div>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });

  const { window } = dom;

  // Stub fetch so archive handler doesn't throw.
  window.fetch = function (url, opts) {
    return Promise.resolve({ ok: true, json: function () { return Promise.resolve({}); } });
  };
  // Stub htmx.
  window.htmx = { trigger: function () {} };
  // Conditionally install SerfPanes stub.
  if (withPanes !== false) {
    window.SerfPanes = {
      open: panesOpen || function (href, title) { openCalls.push({ href, title }); },
    };
  }

  return { dom, window, openCalls };
}

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const wait = (ms) => new Promise(r => setTimeout(r, ms || 30));

(async () => {

  // ---- Test 1: subagent row has an .open-beside-btn -------------------------
  {
    const { window } = buildDom();
    window.eval(sidebarSrc);
    await wait(30);

    const btn = window.document.querySelector(".subagent-row-wrap .open-beside-btn");
    pass(btn !== null, "subagent-row-wrap should contain an .open-beside-btn");
    pass(btn && btn.getAttribute("role") === "button", ".open-beside-btn should have role=button");
    pass(btn && btn.textContent.trim() === "⇲", ".open-beside-btn should show ⇲ glyph");
  }

  // ---- Test 2: clicking open-beside calls SerfPanes.open("/s/<ref>", title) -
  {
    const openCalls = [];
    const { window } = buildDom({ panesOpen: (href, title) => openCalls.push({ href, title }) });
    window.eval(sidebarSrc);
    await wait(30);

    const btn = window.document.querySelector(".subagent-row-wrap .open-beside-btn");
    pass(btn !== null, "open-beside-btn should exist (test 2)");

    btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    await wait(10);

    pass(openCalls.length === 1, "SerfPanes.open should be called once, got " + openCalls.length);
    pass(
      openCalls[0] && openCalls[0].href === "/s/01SUBAGENT",
      "SerfPanes.open href should be /s/01SUBAGENT, got " + JSON.stringify(openCalls[0] && openCalls[0].href)
    );
    pass(
      openCalls[0] && openCalls[0].title === "do some work",
      "SerfPanes.open title should be 'do some work', got " + JSON.stringify(openCalls[0] && openCalls[0].title)
    );
  }

  // ---- Test 3: clicking open-beside does NOT navigate the <a> row ----------
  {
    const { window } = buildDom();
    window.eval(sidebarSrc);
    await wait(30);

    const btn = window.document.querySelector(".subagent-row-wrap .open-beside-btn");
    pass(btn !== null, "open-beside-btn should exist (test 3)");

    const rowA = window.document.querySelector(".subagent-row-wrap .subagent-row");
    let rowClicked = false;
    rowA.addEventListener("click", function () { rowClicked = true; });

    const beforeHref = window.location.href;
    btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    await wait(10);

    pass(!rowClicked, "open-beside click must not propagate to the subagent-row <a>");
    pass(window.location.href === beforeHref, "open-beside click must not change location");
  }

  // ---- Test 4: absent SerfPanes — click is a no-op (iframe guard) ----------
  {
    const { window, openCalls } = buildDom({ withPanes: false });
    window.eval(sidebarSrc);
    await wait(30);

    const btn = window.document.querySelector(".subagent-row-wrap .open-beside-btn");
    pass(btn !== null, "open-beside-btn should still exist in DOM when SerfPanes absent (test 4)");

    // Clicking should not throw and should not open anything.
    let threw = false;
    try {
      btn.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
    } catch (e) {
      threw = true;
    }
    await wait(10);
    pass(!threw, "open-beside click with no SerfPanes must not throw");
    pass(openCalls.length === 0, "open calls should remain 0 when SerfPanes absent");
  }

  // ---- Test 5: ordinary session rows (.sb-row-wrap) do NOT trigger SerfPanes
  {
    const openCalls = [];
    const { window } = buildDom({ panesOpen: (href, title) => openCalls.push({ href, title }) });
    window.eval(sidebarSrc);
    await wait(30);

    // The session row's .sb-row-wrap has an archive-btn but no open-beside-btn.
    const sessionWrap = window.document.querySelector(".sb-row-wrap");
    pass(sessionWrap !== null, "session .sb-row-wrap should exist");
    const sessionOpenBeside = sessionWrap.querySelector(".open-beside-btn");
    pass(sessionOpenBeside === null, "session .sb-row-wrap should NOT contain an .open-beside-btn");
  }

  if (failures.length === 0) {
    console.log("PASS: sidebar open-beside — renders, calls SerfPanes.open, stopPropagation, iframe guard, session rows excluded");
    process.exit(0);
  } else {
    for (const f of failures) console.log(" " + f);
    process.exit(1);
  }
})();
