// Loads the inline <script> from templates/partials/credentials.html into
// JSDOM, mocks launchconfig.instanceList(), and asserts how the grouped
// instance list renders: provider instances grouped by type, the layered
// source display (oauth > file > env with effective/shadowed badges), the
// Set/Replace + make-default button logic, and the global "Add provider
// instance" form whose api-style field follows the selected type.

const fs = require("fs");
const path = require("path");
const { JSDOM } = require("jsdom");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

const html = fs.readFileSync(
  path.resolve(__dirname, "../templates/partials/credentials.html"),
  "utf8",
);
const scriptMatch = html.match(/<script>([\s\S]*?)<\/script>/);
assert(scriptMatch, "credentials.html should contain a <script> block");
const src = scriptMatch[1];

function makeDom() {
  return new JSDOM(
    `<!DOCTYPE html><html><body>
      <button type="button" id="btn-add-provider-instance">+ Add provider instance</button>
      <section id="instances-root" class="settings-collection" data-loaded="false">
        <ul class="settings-collection-list" role="list"><li class="settings-collection-empty">Loading…</li></ul>
      </section>
    </body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/credentials" },
  );
}

const wait = (dom, ms) => new Promise((r) => dom.window.setTimeout(r, ms));

async function waitLoaded(dom) {
  const root = dom.window.document.getElementById("instances-root");
  for (let i = 0; i < 100 && root.dataset.loaded !== "true"; i++) await wait(dom, 0);
  assert(root.dataset.loaded === "true", "instances list should finish loading");
}

(async function main() {
  // Case 1: grouping + layered source display + Set/Replace + make-default.
  {
    const dom = makeDom();
    // Representative InstanceEntry payloads, matching what the hub sends.
    const instances = [
      {
        name: "openai", type: "openai", apiStyle: "responses", isDefault: true,
        authModes: ["apiKey", "oauth"], activeSource: "oauth",
        hasStoredOAuth: true, storedEmail: "o@example.com",
        hasStoredFile: true, // OAuth signed in AND a stored API key beneath it
      },
      {
        name: "anthropic", type: "anthropic",
        authModes: ["apiKey"], activeSource: "file",
        hasStoredFile: true, envVar: "ANTHROPIC_API_KEY", // file shadows env
      },
      {
        name: "google", type: "google",
        authModes: ["apiKey"], activeSource: "absent",
      },
    ];
    dom.window.launchconfig = {
      instanceList: async () => ({ instances, availableTypes: ["openai", "anthropic", "google", "kimi"] }),
      authApiKeySet: async () => ({}),
      authLogout: async () => ({}),
      instanceCreate: async () => ({}),
      instanceEdit: async () => ({}),
      instanceRemove: async () => ({}),
      instanceSetDefault: async () => ({}),
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;

    // Instances are grouped under a per-type header.
    const openaiGroup = doc.querySelector('.credentials-type-group[data-type="openai"]');
    assert(openaiGroup, "openai type group should render");
    assert(
      /openai/i.test(openaiGroup.querySelector(".credentials-type-name").textContent),
      "type group header should show the type name",
    );
    assert(
      openaiGroup.querySelector('.credentials-instance-list li[data-instance="openai"]'),
      "the openai instance row should live inside its type group's instance list",
    );

    // openai: OAuth effective, stored file key shadowed beneath it. The default
    // instance shows the default badge and offers no make-default action.
    const openai = doc.querySelector('li[data-instance="openai"]');
    assert(openai, "openai instance row should render");
    const oaLayers = openai.querySelectorAll(".credentials-source-layer");
    assert(oaLayers.length === 2, "openai should show 2 layers (oauth + file), got " + oaLayers.length);
    assert(
      /oauth: signed in \(o@example\.com\)/.test(oaLayers[0].textContent) && /effective/.test(oaLayers[0].textContent),
      "openai oauth layer should be labeled effective with the stored email",
    );
    assert(
      /file: stored key/.test(oaLayers[1].textContent) && /shadowed/.test(oaLayers[1].textContent),
      "openai file layer should be shown as shadowed beneath oauth",
    );
    const styleInfo = openai.querySelector(".row-text span.dim");
    assert(styleInfo && /responses/.test(styleInfo.textContent), "openai row should show its api style");
    const defBadge = openai.querySelector(".row-text .status-badge");
    assert(defBadge && /default/.test(defBadge.textContent), "the default instance should show the default badge");
    const openaiSet = openai.querySelector('button[data-action="set"]');
    assert(
      openaiSet && openaiSet.textContent.trim() === "Replace key",
      "openai set button should read 'Replace key' when a file key exists, even though OAuth is effective",
    );
    assert(openai.querySelector('button[data-action="oauth"]'), "openai should offer the OAuth button");
    assert(openai.querySelector('button[data-action="clear"]'), "openai should offer Clear (an active stored layer exists)");
    assert(!openai.querySelector('button[data-action="set-default"]'), "the default instance should not offer make-default");

    // anthropic: file effective, env shadowed; non-default → offers make-default.
    const anth = doc.querySelector('li[data-instance="anthropic"]');
    const anLayers = anth.querySelectorAll(".credentials-source-layer");
    assert(anLayers.length === 2, "anthropic should show 2 layers (file + env), got " + anLayers.length);
    assert(
      /file: stored key/.test(anLayers[0].textContent) && /effective/.test(anLayers[0].textContent),
      "anthropic file layer should be effective",
    );
    assert(
      /env: ANTHROPIC_API_KEY/.test(anLayers[1].textContent) && /shadowed/.test(anLayers[1].textContent),
      "anthropic env layer should be shadowed",
    );
    assert(
      anth.querySelector('button[data-action="set"]').textContent.trim() === "Replace key",
      "anthropic set button should read 'Replace key' when a file key exists",
    );
    assert(anth.querySelector('button[data-action="set-default"]'), "a non-default instance should offer make-default");

    // google: nothing configured — single label, no layered display, Set/no Clear.
    const gem = doc.querySelector('li[data-instance="google"]');
    assert(
      gem.querySelectorAll(".credentials-source-layer").length === 0,
      "google (absent) should not render a layered display",
    );
    assert(/Not configured/.test(gem.textContent), "google should show the 'Not configured' label");
    assert(
      gem.querySelector('button[data-action="set"]').textContent.trim() === "Set key",
      "google set button should read 'Set key' when no key exists",
    );
    assert(!gem.querySelector('button[data-action="clear"]'), "google (absent) should not offer Clear");
  }

  // Case 2: the global add form's api-style field follows the selected type
  // (it applies only to type openai).
  {
    const dom = makeDom();
    dom.window.launchconfig = {
      instanceList: async () => ({ instances: [], availableTypes: ["anthropic", "openai", "kimi"] }),
    };
    dom.window.eval(src);
    await waitLoaded(dom);
    const doc = dom.window.document;

    doc.getElementById("btn-add-provider-instance").click();
    for (let i = 0; i < 100 && !doc.getElementById("global-add-form"); i++) await wait(dom, 0);
    assert(doc.getElementById("global-add-form"), "clicking Add should open the global add form");

    const typeSelect = doc.getElementById("add-type-select");
    assert(typeSelect && typeSelect.options.length === 3, "type select should list the available types");

    const apiStyleField = doc.getElementById("add-apistyle-field");
    // Default-selected type is the first option (anthropic) → api-style hidden.
    assert(apiStyleField.hidden, "api-style field is hidden for non-openai types");
    typeSelect.value = "openai";
    typeSelect.dispatchEvent(new dom.window.Event("change"));
    assert(!apiStyleField.hidden, "api-style field shows when openai is selected");
  }

  console.log("test-credentials.js: OK");
})().catch((err) => {
  console.error(err && err.stack ? err.stack : err);
  process.exit(1);
});
