// Loads the inline <script> from templates/partials/credentials.html into
// JSDOM, mocks launchconfig.authList(), and asserts how each provider row
// renders — specifically the layered source display (oauth > file > env with
// effective/shadowed badges) and the Set/Replace button label. Locks down the
// behavior added in PRI-1877: an OAuth sign-in shadowing a stored file key is
// visible, and "Replace key" shows whenever a file key exists.

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

(async function main() {
  const dom = new JSDOM(
    `<!DOCTYPE html><html><body>
      <section id="credentials-rows" class="settings-collection" data-loaded="false">
        <header class="settings-collection-head"><h3>Providers</h3><span class="settings-collection-count" data-count></span></header>
        <ul class="settings-collection-list" role="list"><li class="settings-collection-empty">Loading…</li></ul>
      </section>
    </body></html>`,
    { runScripts: "outside-only", pretendToBeVisual: true, url: "http://127.0.0.1:9180/credentials" },
  );

  // Representative AuthStatusResponse payloads, matching what the hub sends.
  const providers = [
    {
      provider: "openai",
      supported: true,
      signedIn: true,
      activeSource: "oauth",
      authModes: ["apiKey", "oauth"],
      hasStoredOAuth: true,
      storedEmail: "o@example.com",
      hasStoredFile: true, // OAuth signed in AND a stored API key beneath it
    },
    {
      provider: "anthropic",
      supported: true,
      signedIn: true,
      activeSource: "file",
      authModes: ["apiKey"],
      hasStoredFile: true,
      envVar: "ANTHROPIC_API_KEY", // file shadows env
    },
    {
      provider: "gemini",
      supported: true,
      signedIn: false,
      activeSource: "absent",
      authModes: ["apiKey"],
    },
  ];

  dom.window.launchconfig = {
    authList: async () => ({ providers }),
    authApiKeySet: async () => ({}),
    authLoginStart: async () => ({}),
    authLoginComplete: async () => ({}),
    authLogout: async () => ({}),
  };

  dom.window.eval(src);

  // The script's IIFE kicks off an async refresh(); wait until it renders.
  const section = dom.window.document.getElementById("credentials-rows");
  for (let i = 0; i < 100 && section.dataset.loaded !== "true"; i++) {
    await new Promise((r) => dom.window.setTimeout(r, 0));
  }
  assert(section.dataset.loaded === "true", "credentials rows should finish loading");

  const doc = dom.window.document;

  // openai: OAuth effective, stored file key shadowed beneath it. This is the
  // case that was previously invisible (only file+env was ever layered).
  const openai = doc.querySelector('li[data-provider="openai"]');
  assert(openai, "openai row should render");
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
  const openaiSet = openai.querySelector('button[data-action="set"]');
  assert(
    openaiSet && openaiSet.textContent.trim() === "Replace key",
    "openai set button should read 'Replace key' when a file key exists, even though OAuth is effective",
  );
  assert(openai.querySelector('button[data-action="oauth"]'), "openai should offer the OAuth button");
  assert(openai.querySelector('button[data-action="clear"]'), "openai should offer Clear (an active stored layer exists)");

  // anthropic: file effective, env shadowed — the pre-existing case still works
  // through the generalized renderer.
  const anth = doc.querySelector('li[data-provider="anthropic"]');
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

  // gemini: nothing configured — single label, no layered display, Set/no Clear.
  const gem = doc.querySelector('li[data-provider="gemini"]');
  assert(
    gem.querySelectorAll(".credentials-source-layer").length === 0,
    "gemini (absent) should not render a layered display",
  );
  assert(/Not configured/.test(gem.textContent), "gemini should show the 'Not configured' label");
  assert(
    gem.querySelector('button[data-action="set"]').textContent.trim() === "Set API key",
    "gemini set button should read 'Set API key' when no key exists",
  );
  assert(!gem.querySelector('button[data-action="clear"]'), "gemini (absent) should not offer Clear");

  console.log("test-credentials.js: OK");
})().catch((err) => {
  console.error(err && err.stack ? err.stack : err);
  process.exit(1);
});
