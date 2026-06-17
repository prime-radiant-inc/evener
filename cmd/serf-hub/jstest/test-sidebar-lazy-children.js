// Verify the sidebar's lazy children load: a collapsed project ships an EMPTY
// .project-children; the first chevron expand fetches /_partials/sidebar/project
// and injects the returned rows; a second expand does NOT refetch; and an
// auto-expanded (live) project that already has children inline never fetches.
const fs = require("fs");
const { JSDOM } = require("jsdom");

const SIDEBAR_JS = "../assets/sidebar.js";
const sidebarSrc = fs.readFileSync(SIDEBAR_JS, "utf8");

// buildDom renders one collapsed project (empty children, awaiting lazy load)
// and one auto-expanded live project (children already inline). Mirrors the
// server markup: collapsed sections carry the `collapsed` class +
// data-default-expanded="false"; expanded ones omit `collapsed` +
// data-default-expanded="true".
function buildDom() {
  return new JSDOM(`<!DOCTYPE html><html><body>
    <nav class="sidebar">
      <section class="sidebar-section project-section collapsed"
               data-project-key="lazy-proj" data-default-expanded="false">
        <header class="project-header">
          <button type="button" class="project-chevron" aria-expanded="false">▸</button>
          <span class="project-name">lazy-proj</span>
        </header>
        <div class="project-children"></div>
      </section>
      <section class="sidebar-section project-section"
               data-project-key="live-proj" data-default-expanded="true">
        <header class="project-header">
          <button type="button" class="project-chevron" aria-expanded="true">▾</button>
          <span class="project-name">live-proj</span>
        </header>
        <div class="project-children">
          <a class="sb-row" href="/s/01LIVE">live row</a>
        </div>
      </section>
    </nav>
  </body></html>`, { runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/" });
}

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const wait = () => new Promise(resolve => setTimeout(resolve, 30));

(async () => {
  const dom = buildDom();
  const { window } = dom;

  // Stub fetch: record calls and return a children fragment for lazy-proj.
  const fetchCalls = [];
  window.fetch = function (url, opts) {
    fetchCalls.push({ url: url, opts: opts });
    return Promise.resolve({
      ok: true,
      text: function () {
        return Promise.resolve(
          '<div class="session-tier" data-tier="current">' +
          '<a class="sb-row" href="/s/01LAZY">lazy row</a></div>'
        );
      },
    });
  };

  window.eval(sidebarSrc);
  await wait();

  const lazy = window.document.querySelector('[data-project-key="lazy-proj"]');
  const lazyChevron = lazy.querySelector(".project-chevron");
  const lazyChildren = lazy.querySelector(".project-children");

  pass(lazy.classList.contains("collapsed"), "lazy-proj starts collapsed");
  pass(lazyChildren.children.length === 0, "lazy-proj children start empty");

  // First expand: should fetch the project partial and inject the rows.
  lazyChevron.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  await wait();

  pass(!lazy.classList.contains("collapsed"), "lazy-proj expands on first click");
  pass(fetchCalls.length === 1, "first expand issues exactly one fetch, got " + fetchCalls.length);
  if (fetchCalls.length >= 1) {
    pass(
      fetchCalls[0].url.indexOf("/_partials/sidebar/project") === 0,
      "fetch hits the project partial endpoint, got " + fetchCalls[0].url
    );
    pass(
      fetchCalls[0].url.indexOf("key=lazy-proj") !== -1,
      "fetch carries the project key, got " + fetchCalls[0].url
    );
    const hx = fetchCalls[0].opts && fetchCalls[0].opts.headers && fetchCalls[0].opts.headers["HX-Request"];
    pass(hx === "true", "fetch sends HX-Request:true header, got " + JSON.stringify(hx));
  }
  pass(
    lazyChildren.querySelector('a[href="/s/01LAZY"]') !== null,
    "lazy rows injected into children:\n" + lazyChildren.innerHTML
  );

  // Collapse, then expand again: must NOT refetch.
  lazyChevron.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  await wait();
  pass(lazy.classList.contains("collapsed"), "lazy-proj collapses on second click");
  lazyChevron.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  await wait();
  pass(!lazy.classList.contains("collapsed"), "lazy-proj re-expands on third click");
  pass(fetchCalls.length === 1, "re-expand does NOT refetch, fetch count = " + fetchCalls.length);

  // The live project already has its children inline → never fetched.
  const live = window.document.querySelector('[data-project-key="live-proj"]');
  pass(!live.classList.contains("collapsed"), "live-proj is expanded (auto-open)");
  const liveChevron = live.querySelector(".project-chevron");
  // Collapse + expand the live project; still no fetch because children exist.
  liveChevron.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  await wait();
  liveChevron.dispatchEvent(new window.MouseEvent("click", { bubbles: true, cancelable: true }));
  await wait();
  pass(fetchCalls.length === 1, "expanding a project with inline children does NOT fetch, count = " + fetchCalls.length);

  if (failures.length === 0) {
    console.log("PASS: sidebar lazy children — fetch on first expand, no refetch, inline skip");
    process.exit(0);
  } else {
    for (const f of failures) console.log(" " + f);
    process.exit(1);
  }
})();
