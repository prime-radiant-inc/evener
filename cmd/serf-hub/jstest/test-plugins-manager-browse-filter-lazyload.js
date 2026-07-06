// Loads the inline <script> from templates/partials/settings/plugins-manager.html
// into JSDOM and exercises the Browse filter's lazy-load-on-first-keystroke
// behavior (Part 1 of the plugin-marketplace-improvements design, Task 3):
// the first non-empty filter keystroke fetches every not-yet-loaded
// marketplace's catalog once (showing a "Loading marketplaces…" affordance
// meanwhile), then filters over the now-cached data; a later keystroke
// filters the cache with no further RPCs.

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
  input.value = value;
  input.dispatchEvent(new dom.window.Event("input", { bubbles: true }));
}

(async function main() {
  // Case 1: first keystroke fetches every not-yet-loaded marketplace once,
  // then filters over the (now cached) data; a later keystroke does not
  // re-fetch.
  {
    const dom = makeDom();
    const browseCalls = [];
    dom.window.pluginsAdmin = {
      marketplaceList: async () => ({
        marketplaces: [
          { name: "alpha", source: { kind: "url", url: "https://example.com/alpha.git" }, lastUpdated: 0 },
          { name: "beta", source: { kind: "url", url: "https://example.com/beta.git" }, lastUpdated: 0 },
        ],
      }),
      pluginList: async () => ({ plugins: [] }),
      marketplaceBrowse: async (name) => {
        browseCalls.push(name);
        if (name === "alpha") return { name: "alpha", plugins: [{ name: "elements-of-style", description: "" }] };
        return { name: "beta", plugins: [{ name: "private-journal-mcp", description: "" }] };
      },
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;

    pass(browseCalls.length === 0, "no catalogs are fetched before the filter is used");

    typeInFilter(doc, dom, "elements");
    await tick(dom, 170); // past the 150ms debounce
    pass(browseCalls.includes("alpha") && browseCalls.includes("beta"),
      "first filter keystroke fetches every not-yet-loaded marketplace, got " + JSON.stringify(browseCalls));
    pass(doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').getAttribute("aria-expanded") === "true",
      "alpha auto-expands once its (now-loaded) catalog matches the query");
    pass(!/Loading marketplaces/.test(doc.getElementById("browse-section").textContent),
      "the loading-marketplaces affordance clears once every fetch settles");

    const callsBefore = browseCalls.length;
    typeInFilter(doc, dom, "element");
    await tick(dom, 170);
    pass(browseCalls.length === callsBefore, "a later keystroke filters the cache with no new RPCs, calls=" + browseCalls.length);
  }

  // Case 2: while a not-yet-loaded catalog's fetch is still pending, the tree
  // shows the "Loading marketplaces…" affordance.
  {
    const dom = makeDom();
    let resolvePending;
    dom.window.pluginsAdmin = {
      marketplaceList: async () => ({
        marketplaces: [{ name: "alpha", source: { kind: "url", url: "https://example.com/alpha.git" }, lastUpdated: 0 }],
      }),
      pluginList: async () => ({ plugins: [] }),
      marketplaceBrowse: async () => new Promise((resolve) => {
        resolvePending = () => resolve({ name: "alpha", plugins: [] });
      }),
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;

    typeInFilter(doc, dom, "x");
    await tick(dom, 170);
    pass(/Loading marketplaces…/.test(doc.getElementById("browse-section").textContent),
      "while a not-yet-loaded catalog is still fetching, the tree shows the loading-marketplaces affordance");

    resolvePending();
    await tick(dom, 20);
    pass(!/Loading marketplaces…/.test(doc.getElementById("browse-section").textContent),
      "the affordance clears once the fetch settles");
  }

  if (failures.length === 0) {
    console.log("PASS: plugins-manager browse filter lazy-load");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
