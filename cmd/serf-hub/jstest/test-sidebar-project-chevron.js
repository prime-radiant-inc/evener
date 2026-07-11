// Project headers carry a real disclosure affordance: a .project-chevron
// span ("›", same idiom as sb-children-chevron / cluster-chevron) before the
// project name. Rotation is pure CSS driven off the header's aria-expanded
// (which build + patch already maintain), so this test asserts:
//   1. the chevron exists on every project header — active AND archived stubs;
//   2. it renders before the project name;
//   3. toggling expansion flips the header's aria-expanded (the CSS hook);
//   4. the patch path keeps the chevron on the SAME reused header node;
//   5. style.css actually has the [aria-expanded="true"] rotation rule.
const fs = require("fs");
const { JSDOM } = require("jsdom");
const src = fs.readFileSync(__dirname + "/../assets/sidebar.js", "utf8");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");

function boot() {
  const dom = new JSDOM(`<!DOCTYPE html><html><body><aside id="sidebar"></aside></body></html>`, {
    runScripts: "outside-only", pretendToBeVisual: true, url: "http://localhost/",
  });
  const w = dom.window;
  w.fetch = () => Promise.resolve({ ok: true, json: () => Promise.resolve(tree()) });
  w.htmx = { process() {} };
  w.SerfAppwire = { onNotification() {}, onConnectionRestored() {} };
  w.eval(src);
  return w;
}
function tree() {
  return {
    needs_you: [], favorites: [],
    projects: [{ key: "p1", name: "alpha", working_dir: "/w/a", sessions: [] }],
    archived_projects: [{ key: "arch1", name: "old-proj", working_dir: "/w/o", sessions: [] }],
    test_runs: [],
    attentionSummary: { needsYou: 0, error: 0, working: 0 },
  };
}

const w = boot();
w.SerfSidebar.renderTree(tree());

// 1+2: chevron exists on the active project header, before the name.
const header = w.document.querySelector('[data-row-id="header:p1"]');
if (!header) throw new Error("project header not rendered");
const chev = header.querySelector(".project-chevron");
if (!chev) throw new Error("project header must contain a .project-chevron disclosure affordance");
if (chev.textContent !== "›") throw new Error("chevron must reuse the › idiom, got " + JSON.stringify(chev.textContent));
if (chev.getAttribute("aria-hidden") !== "true") throw new Error("chevron is decorative (header itself carries aria-expanded); must be aria-hidden");
const name = header.querySelector(".project-name");
if (!(chev.compareDocumentPosition(name) & w.Node.DOCUMENT_POSITION_FOLLOWING)) {
  throw new Error("chevron must render BEFORE the project name");
}

// 1 (archived stub): expand the Archived section, then check its header too.
const archSection = w.document.querySelector(".sb-section[data-row-id]");
if (!archSection) throw new Error("archived section header not rendered");
archSection.dispatchEvent(new w.Event("click", { bubbles: true }));
const archHeader = w.document.querySelector('[data-row-id="header:arch1"]');
if (!archHeader) throw new Error("archived project header not rendered after expanding Archived");
if (!archHeader.querySelector(".project-chevron")) throw new Error("archived project stubs must also carry the chevron");

// 3: click toggles aria-expanded true -> the CSS rotation hook.
if (header.getAttribute("aria-expanded") !== "false") throw new Error("collapsed header should be aria-expanded=false");
header.dispatchEvent(new w.Event("click", { bubbles: true }));
const headerOpen = w.document.querySelector('[data-row-id="header:p1"]');
if (headerOpen.getAttribute("aria-expanded") !== "true") throw new Error("click must set aria-expanded=true");
if (!headerOpen.querySelector(".project-chevron")) throw new Error("chevron must survive expansion re-render");

// 4: patch path — re-render the same tree, header node is reused and keeps its chevron.
headerOpen.__probe = true;
w.SerfSidebar.renderTree(tree());
const header2 = w.document.querySelector('[data-row-id="header:p1"]');
if (header2.__probe !== true) throw new Error("patch path must reuse the same header node");
if (!header2.querySelector(".project-chevron")) throw new Error("patched header must keep its chevron");

// 5: the stylesheet drives rotation off [aria-expanded="true"].
if (!/\.project-header\[aria-expanded="true"\]\s+\.project-chevron\s*\{[^}]*rotate\(90deg\)/.test(css)) {
  throw new Error('style.css must rotate .project-chevron 90deg under .project-header[aria-expanded="true"]');
}
if (!/\.project-chevron\s*\{[^}]*transition:[^}]*transform[^}]*var\(--motion-/.test(css)) {
  throw new Error("chevron rotation must transition via a motion token");
}

console.log("ok project-header disclosure chevron (build + patch + archived + rotation hook)");
process.exit(0); // sidebar.js's idle-resync interval keeps the loop alive otherwise
