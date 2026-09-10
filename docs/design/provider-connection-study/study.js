/* Standalone interaction mockups. No provider requests, storage, or real authentication. */
const concepts = {
  a: { name: "Quick connect", thesis: "A small picker, then one explained input.", home: "picker" },
  b: { name: "Inline cards", thesis: "Connect in place, directly from provider settings.", home: "inline" },
  c: { name: "Guided setup", thesis: "A focused first-run journey with a clear finish.", home: "picker" },
  d: { name: "Model first", thesis: "Start with the model family you want to use.", home: "models" },
  e: { name: "Access first", thesis: "Start with the access you already have.", home: "methods" },
  f: { name: "Provider directory", thesis: "Keep discovery and connection details side by side.", home: "directory" },
};
const providers = {
  anthropic: { name: "Anthropic", sub: "Claude · API key", mark: "A", family: "Claude", model: "claude-opus-5", env: "ANTHROPIC_API_KEY", url: "https://api.anthropic.com/v1", keyUrl: "https://platform.claude.com/settings/keys", keyPlace: "Claude Console", billing: "Claude API usage has separate billing from Claude chat subscriptions." },
  openai: { name: "OpenAI", sub: "ChatGPT sign-in or API key", mark: "◎", family: "GPT", model: "Provider default", env: "OPENAI_API_KEY", url: "https://api.openai.com/v1", keyUrl: "https://platform.openai.com/api-keys", keyPlace: "OpenAI Platform", billing: "API usage is billed separately from your ChatGPT subscription." },
  google: { name: "Google Gemini", sub: "Gemini · API key", mark: "G", family: "Gemini", model: "Provider default", env: "GEMINI_API_KEY", url: "https://generativelanguage.googleapis.com/v1beta", keyUrl: "https://aistudio.google.com/apikey", keyPlace: "Google AI Studio", billing: "Your key belongs to a Google Cloud project. Quotas and billing apply to that project." },
  openrouter: { name: "OpenRouter", sub: "Many model families · API key", mark: "↗", family: "Available models", model: "Choose after connecting", env: "OPENROUTER_API_KEY", url: "https://openrouter.ai/api/v1", keyUrl: "https://openrouter.ai/settings/keys", keyPlace: "OpenRouter", billing: "Model usage is charged to your OpenRouter account." },
  deepseek: { name: "DeepSeek", sub: "API key", mark: "D", env: "DEEPSEEK_API_KEY", url: "https://api.deepseek.com", keyUrl: "https://platform.deepseek.com/api_keys", keyPlace: "DeepSeek Platform", billing: "API usage is billed by DeepSeek." },
  groq: { name: "Groq", sub: "API key", mark: "g", env: "GROQ_API_KEY", url: "https://api.groq.com/openai/v1", keyUrl: "https://console.groq.com/keys", keyPlace: "Groq Console", billing: "Your provider's quota and billing limits apply." },
  mistral: { name: "Mistral", sub: "API key", mark: "M", env: "MISTRAL_API_KEY", url: "https://api.mistral.ai/v1", keyUrl: "https://console.mistral.ai/", keyPlace: "Mistral Console", billing: "Your provider's quota and billing limits apply." },
  ollama: { name: "Ollama", sub: "Local models · no key", mark: ">_", mode: "local", url: "http://localhost:11434/v1", model: "Choose an installed model" },
  azure: { name: "Azure", sub: "Cloud resource + API key", mark: "Az", mode: "azure", env: "AZURE_API_KEY", keyUrl: "https://portal.azure.com/", keyPlace: "Azure portal", billing: "Usage is billed to your Azure subscription." },
  vertex: { name: "Google Vertex AI", sub: "Project, location + Google credentials", mark: "V", mode: "cloud", model: "Provider default" },
  custom: { name: "Custom endpoint", sub: "Company gateway or compatible API", mark: "⌘", mode: "custom", model: "Choose an available model" },
};
const popular = ["anthropic", "openai", "google", "openrouter"];
const params = new URLSearchParams(location.search);
const direction = concepts[params.get("concept")] ? params.get("concept") : "a";
const concept = concepts[direction];
const state = { screen: params.get("screen") || concept.home, provider: providers[params.get("provider")] ? params.get("provider") : null, method: "signin", outcome: "success", saved: false, family: null };
if (["credential", "success", "auth", "endpoint", "unsupported", "saved", "checking", "oauth"].includes(state.screen) && !state.provider) state.provider = "anthropic";
const app = document.getElementById("app");
const safe = (text) => String(text ?? "").replace(/[&<>"']/g, c => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;", '"': "&quot;", "'": "&#39;" })[c]);
const button = (text, action, className = "btn", extras = "") => `<button type="button" class="${className}" data-action="${action}" ${extras}>${text}</button>`;
const mark = id => `<span class="mark ${id}" aria-hidden="true">${providers[id].mark}</span>`;
function providerButton(id, selected = false) {
  const p = providers[id];
  return `<button type="button" class="provider ${selected ? "selected" : ""}" data-provider="${id}">${mark(id)}<span class="grow"><strong>${p.name}</strong><small>${p.sub}</small></span><span class="arrow" aria-hidden="true">→</span></button>`;
}
function header(title, subtitle, eyebrow = "") {
  return `<header class="panel-head">${eyebrow ? `<div class="eyebrow">${eyebrow}</div>` : ""}<h2 tabindex="-1" data-heading>${title}</h2>${subtitle ? `<p>${subtitle}</p>` : ""}</header>`;
}
function browseLinks() {
  return `<div class="support-links">${button("Browse all providers →", "all", "btn link")}${button("Local or company endpoint", "local", "btn quiet")}</div>`;
}
function picker() {
  return `${header("Connect a provider", "Choose where your models come from. We’ll handle the defaults.")}<div class="section-label">Popular providers</div><div class="provider-grid">${popular.map(id => providerButton(id)).join("")}</div>${browseLinks()}<p class="footer-note">Already configured a key on this host? ${button("Check existing access", "detected", "btn link")}</p>`;
}
function allProviders(localOnly = false) {
  const ids = localOnly ? ["ollama", "custom", "azure", "vertex"] : Object.keys(providers);
  return `${button("← Back", "home", "back")}${header(localOnly ? "Local & company providers" : "Find your provider", localOnly ? "Use a local model server or the details from your team." : "Search by provider or model family.")}<label for="provider-search" class="input-label">Search providers</label><input id="provider-search" class="search" type="search" placeholder="e.g. DeepSeek, Azure, Ollama" autocomplete="off"><div class="provider-list" id="search-results">${ids.map(id => `<div data-search="${safe(`${providers[id].name} ${providers[id].sub}`.toLowerCase())}">${providerButton(id)}</div>`).join("")}</div><p class="no-results" id="no-results" hidden>No matching provider. Try a different name or use a custom endpoint.</p>${button("Use a custom endpoint", "custom", "btn link")}`;
}
function field(id, label, placeholder = "", value = "", type = "text", help = "", required = true) {
  return `<div class="field-block"><label class="input-label" for="${id}">${label}</label><input id="${id}" name="${id}" type="${type}" placeholder="${safe(placeholder)}" value="${safe(value)}" ${required ? "required" : ""} autocomplete="off" spellcheck="false">${help ? `<p class="help">${help}</p>` : ""}</div>`;
}
function advanced(p) {
  return `<details id="advanced"><summary>Advanced settings <span class="muted">· optional</span></summary><div class="advanced-fields"><p class="help">The defaults work for a standard connection. Change these only for a separate account, proxy, or team setup.</p><label><span class="input-label">Connection name</span><input name="connection-name" value="${safe(p.name)}" autocomplete="off"></label><label><span class="input-label">API base URL</span><input type="url" name="advanced-url" value="${safe(p.url || "")}" placeholder="Use provider default"></label><label><span class="input-label">API key environment variable</span><input name="key-env" placeholder="${safe(p.env || "Use provider default")}"></label><label><span class="input-label">API format</span><select name="protocol"><option>Use provider default</option>OpenAI Chat Completions</option>OpenAI Responses</option>Anthropic Messages</option>Google Generative AI</option></select></label><label><span class="input-label">Credential header</span><input name="credential-header" placeholder="Authorization=Bearer $TEAM_KEY"></label><p class="help">Reference an environment variable here. Keep secret values in the credential input.</p></div></details>`;
}
function keyInput(p) {
  return `<div class="callout"><strong>Get an API key</strong><ol class="steps"><li>Open <a href="${p.keyUrl}" target="_blank" rel="noopener noreferrer">${p.keyPlace} ↗</a> and create a key.</li><li>Copy it and paste it below.</li></ol><p class="help">${p.billing}</p></div><div class="field-block"><label class="input-label" for="api-key">API key</label><div class="input-wrap"><input id="api-key" name="api-key" type="password" autocomplete="off" spellcheck="false" placeholder="Paste your API key" required>${button("Show", "show-key", "btn", 'aria-label="Show API key" aria-pressed="false"')}</div><p class="help">This is the only required field for a standard connection.</p></div>`;
}
function credential() {
  const p = providers[state.provider];
  const mode = p.mode || "key";
  let body;
  let cta = "Save & check connection";
  let note = "Saves your key on the Evener host, then checks access by requesting the model list. No prompt or model generation is sent.";
  const identity = `<div class="credential-title">${mark(state.provider)}<div><h2 tabindex="-1" data-heading>Connect ${p.name}</h2><p class="help">${p.sub}</p></div></div>`;
  let methodChoice = "";
  if (state.provider === "openai") {
    methodChoice = `<div class="segmented" aria-label="OpenAI access method">${button("ChatGPT sign-in", "signin-method", "", `aria-pressed="${state.method === "signin"}"`)}${button("API key", "key-method", "", `aria-pressed="${state.method === "key"}"`)}</div>`;
  }
  if (state.provider === "openai" && state.method === "signin") {
    body = `<div class="callout"><strong>Use your ChatGPT account</strong><p>Sign in through OpenAI to use the Codex connection. Availability and usage limits depend on your plan.</p><p class="help">No API key is needed. You’ll approve access in a browser, then return here.</p></div>`;
    cta = "Continue with ChatGPT";
    note = "After sign-in, Evener checks whether it can list models. It does not generate a response.";
  } else if (mode === "local") {
    body = `<div class="callout"><strong>Start Ollama first</strong><p>Make sure the server is running and has a model installed. No API key is needed for a default local server.</p></div>${field("endpoint", "Ollama API URL", "", p.url, "url", "localhost refers to the machine running Evener, which may differ from this browser.")}`;
    cta = "Check local connection";
    note = "The check contacts this server from the Evener host.";
  } else if (mode === "custom") {
    body = `${field("endpoint", "API base URL", "https://gateway.example.com/v1", "", "url", "Use the URL your team or provider gave you.")}<div class="field-block"><label class="input-label" for="custom-format">API format</label><select id="custom-format" required><option value="">Choose the format your endpoint supports</option><option>OpenAI Chat Completions</option><option>OpenAI Responses</option><option>Anthropic Messages</option><option>Google Generative AI</option></select></div><div class="field-block"><label class="input-label" for="custom-auth">Authentication</label><select id="custom-auth"><option value="key">API key</option><option value="none">No authentication</option></select></div>${field("api-key", "API key", "Paste the key from your team", "", "password", "Required when this endpoint uses API-key authentication.")}`;
  } else if (mode === "azure") {
    body = `${field("resource", "Azure resource name", "e.g. my-team-resource", "", "text", "Find this in your Azure resource overview.")}${keyInput(p).replace("This is the only required field for a standard connection.", "Use the API key for this Azure resource.")}`;
  } else if (mode === "cloud") {
    body = `<div class="callout"><strong>Use your Google Cloud project</strong><p>Your project and location determine the endpoint. Use Google credentials authorized for Vertex AI.</p></div>${field("project", "Google Cloud project ID", "my-project-id")}${field("region", "Location", "e.g. us-central1")}<div class="field-block"><label class="input-label" for="cloud-auth">Google credentials</label><select id="cloud-auth"><option value="host">Use credentials configured on the Evener host</option><option value="json">Paste credential JSON</option></select><p class="help">Use host credentials if your administrator has configured them.</p></div><div id="json-field" class="field-block" hidden><label class="input-label" for="credential-json">Credential JSON</label><textarea id="credential-json" spellcheck="false" placeholder="Service-account or application-default credential JSON"></textarea></div>`;
    cta = "Save & check connection";
    note = "The check requests the model list from the Evener host. Some Vertex connections cannot be checked this way.";
  } else {
    body = keyInput(p);
  }
  const selectedFamily = state.family ? `<p class="route-note">For <strong>${safe(state.family)}</strong> · via ${p.name}. You can choose a different provider.</p>` : "";
  return `${button("← Change provider", "home", "back")}${identity}${selectedFamily}${methodChoice}<form id="connect-form">${body}${advanced(p)}<p class="help" style="margin-top:20px">${note}</p><div class="actions"><button class="btn primary" type="submit">${cta} →</button>${button("Cancel", "home", "btn quiet")}</div></form>`;
}
function result() {
  const p = providers[state.provider];
  const s = state.screen;
  if (s === "oauth") return `${button("← Back", "credential", "back")}${header("Approve access in OpenAI", "Complete the browser sign-in, then return to Evener.")}<div class="callout"><strong>Device-code flow</strong><p>Enter this code on the verification page shown by OpenAI.</p></div><div class="code">DEMO-CODE</div>${button("Open sign-in page ↗", "demo-oauth", "btn primary full")}<p class="help">Mockup: this button opens the next simulated step, not a real sign-in page.</p><div class="actions">${button("I’ve approved access", "check", "btn")}${button("Cancel sign-in", "credential", "btn quiet")}</div><details><summary>Using a browser redirect instead?</summary><p class="help">Show the provider’s authorization URL and a field for its completed redirect URL. Keep these details in the sign-in step, never the provider form.</p></details>`;
  if (s === "checking") return `${header("Checking connection…", `Contacting ${p.name} from the Evener host.`)}<div class="callout" role="status">Your access details have been saved. Waiting for the provider response.</div><div class="actions">${button("Show simulated result", "check", "btn primary")}${button("Stop checking", "saved", "btn quiet")}</div>`;
  if (s === "success") return `<div class="status-symbol good" aria-hidden="true">✓</div>${header(`${p.name} is connected`, "Evener could access the model list. Choose a model to continue.")}<div class="result-card">${mark(state.provider)}<div class="grow"><strong>${p.name}</strong><p class="help">Connection checked · just now</p><label class="input-label" for="next-model" style="margin-top:14px">Model</label><select id="next-model"><option>${safe(p.model || "Provider default")}</option></select></div></div><p class="help">The list is refreshed from the catalog. This check does not verify model generation or access to every model.</p><div class="actions">${button(direction === "b" || direction === "f" ? "Done" : "Use this model", "done", "btn primary")}${button("Connect another provider", "home", "btn quiet")}</div>`;
  if (s === "saved") return `<div class="status-symbol" aria-hidden="true">○</div>${header("Saved, not checked", "Your access details are saved. Evener has not confirmed that this provider works.")}<div class="actions">${button("Check connection", "check", "btn primary")}${button("Back to provider setup", "credential", "btn quiet")}</div>`;
  if (s === "unsupported") return `${header("Saved · check unavailable", "This provider does not support the connection check.")}<div class="callout" role="status"><strong>Your setup is saved, but unverified.</strong><p>You can try it in a session. Evener will report any access or model errors there.</p></div><div class="actions">${button("Continue without verification", "done", "btn primary")}${button("Review settings", "credential", "btn quiet")}</div>`;
  const auth = s === "auth";
  return `${header(auth ? "The provider rejected access" : "Couldn’t reach the provider", auth ? "Your details are saved, but the connection is not working yet." : "Your details are saved. A network or endpoint problem interrupted the check.")}<div class="callout error" role="alert"><h3>${auth ? "Check your access" : "Check the connection"}</h3><p>${auth ? "Confirm that the key or sign-in has access to this model and that the provider account can make API requests." : "Check connectivity from the Evener host. If you use a proxy or gateway, review its URL."}</p></div><div class="actions">${button(auth ? "Edit access details" : "Retry check", auth ? "credential" : "check", "btn primary")}${button(auth ? "Retry check" : "Review settings", auth ? "check" : "credential", "btn")}</div><details><summary>Technical details</summary><p class="help">Illustrative ${auth ? "auth_rejected" : "endpoint_failure"} result. Show sanitized diagnostics here, with no keys or raw response bodies.</p></details>`;
}
function models() {
  return `${header("Which models do you want to use?", "Connect a provider to make its models available.")}<div class="model-card">${mark("anthropic")}<div class="grow"><strong>Claude</strong><p class="model-caption">Direct from Anthropic</p></div>${button("Connect Anthropic", "model-anthropic", "btn")}</div><div class="model-card">${mark("openai")}<div class="grow"><strong>GPT & Codex</strong><p class="model-caption">Direct from OpenAI</p></div>${button("Connect OpenAI", "model-openai", "btn")}</div><div class="model-card">${mark("google")}<div class="grow"><strong>Gemini</strong><p class="model-caption">Direct from Google</p></div>${button("Connect Google", "model-google", "btn")}</div><p class="help" style="margin-top:18px">Already use OpenRouter or a cloud provider? Choose that provider to use its billing and access.</p>${browseLinks()}`;
}
function methods() {
  return `${header("How would you like to connect?", "Use the access you already have.")}<div class="method-list"><button class="provider" data-action="chatgpt">${mark("openai")}<span class="grow"><strong>Sign in with ChatGPT</strong><small>Use an eligible ChatGPT plan · no API key</small></span><span aria-hidden="true">→</span></button><button class="provider" data-action="picker"><span class="mark" aria-hidden="true">⌁</span><span class="grow"><strong>Use a provider API key</strong><small>Anthropic, OpenAI, Google, OpenRouter, and more</small></span><span aria-hidden="true">→</span></button><button class="provider" data-action="local">${mark("custom")}<span class="grow"><strong>Use local or company access</strong><small>Ollama, cloud credentials, or a gateway</small></span><span aria-hidden="true">→</span></button></div><p class="footer-note">Not sure? ${button("Choose by provider instead →", "picker", "btn link")}</p>`;
}
function detected() {
  return `${button("← Back", "home", "back")}${header("Check existing access", "Credentials are configured on the Evener host. Check them before using a model.")}<div class="callout"><div class="row">${mark("anthropic")}<div><strong>Anthropic</strong><p class="help">Environment variable · key value hidden</p></div></div><p class="help">This is an illustrative detected credential, not a scan of your machine.</p></div><p class="help" style="margin-top:18px">The check requests the model list. It does not send a prompt or generate a response.</p><div class="actions">${button("Check Anthropic connection", "check-detected", "btn primary")}${button("Use different access", "picker", "btn quiet")}</div>`;
}
function finished() {
  return `${header("Back to your workspace", "The connection flow is complete.")}<div class="callout"><strong>${direction === "b" || direction === "f" ? "Provider settings" : "Model selection"}</strong><p>${direction === "b" || direction === "f" ? "The saved connection appears in your list with its verification state." : "You can now continue choosing a model and creating your session."}</p></div><p class="help">End of prototype. No settings were saved and no real provider was contacted.</p><div class="actions">${button("Try another provider", "home", "btn primary")}</div>`;
}
function body() {
  if (["credential"].includes(state.screen)) return credential();
  if (["success", "auth", "endpoint", "unsupported", "saved", "checking", "oauth"].includes(state.screen)) return result();
  if (state.screen === "all" || state.screen === "local") return allProviders(state.screen === "local");
  if (state.screen === "models") return models();
  if (state.screen === "methods") return methods();
  if (state.screen === "detected") return detected();
  if (state.screen === "done") return finished();
  return picker();
}
function inline() {
  const showing = !["inline", "picker"].includes(state.screen);
  if (["all", "local", "detected", "done"].includes(state.screen)) return `<div class="panel">${body()}</div>`;
  return `<section class="inline-page"><header><div class="eyebrow">Settings / Providers</div><h1 style="margin-top:14px">Your model providers</h1><p>Connect one provider to get started. Add more whenever you need them.</p></header><div class="section-label">Popular providers</div>${popular.map(id => `<div class="inline-row">${providerButton(id, state.provider === id && showing)}${state.provider === id && showing ? `<div class="inline-form">${body()}</div>` : ""}</div>`).join("")}${state.provider && !popular.includes(state.provider) && showing ? `<div class="inline-row"><div class="inline-form">${body()}</div></div>` : ""}${browseLinks()}</section>`;
}
function guided() {
  const step = ["success", "done"].includes(state.screen) ? 3 : state.provider ? 2 : 1;
  return `<section class="guided"><aside class="guided-rail"><div class="brand"><span class="brand-mark">≋</span>evener</div><h2>Bring your<br>favorite models.</h2><div class="guided-steps">${["Provider", "Access", "Ready"].map((name, i) => `<div class="guided-step ${step === i + 1 ? "active" : ""}"><b>${step > i + 1 ? "✓" : i + 1}</b>${name}</div>`).join("")}</div><p class="guided-note">You only need one provider.<br>Add others later in Settings.</p></aside><div class="guided-content">${body()}</div></section>`;
}
function directory() {
  return `<section class="directory ${state.provider ? "has-selection" : ""}"><aside class="directory-list"><h3>Providers</h3><label for="provider-search" class="section-label">Find a provider</label><input id="provider-search" class="search" type="search" placeholder="Search providers"><div class="section-label">Popular providers</div><div class="provider-list" id="search-results">${Object.keys(providers).map(id => `<div data-search="${safe(`${providers[id].name} ${providers[id].sub}`.toLowerCase())}">${providerButton(id, state.provider === id)}</div>`).join("")}</div><p id="no-results" class="no-results" hidden>No matching provider. Try a different name.</p></aside><div class="directory-content">${state.screen === "directory" ? `<div class="empty-detail"><span class="mark" aria-hidden="true">↗</span><h2 tabindex="-1" data-heading>Connect your first provider</h2><p class="muted">Choose a provider on the left.<br>We’ll ask only for what it needs.</p></div>` : body()}</div></section>`;
}
function render(focus = true) {
  const toolbar = `<header class="study-bar"><div class="row"><a href="index.html">← Six options</a><span class="study-name">${direction.toUpperCase()} · ${concept.name}</span></div><div class="study-controls">${button("Reset", "home", "btn quiet")}<label for="demo-outcome">Check result</label><select id="demo-outcome">${[["success", "Success"], ["auth", "Access rejected"], ["endpoint", "Endpoint unavailable"], ["unsupported", "Check unsupported"]].map(([value, text]) => `<option value="${value}" ${state.outcome === value ? "selected" : ""}>${text}</option>`).join("")}</select>${button("Fill demo details", "fill-demo", "btn")}</div></header><div class="study-note"><strong>DESIGN MOCKUP</strong> · ${concept.thesis} Nothing is saved or sent. Use demo details, never real credentials. Provider catalog is a representative sample.</div>`;
  let canvas;
  if (direction === "b") canvas = inline();
  else if (direction === "c") canvas = guided();
  else if (direction === "f") canvas = directory();
  else if (direction === "d") canvas = `<div class="model-stage"><div class="row spread"><h1>New session</h1><span class="tag">Workspace: evener</span></div><div class="composer">What would you like to work on?<div class="row spread" style="margin-top:26px"><span class="tag">${state.family || "Choose a model"} ▾</span><span aria-hidden="true">↑</span></div></div><div class="panel model-picker">${body()}</div></div>`;
  else canvas = `<section class="panel">${body()}</section>`;
  app.innerHTML = `${toolbar}<div class="app-shell"><aside class="rail"><div class="brand"><span class="brand-mark">≋</span>evener</div><nav aria-label="Workspace context"><div class="rail-item">+ New session</div><div class="rail-item">Sessions</div><div class="rail-item">Projects</div><div class="rail-item active">${direction === "b" || direction === "f" ? "Settings" : "Get started"}</div></nav><div class="rail-bottom">Local host<br><span class="small">Design study · September 2026</span></div></aside><main class="workspace"><div class="workspace-top"><span>${direction === "b" || direction === "f" ? "Settings / Providers" : "Workspace / Connect a provider"}</span><span>Preview ${direction.toUpperCase()}</span></div><div class="stage">${canvas}</div></main></div>`;
  bind();
  if (focus) app.querySelector("[data-heading]")?.focus({ preventScroll: true });
}
function choose(id) { state.provider = id; state.screen = "credential"; state.saved = false; state.method = "signin"; render(); }
function go(screen) { state.screen = screen; render(); }
function bind() {
  app.querySelectorAll("[data-provider]").forEach(el => el.addEventListener("click", () => choose(el.dataset.provider)));
  app.querySelectorAll("[data-action]").forEach(el => el.addEventListener("click", () => action(el.dataset.action, el)));
  document.getElementById("demo-outcome")?.addEventListener("change", e => { state.outcome = e.target.value; });
  document.getElementById("provider-search")?.addEventListener("input", e => {
    const term = e.target.value.toLowerCase().trim();
    const rows = [...document.querySelectorAll("[data-search]")];
    rows.forEach(el => { el.hidden = !el.dataset.search.includes(term); });
    document.getElementById("no-results").hidden = rows.some(el => !el.hidden);
  });
  document.getElementById("connect-form")?.addEventListener("submit", e => {
    e.preventDefault();
    if (!e.target.reportValidity()) return;
    state.saved = true;
    go(state.provider === "openai" && state.method === "signin" ? "oauth" : state.outcome);
  });
  document.getElementById("cloud-auth")?.addEventListener("change", e => {
    const json = e.target.value === "json";
    document.getElementById("json-field").hidden = !json;
    document.getElementById("credential-json").required = json;
  });
  document.getElementById("custom-auth")?.addEventListener("change", e => {
    const key = document.getElementById("api-key");
    key.required = e.target.value === "key";
    key.closest(".field-block").hidden = !key.required;
    if (!key.required) key.value = "";
  });
}
function action(name, element) {
  if (name === "home") { state.provider = null; state.family = null; state.saved = false; go(concept.home); }
  else if (name === "custom") choose("custom");
  else if (name === "chatgpt") choose("openai");
  else if (name === "signin-method" || name === "key-method") { state.method = name === "key-method" ? "key" : "signin"; render(); }
  else if (name.startsWith("model-")) { const id = name.slice(6); state.family = providers[id].family; choose(id); }
  else if (name === "check-detected") { state.provider = "anthropic"; state.saved = true; go(state.outcome); }
  else if (name === "check") go(state.outcome);
  else if (name === "demo-oauth") go("checking");
  else if (name === "show-key") {
    const input = document.getElementById("api-key");
    const show = input.type === "password";
    input.type = show ? "text" : "password";
    element.textContent = show ? "Hide" : "Show";
    element.setAttribute("aria-label", show ? "Hide API key" : "Show API key");
    element.setAttribute("aria-pressed", String(show));
  } else if (name === "fill-demo") {
    const demo = { "api-key": "demo-key-not-a-secret", endpoint: "https://gateway.example.com/v1", project: "demo-project", region: "us-central1", resource: "demo-resource", "credential-json": '{"demo":"not real credentials"}' };
    for (const [id, value] of Object.entries(demo)) { const input = document.getElementById(id); if (input && (!input.value || id === "api-key")) input.value = value; }
    const format = document.getElementById("custom-format");
    if (format) format.selectedIndex = 1;
  } else go(name);
}
render(false);
