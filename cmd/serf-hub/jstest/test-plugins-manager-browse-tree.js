// Loads the inline <script> from templates/partials/settings/plugins-manager.html
// into JSDOM, mocks window.pluginsAdmin, and exercises the Browse tree (Part 1
// of the plugin-marketplace-improvements design): marketplace nodes collapsed
// by default, lazy-loaded catalogs on first expand (cached thereafter),
// per-node loading/error states, inline install, marketplace-refresh cache
// invalidation, and the zero-marketplaces empty state.

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

const html = fs.readFileSync(
  path.resolve(__dirname, "../templates/partials/settings/plugins-manager.html"),
  "utf8",
);
const scriptMatch = html.match(/<script>([\s\S]*?)<\/script>/);
if (!scriptMatch) { console.error("FAIL: plugins-manager.html should contain a <script> block"); process.exit(1); }
const src = scriptMatch[1];

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
  pass(root.dataset.loaded === "true", "plugins-manager pane should finish loading");
  return root;
}

(async function main() {
  // Case 1: two marketplaces render as collapsed nodes; expanding one
  // lazy-loads its catalog once (cached on re-expand); the other node is
  // unaffected by the first's error; install works from within the tree.
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
      pluginList: async () => ({
        plugins: [{ plugin: "already-installed", marketplace: "alpha", version: "1.0.0", enabled: true }],
      }),
      marketplaceBrowse: async (name) => {
        browseCalls.push(name);
        if (name === "alpha") {
          return { name: "alpha", plugins: [
            { name: "already-installed", description: "Already on the box" },
            { name: "fresh-plugin", description: "Not installed yet", category: "tools" },
          ] };
        }
        throw new Error("network down");
      },
      marketplaceRefresh: async () => ({}),
      pluginInstall: async () => ({}),
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;

    const nodes = doc.querySelectorAll("#browse-section .browse-marketplace-node");
    pass(nodes.length === 2, "both marketplaces render as tree nodes, got " + nodes.length);
    pass(browseCalls.length === 0, "no catalog is fetched before any node is expanded, got " + browseCalls.length);
    pass(doc.querySelectorAll("#browse-section .browse-marketplace-plugins").length === 0,
      "collapsed nodes render no plugin list");

    const alphaToggle = doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]');
    alphaToggle.click();
    await tick(dom, 20);
    pass(browseCalls.filter(n => n === "alpha").length === 1, "expanding alpha fetches its catalog exactly once");
    // render() replaces #plugins-manager-root's innerHTML wholesale (this pane
    // never reconciles DOM identity across renders, unlike the sidebar), so
    // the pre-click `alphaToggle` handle is detached the instant render() ran
    // inside its own click handler. Re-query the live node instead of reusing it.
    pass(doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').getAttribute("aria-expanded") === "true",
      "alpha toggle reports aria-expanded=true");
    let alphaRows = doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row');
    pass(alphaRows.length === 2, "alpha shows both catalog plugins, got " + alphaRows.length);
    const installedRow = Array.from(alphaRows).find(r => r.textContent.includes("already-installed"));
    pass(installedRow && installedRow.querySelector(".status-badge"), "already-installed plugin shows the Installed badge");
    const freshRow = Array.from(alphaRows).find(r => r.textContent.includes("fresh-plugin"));
    pass(freshRow && freshRow.querySelector('button[data-action="install"]'), "fresh-plugin shows an Install button");

    // Collapse then re-expand alpha: no second fetch, cached rows reappear.
    // (Re-query the toggle each time — see the staleness note above.)
    doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').click();
    await tick(dom, 20);
    pass(doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row').length === 0,
      "collapsing alpha hides its rows");
    doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').click();
    await tick(dom, 20);
    pass(browseCalls.filter(n => n === "alpha").length === 1, "re-expanding alpha does not re-fetch, still 1 call");
    pass(doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row').length === 2,
      "re-expanding alpha shows the cached rows");

    // Expand beta: its browse call fails; alpha's already-rendered rows are unaffected.
    const betaToggle = doc.querySelector('.browse-marketplace-toggle[data-marketplace="beta"]');
    betaToggle.click();
    await tick(dom, 20);
    const betaBody = doc.querySelector('.browse-marketplace-node[data-marketplace-node="beta"] .browse-marketplace-plugins');
    pass(betaBody && /Failed to browse: network down/.test(betaBody.textContent), "beta shows its own inline error");
    pass(doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row').length === 2,
      "alpha's rows are unaffected by beta's error");

    // Installing fresh-plugin from the tree calls pluginInstall(plugin, marketplace)
    // and flips the row to Installed.
    let installArgs = null;
    dom.window.pluginsAdmin.pluginInstall = async (plugin, marketplace) => { installArgs = [plugin, marketplace]; return {}; };
    dom.window.pluginsAdmin.pluginList = async () => ({
      plugins: [
        { plugin: "already-installed", marketplace: "alpha", version: "1.0.0", enabled: true },
        { plugin: "fresh-plugin", marketplace: "alpha", version: "1.0.0", enabled: true },
      ],
    });
    doc.querySelector('.browse-marketplace-node[data-marketplace-node="alpha"] button[data-action="install"]').click();
    await tick(dom, 20);
    pass(installArgs && installArgs[0] === "fresh-plugin" && installArgs[1] === "alpha",
      "install calls pluginInstall with (plugin, marketplace) = " + JSON.stringify(installArgs));
    alphaRows = doc.querySelectorAll('.browse-marketplace-node[data-marketplace-node="alpha"] .settings-collection-row');
    const freshRowAfter = Array.from(alphaRows).find(r => r.textContent.includes("fresh-plugin"));
    pass(freshRowAfter && freshRowAfter.querySelector(".status-badge"), "fresh-plugin flips to Installed after install");
  }

  // Case 2: refreshing a marketplace from the Marketplaces section invalidates
  // its Browse cache; the next fetch happens immediately if the node is
  // currently expanded.
  {
    const dom = makeDom();
    let browseCalls = 0;
    dom.window.pluginsAdmin = {
      marketplaceList: async () => ({
        marketplaces: [{ name: "alpha", source: { kind: "url", url: "https://example.com/alpha.git" }, lastUpdated: 0 }],
      }),
      pluginList: async () => ({ plugins: [] }),
      marketplaceBrowse: async () => { browseCalls++; return { name: "alpha", plugins: [{ name: "p1", description: "" }] }; },
      marketplaceRefresh: async () => ({}),
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;

    doc.querySelector('.browse-marketplace-toggle[data-marketplace="alpha"]').click();
    await tick(dom, 20);
    pass(browseCalls === 1, "initial expand fetches once, got " + browseCalls);

    doc.querySelector('#marketplaces-section [data-marketplace="alpha"] button[data-action="mkt-refresh"]').click();
    await tick(dom, 20);
    pass(browseCalls === 2, "refreshing an expanded marketplace re-fetches its catalog, calls=" + browseCalls);
  }

  // Case 3: zero marketplaces registered shows the empty-state guidance.
  {
    const dom = makeDom();
    dom.window.pluginsAdmin = {
      marketplaceList: async () => ({ marketplaces: [] }),
      pluginList: async () => ({ plugins: [] }),
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;
    pass(/No marketplaces registered/.test(doc.querySelector("#browse-section").textContent),
      "zero marketplaces shows the empty-state guidance in Browse");
  }

  if (failures.length === 0) {
    console.log("PASS: plugins-manager browse tree");
    process.exit(0);
  }
  for (const f of failures) console.log(" " + f);
  process.exit(1);
})().catch((e) => { console.error(e && e.stack ? e.stack : e); process.exit(1); });
