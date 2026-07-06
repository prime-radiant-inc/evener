// Loads the inline <script> from templates/partials/settings/plugins-manager.html
// into JSDOM and exercises the Browse tree's filter box (Part 1 of the
// plugin-marketplace-improvements design, Task 2): typing filters plugins
// across already-loaded marketplace catalogs (debounced ~150ms), matching
// marketplaces auto-expand while non-matching ones collapse, an empty result
// shows a "No plugins match" message, clearing the filter collapses the whole
// tree, and the input keeps focus/cursor position across the re-render the
// debounced filter triggers.

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const html = fs.readFileSync(
  path.resolve(__dirname, "../templates/partials/settings/plugins-manager.html"),
  "utf8",
);
const src = html.match(/<script>([\s\S]*?)<\/script>/)[1];

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };
const tick = (dom, ms) => new Promise((r) => dom.window.setTimeout(r, ms));

function makeDom() {
  return new JSDOM(
    `<!DOCTYPE html><html><body>
      <div id="plugins-manager-root" data-loaded="false">
        <p class="settings-help">Loading…</p>
      </div>
    </body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/settings/plugins-manager" },
  );
}

async function waitLoaded(dom) {
  const root = dom.window.document.getElementById("plugins-manager-root");
  for (let i = 0; i < 100 && root.dataset.loaded !== "true"; i++) await tick(dom, 0);
  return root;
}

function typeInFilter(doc, dom, value) {
  const input = doc.getElementById("browse-filter-input");
  input.focus();
  input.value = value;
  input.setSelectionRange(value.length, value.length);
  input.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
}

async function testMatchingAndCollapsing() {
  const dom = makeDom();
  dom.window.pluginsAdmin = {
    marketplaceList: async () => ({
      marketplaces: [
        { name: "alpha", source: { kind: "url", url: "https://example.com/alpha.git" }, lastUpdated: 0 },
        { name: "beta", source: { kind: "url", url: "https://example.com/beta.git" }, lastUpdated: 0 },
      ],
    }),
    pluginList: async () => ({ plugins: [] }),
    marketplaceBrowse: async (name) => name === "alpha"
      ? { name: "alpha", plugins: [
          { name: "elements-of-style", description: "Strunk's writing rules" },
          { name: "sample-other", description: "Unrelated plugin" },
        ] }
      : { name: "beta", plugins: [{ name: "private-journal-mcp", description: "Journal MCP server" }] },
  };
  dom.window.eval(src);
  await waitLoaded(dom);
  const doc = dom.window.document;

  // Preload both catalogs (Task 2 filters already-loaded catalogs only).
  doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').click();
  doc.querySelector('.browse-marketplace-toggle[data-marketplace="beta"]').click();
  await tick(dom, 20);

  // A name match: beta (no match) collapses, alpha (match) stays/auto-expands
  // and shows only the matching row.
  typeInFilter(doc, dom, "elements");
  await tick(dom, 170); // past the 150ms debounce
  pass(doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').getAttribute("aria-expanded") === "true",
    "alpha (has a match) stays/auto-expands");
  pass(doc.querySelector('.browse-marketplace-node[data-marketplace-node="beta"]') === null,
    "beta (no match) is hidden entirely, not just collapsed");
  let alphaRows = doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row');
  pass(alphaRows.length === 1 && alphaRows[0].textContent.includes("elements-of-style"),
    "alpha shows only the matching row");
  pass(!doc.querySelector('.browse-marketplace-node[data-marketplace-node="alpha"]').textContent.includes("sample-other"),
    "alpha's non-matching sibling plugin is hidden");

  // A description match also auto-expands.
  typeInFilter(doc, dom, "journal");
  await tick(dom, 170);
  pass(doc.querySelector('.browse-marketplace-toggle[data-marketplace="beta"]').getAttribute("aria-expanded") === "true",
    "a description match auto-expands beta");
  pass(doc.querySelector('.browse-marketplace-node[data-marketplace-node="alpha"]') === null,
    "alpha (no longer matching) is hidden entirely");

  // No matches anywhere: the global empty-result message, no tree nodes.
  typeInFilter(doc, dom, "zzz-nothing-matches");
  await tick(dom, 170);
  pass(/No plugins match "zzz-nothing-matches"/.test(doc.getElementById("browse-section").textContent),
    "empty filter result shows the no-matches message");
  pass(doc.querySelectorAll(".browse-marketplace-node").length === 0,
    "no marketplace nodes render when nothing matches");

  // Clearing the filter restores the fully collapsed tree.
  typeInFilter(doc, dom, "");
  await tick(dom, 170);
  const toggles = doc.querySelectorAll(".browse-marketplace-toggle");
  pass(toggles.length === 2, "clearing the filter shows both marketplace nodes again");
  pass(Array.from(toggles).every(t => t.getAttribute("aria-expanded") === "false"),
    "clearing the filter collapses the whole tree");

  // Focus/cursor survive the debounced re-render (the filter input must not
  // be recreated wholesale mid-type, or render() would steal focus).
  typeInFilter(doc, dom, "ele");
  await tick(dom, 170);
  const liveInput = doc.getElementById("browse-filter-input");
  pass(doc.activeElement === liveInput, "the filter input keeps focus after the debounced re-render");
  pass(liveInput.selectionStart === 3 && liveInput.selectionEnd === 3, "cursor stays at the end of the typed text");
}

// A marketplace whose catalog failed to load must stay visible in the
// Browse tree while a filter query is active — even when no other
// marketplace's catalog matches the query — so its existing "Failed to
// browse: …" rendering (unchanged by this task) stays reachable instead of
// vanishing behind the filter (regression guard for the errored-marketplace
// case the initial per-row-filter implementation missed).
async function testErroredMarketplaceStaysVisible() {
  const dom = makeDom();
  dom.window.pluginsAdmin = {
    marketplaceList: async () => ({
      marketplaces: [
        { name: "broken", source: { kind: "url", url: "https://example.com/broken.git" }, lastUpdated: 0 },
        { name: "quiet", source: { kind: "url", url: "https://example.com/quiet.git" }, lastUpdated: 0 },
      ],
    }),
    pluginList: async () => ({ plugins: [] }),
    marketplaceBrowse: async (name) => {
      if (name === "broken") throw new Error("connection refused");
      return { name: "quiet", plugins: [{ name: "sample-plugin", description: "Nothing relevant here" }] };
    },
  };
  dom.window.eval(src);
  await waitLoaded(dom);
  const doc = dom.window.document;

  // Preload both catalogs, same as testMatchingAndCollapsing: expand each
  // node once so browseCatalogs["broken"] holds {error: ...} before filtering.
  doc.querySelector('.browse-marketplace-toggle[data-marketplace="broken"]').click();
  doc.querySelector('.browse-marketplace-toggle[data-marketplace="quiet"]').click();
  await tick(dom, 20);
  pass(/Failed to browse: connection refused/.test(
    doc.querySelector('.browse-marketplace-node[data-marketplace-node="broken"]').textContent),
    "broken shows its error message before any filter is applied");

  // Filter on a query that matches nothing in quiet's (successfully-loaded)
  // catalog. broken's catalog already errored, so its match status can never
  // be resolved — it must stay visible rather than disappear behind the
  // filter or a false "No plugins match" message.
  typeInFilter(doc, dom, "zzz-nothing-matches");
  await tick(dom, 170);
  pass(doc.querySelector('.browse-marketplace-node[data-marketplace-node="broken"]') !== null,
    "broken (errored catalog) stays visible while filtering, even though nothing else matches");
  pass(doc.querySelector('.browse-marketplace-node[data-marketplace-node="quiet"]') === null,
    "quiet (successfully-loaded, no match) is still hidden entirely");

  // broken was collapsed by applyFilter's auto-collapse (it's not a
  // confirmed match), but it must still be expandable to reveal the error.
  const brokenToggle = doc.querySelector('.browse-marketplace-toggle[data-marketplace="broken"]');
  pass(brokenToggle.getAttribute("aria-expanded") === "false", "broken auto-collapses while filtering, like any non-match");
  brokenToggle.click();
  pass(/Failed to browse: connection refused/.test(
    doc.querySelector('.browse-marketplace-node[data-marketplace-node="broken"]').textContent),
    "broken still shows 'Failed to browse:' once re-expanded while filtering");
}

(async function main() {
  await testMatchingAndCollapsing();
  await testErroredMarketplaceStaysVisible();

  if (failures.length === 0) {
    console.log("PASS: plugins-manager browse filter");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
