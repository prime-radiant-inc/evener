/* Standalone interaction mockups. No provider requests, storage, or real authentication. */
const concepts = {
  a: { name: "Quick connect", thesis: "A small picker, then one explained input.", home: "picker" },
  b: { name: "Inline cards", thesis: "Expand one available provider to make your first connection.", home: "inline" },
  c: { name: "Guided setup", thesis: "A focused first-run journey with a clear finish.", home: "picker" },
  d: { name: "Model first", thesis: "Start with the model family you want to use.", home: "models" },
  e: { name: "Access first", thesis: "Start with the access you already have.", home: "methods" },
  f: { name: "Provider directory", thesis: "Keep discovery and connection details side by side.", home: "directory" },
};
const providers = {
  anthropic: { name: "Anthropic", sub: "Claude · API key", mark: "A", family: "Claude", model: "claude-opus-5", env: "ANTHROPIC_API_KEY", url: "https://api.anthropic.com/v1", keyUrl: "https://platform.claude.com/settings/keys", keyPlace: "Claude Console", billing: "A Claude chat subscription does not include API access. Create a Claude Console API key; API usage is billed separately." },
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
const state = { screen: params.get("screen") || concept.home, provider: providers[params.get("provider")] ? params.get("provider") : null, method: "signin", outcome: "success", saved: false, family: null, entryMethod: null, source: "key", destination: "", credentialSource: "", lastOutcome: null, selectedModel: null };
const drafts = new Map();
let directoryQuery = "";
let returnProvider = null;
const resultScreens = ["success", "auth", "endpoint", "unsupported", "saved", "checking", "oauth", "oauth-expired", "missing", "configuration", "save-failure", "partial-save", "review-destination"];
function connection() {
  const p = providers[state.provider];
  return state.provider === "openai" && state.method === "signin" ? { ...p, id: "openai-codex", name: "OpenAI Codex", sub: "ChatGPT sign-in · OAuth", env: "", url: "https://chatgpt.com/backend-api/codex", codex: true } : { ...p, id: state.provider };
}
if (["credential", "editor", ...resultScreens].includes(state.screen) && !state.provider) state.provider = "anthropic";
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
  return `${header("Connect a provider", "Connect one provider to get started. Add others later. We’ll handle the defaults.")}<div class="section-label">Popular providers</div><div class="provider-grid">${popular.map(id => providerButton(id)).join("")}</div>${browseLinks()}<details><summary>Already configured access on this host?</summary>${button("Check existing access", "detected", "btn link")}</details>`;
}
function allProviders(localOnly = false) {
  const ids = localOnly ? ["ollama", "custom", "azure", "vertex"] : Object.keys(providers);
  return `${button("← Back", "home", "back")}${header(localOnly ? "Local & company providers" : "Find your provider", localOnly ? "Use a local model server or the details from your team." : "Search by provider or model family.")}<label for="provider-search" class="input-label">Search providers</label><input id="provider-search" class="search" type="search" placeholder="e.g. DeepSeek, Azure, Ollama" autocomplete="off"><div class="provider-list" id="search-results">${ids.map(id => `<div data-search="${safe(`${providers[id].name} ${providers[id].sub}`.toLowerCase())}">${providerButton(id)}</div>`).join("")}</div><p class="no-results" id="no-results" hidden>No matching provider. Try a different name or use a custom endpoint.</p>${button("Use a custom endpoint", "custom", "btn link")}`;
}
function field(id, label, placeholder = "", value = "", type = "text", help = "", required = true) {
  return `<div class="field-block"><label class="input-label" for="${id}">${label}</label><input id="${id}" name="${id}" type="${type}" placeholder="${safe(placeholder)}" value="${safe(value)}" ${required ? "required" : ""} autocomplete="off" spellcheck="false">${help ? `<p class="help">${help}</p>` : ""}</div>`;
}
function advanced(p) {
  if (p.codex) return `<details id="advanced"><summary>Advanced settings <span class="muted">· optional</span></summary><p class="help">Connection: openai-codex · OAuth. OpenAI API keys are a separate connection.</p>${button("Open full connection editor", "editor", "btn link")}</details>`;
  return `<details id="advanced"><summary>Advanced settings <span class="muted">· optional</span></summary><div class="advanced-fields"><p class="help">The defaults work for your first connection. Change these only for a separate account, proxy, or team setup.</p>${!p.mode && state.source === "key" ? button("Use existing host access instead", "source-host", "btn link") : ""}<label><span class="input-label">Connection name</span><input name="connection-name" value="${safe(p.name)}" autocomplete="off"></label>${!["custom", "local"].includes(p.mode) ? `<label><span class="input-label">API base URL</span><input type="url" name="advanced-url" value="${safe(p.url || "")}" placeholder="Use provider default"></label>` : ""}<label><span class="input-label">API key environment variable</span><input name="key-env" placeholder="${safe(p.env || "Use provider default")}"></label><label><span class="input-label">API format</span><select name="protocol"><option>Use provider default</option>OpenAI Chat Completions</option>OpenAI Responses</option>Anthropic Messages</option>Google Generative AI</option></select></label><label><span class="input-label">Credential header</span><input name="credential-header" placeholder="Authorization=Bearer $TEAM_KEY"></label><p class="help">Reference an environment variable here. Keep secret values in the credential input.</p>${p.mode === "local" ? field("api-key", "Remote server API key · optional", "Only if your server requires a key", "", "password", "Default local Ollama needs no key.", false) : ""}${button("Open full connection editor", "editor", "btn link")}</div></details>`;
}
function keyInput(p) {
  return `<div class="callout"><strong>Get an API key</strong><p>A secret code that lets Evener use your provider account.</p><ol class="steps"><li>Open <a href="${p.keyUrl}" target="_blank" rel="noopener noreferrer">${p.keyPlace} ↗</a> and create a key.</li><li>Copy it and paste it below.</li></ol><p class="help">${p.billing}</p></div><div class="field-block"><label class="input-label" for="api-key">API key</label><div class="input-wrap"><input id="api-key" name="api-key" type="password" autocomplete="off" spellcheck="false" placeholder="Paste your API key" required>${button("Show", "show-key", "btn", 'aria-label="Show API key" aria-pressed="false"')}</div><p class="help">This is the only required field for a standard connection.</p>${state.saved ? `<p class="help">A key is saved. Your masked draft is retained for this setup; replace it only to change access.</p>` : ""}</div>`;
}
function credential() {
  const p = connection();
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
    body = `<div class="callout"><strong>Use your ChatGPT account</strong><p>Sign in through OpenAI to use the Codex connection. Availability and usage limits depend on your plan. <a href="https://developers.openai.com/codex/auth" target="_blank" rel="noopener noreferrer">Check current requirements ↗</a>.</p><p class="help">No API key is needed. You’ll approve access in a browser, then return here.</p></div>`;
    cta = "Continue with ChatGPT";
    note = "Creates the openai-codex connection, separate from OpenAI API-key access. After sign-in, Evener checks its model list without generating.";
  } else if (mode === "local") {
    body = `<div class="callout"><strong>Start Ollama first</strong><p>Make sure the server is running and has a model installed. No API key is needed for a default local server.</p></div>${field("endpoint", "Ollama API URL", "", p.url, "url", "localhost refers to the machine running Evener, which may differ from this browser.")}`;
    cta = "Check local connection";
    note = "The check contacts this server from the Evener host.";
  } else if (mode === "custom") {
    body = `${field("endpoint", "API base URL", "https://gateway.example.com/v1", "", "url", "Use the URL your team or provider gave you.")}<div class="field-block"><label class="input-label" for="custom-format">API format</label><select id="custom-format" required><option value="">Choose the format your endpoint supports</option><option>OpenAI Chat Completions</option><option>OpenAI Responses</option><option>Anthropic Messages</option><option>Google Generative AI</option></select></div><div class="field-block"><label class="input-label" for="custom-auth">Authentication</label><select id="custom-auth"><option value="key">API key</option><option value="none">No new credential</option></select></div>${field("api-key", "API key", "Paste the key from your team", "", "password", "Required when this endpoint uses API-key authentication.")}`;
  } else if (mode === "azure") {
    body = `${field("resource", "Azure resource name", "e.g. my-team-resource", "", "text", "Find this in your Azure resource overview.")}${keyInput(p).replace("This is the only required field for a standard connection.", "Use the API key for this Azure resource.")}`;
  } else if (mode === "cloud") {
    body = `<div class="callout"><strong>Use your Google Cloud project</strong><p>Your project and location determine the endpoint. Use Google credentials authorized for Vertex AI.</p></div>${field("project", "Google Cloud project ID", "my-project-id")}${field("region", "Location", "e.g. us-central1")}<div class="field-block"><label class="input-label" for="cloud-auth">Google credentials</label><select id="cloud-auth"><option value="host">Use credentials configured on the Evener host</option><option value="json">Paste credential JSON</option></select><p class="help">Uses a supported ADC file on the Evener host: GOOGLE_APPLICATION_CREDENTIALS or the gcloud ADC file. Metadata-server identity alone is not supported.</p></div><div id="json-field" class="field-block" hidden><label class="input-label" for="credential-json">Credential JSON</label><div class="input-wrap"><input id="credential-json" type="password" autocomplete="off" spellcheck="false" placeholder="Paste service_account or authorized_user JSON">${button("Show", "show-json", "btn", 'aria-label="Show credential JSON" aria-pressed="false"')}</div><p class="help">Accepts service_account or authorized_user JSON only. Keep this secret.</p></div>`;
    cta = "Save & check connection";
    note = "The check requests the model list from the Evener host. Some Vertex connections cannot be checked this way.";
  } else if (state.source === "host") {
    body = `<div class="callout"><strong>Use access configured on the Evener host</strong><p>No pasted key is required. Review the active environment or header credential before checking.</p></div>${button("Paste a new API key instead", "source-key", "btn link")}`;
  } else {
    body = keyInput(p);
  }
  const selectedFamily = state.family ? `<p class="route-note">For <strong>${safe(state.family)}</strong> · via ${p.name}. You can choose a different provider.</p>` : "";
  return `${button("← Change provider", "change-provider", "back")}${identity}${selectedFamily}${methodChoice}<form id="connect-form">${body}${advanced(p)}<p class="help" style="margin-top:20px">${note}</p><div class="actions"><button class="btn primary" type="submit">${cta} →</button>${button(state.saved ? "Done" : "Cancel", state.saved ? "done" : "cancel", "btn quiet")}</div></form>`;
}
function result() {
  const p = connection();
  const s = state.screen;
  const failures = {
    "missing": ["Credentials are missing", "No usable credential was found on the Evener host. Add access details or review the active credential source."],
    "configuration": ["Review the connection settings", "The connection could not be configured. Review the endpoint, API format, and required cloud values."],
    "save-failure": ["Access details were not saved", "The host could not save your changes. Your draft is retained here. Review the sanitized save error and retry."],
    "partial-save": ["Setup is only partly saved", "Some changes may have reached the host before a later step failed. Re-read the saved connection and credential status before retrying; do not create another connection."],
  };
  if (failures[s]) return `${header(...failures[s])}<div class="callout error" role="alert">${safe(failures[s][1])}</div><p class="help">No successful connection check is claimed. Production diagnostics must be sanitized.</p><div class="actions">${button("Review access and settings", "credential", "btn primary")}${button("Close setup", "done", "btn quiet")}</div>`;
  if (s === "review-destination") return `${header("Review where access will be used", "Confirm the destination and active credential before saving or sending a check.")}<div class="callout"><strong>Destination</strong><p class="destination">${safe(state.destination)}</p><strong>Credential source</strong><p>${safe(state.credentialSource)}</p></div><p class="help">Illustrative resolution, not a host scan. In the live flow, re-read resolved host status: environment or header credentials can take precedence over a saved key. “No new credential” does not disable existing access. Never carry a credential silently to a changed endpoint.</p><div class="actions">${button("Confirm destination & continue", "confirm-destination", "btn primary")}${button("Review settings", "credential", "btn quiet")}</div>`;
  if (s === "oauth-expired") return `${header("Sign-in code expired", "No new sign-in was completed. Start again or use the browser-redirect flow.")}<div class="callout" role="alert">Your previous connection, if any, is unchanged.</div><div class="actions">${button("Get a new code", "oauth", "btn primary")}${button("Back to sign-in options", "credential", "btn quiet")}</div>`;
  if (s === "oauth") return `${button("← Back", "credential", "back")}${header("Approve access in OpenAI", "Complete the browser sign-in, then return to Evener.")}<div class="callout"><strong>Device-code flow</strong><p>Enter this code on the verification page shown by OpenAI.</p></div><div class="code">DEMO-CODE</div>${button("Open sign-in page ↗", "demo-oauth", "btn primary full")}<p class="help">Mockup: this button opens the next simulated step, not a real sign-in page.</p><div class="actions">${button("I’ve approved access", "check", "btn")}${button("Cancel sign-in", "cancel", "btn quiet")}${button("Simulate expired code", "oauth-expired", "btn quiet")}</div><details><summary>Using a browser redirect instead?</summary><p class="help">Open the provider’s authorization URL, approve access, then paste the completed redirect URL below. This prototype does not open real authentication.</p>${field("redirect-url", "Completed redirect URL", "Paste the completed redirect URL", "", "password", "Treat this URL as a secret. It stays in memory in this mockup.", false)}${button("Simulate redirect completion", "demo-oauth", "btn")}</details>`;
  if (s === "checking") return `${header("Checking connection…", `Contacting ${p.name} from the Evener host.`)}<div class="callout" role="status">Your access details have been saved. Waiting for the provider response.</div><div class="actions">${button("Show simulated result", "check", "btn primary")}${button("Stop checking", "saved", "btn quiet")}</div>`;
  if (s === "success") return `<div class="status-symbol good" aria-hidden="true">✓</div>${header(`${p.name} is connected`, "Evener could access the model list. Choose a model to continue.")}<div class="result-card">${mark(state.provider)}<div class="grow"><strong>${p.name}</strong><p class="help">Model list checked · this setup only</p><label class="input-label" for="next-model" style="margin-top:14px">Model</label><select id="next-model"><option>${safe(p.model || "Provider default")}</option></select></div></div><p class="help">Connection: ${safe(p.id)}. Catalog choices are illustrative. This check does not verify generation or access to every model. Your global default is unchanged.</p><div class="actions">${button("Continue to first session", "done", "btn primary")}${button("Connect another provider", "home", "btn quiet")}</div>`;
  if (s === "saved") return `<div class="status-symbol" aria-hidden="true">○</div>${header("Saved, not checked", "Your access details are saved. Evener has not confirmed that this provider works.")}<div class="actions">${button("Check connection", "check", "btn primary")}${button("Back to provider setup", "credential", "btn quiet")}</div>`;
  if (s === "unsupported") return `${header("Saved · check unavailable", "This provider does not support the connection check.")}<div class="callout" role="status"><strong>Your setup is saved, but unverified.</strong><p>You can try it in a session. Evener will report any access or model errors there.</p></div><div class="actions">${button("Continue without verification", "done", "btn primary")}${button("Review settings", "credential", "btn quiet")}</div>`;
  const auth = s === "auth";
  return `${header(auth ? "The provider rejected access" : "Couldn’t reach the provider", auth ? "Your details are saved, but the connection is not working yet." : "Your details are saved. A network or endpoint problem interrupted the check.")}<div class="callout error" role="alert"><h3>${auth ? "Check your access" : "Check the connection"}</h3><p>${auth ? "The provider rejected these access details. Check the key or sign-in and your account’s API permissions." : "Check connectivity from the Evener host. If you use a proxy or gateway, review its URL."}</p></div><div class="actions">${button(auth ? "Edit access details" : "Retry check", auth ? "credential" : "check", "btn primary")}${button(auth ? "Retry check" : "Review settings", auth ? "check" : "credential", "btn")}${button("Done", "done", "btn quiet")}</div><details><summary>Technical details</summary><p class="help">Illustrative ${auth ? "auth_rejected" : "endpoint_failure"} result. Show sanitized diagnostics here, with no keys or raw response bodies.</p></details>`;
}
function models() {
  return `${header("Which models do you want to use?", "Connect one provider to make its models available. Add others later.")}<div class="model-card">${mark("anthropic")}<div class="grow"><strong>Claude</strong><p class="model-caption">Direct from Anthropic</p></div>${button("Connect Anthropic", "model-anthropic", "btn")}</div><div class="model-card">${mark("openai")}<div class="grow"><strong>GPT & Codex</strong><p class="model-caption">Direct from OpenAI</p></div>${button("Connect OpenAI", "model-openai", "btn")}</div><div class="model-card">${mark("google")}<div class="grow"><strong>Gemini</strong><p class="model-caption">Direct from Google</p></div>${button("Connect Google", "model-google", "btn")}</div><p class="help" style="margin-top:18px">Already use OpenRouter or a cloud provider? Choose that provider to use its billing and access.</p>${browseLinks()}`;
}
function methods() {
  return `${header("How would you like to connect?", "Connect one provider to get started. Use the access you already have.")}<div class="method-list"><button class="provider" data-action="chatgpt">${mark("openai")}<span class="grow"><strong>Sign in with ChatGPT</strong><small>Use an eligible ChatGPT plan · no API key</small></span><span aria-hidden="true">→</span></button><button class="provider" data-action="picker"><span class="mark" aria-hidden="true">⌁</span><span class="grow"><strong>Connect with a provider API key</strong><small>We’ll show you where to create one</small></span><span aria-hidden="true">→</span></button><button class="provider" data-action="local">${mark("custom")}<span class="grow"><strong>Use local or company access</strong><small>Ollama, cloud credentials, or a gateway</small></span><span aria-hidden="true">→</span></button></div><p class="footer-note">Not sure? ${button("Choose by provider instead →", "picker", "btn link")}</p>`;
}
function detected() {
  return `${button("← Back", "home", "back")}${header("Check existing access", "Credentials are configured on the Evener host. Check them before using a model.")}<div class="callout"><div class="row">${mark("anthropic")}<div><strong>Anthropic</strong><p class="help">Environment variable · key value hidden</p></div></div><p class="help">This is an illustrative detected credential, not a scan of your machine.</p></div><p class="help" style="margin-top:18px">The check requests the model list. It does not send a prompt or generate a response.</p><div class="actions">${button("Check Anthropic connection", "check-detected", "btn primary")}${button("Use different access", "picker", "btn quiet")}</div>`;
}
function finished() {
  const verified = state.lastOutcome === "success";
  const canContinue = verified || state.lastOutcome === "unsupported";
  return `${header(canContinue ? "Your first session" : "Finish connecting your first provider", verified ? "One provider is enough. You can add others later." : "This connection has not passed a check.")}<div class="callout" data-session-connection="${safe(state.provider ? connection().id : "")}"><strong>${safe(state.provider ? connection().name : "No provider selected")}</strong><p>${safe(state.selectedModel || "Choose a model before starting")}</p><p class="help">${verified ? "Model list checked; generation is not verified." : "Unverified connection. A session may report an access or model error."} Your global default is unchanged.</p></div>${canContinue ? `<div class="field-block"><label class="input-label" for="first-session-prompt">What would you like to work on?</label><textarea id="first-session-prompt" placeholder="e.g. Help me understand this project"></textarea></div>` : `<div class="actions">${button("Return to provider setup", "credential", "btn primary")}</div>`}<p class="help">End of design mockup: this shows the handoff to your first session. No prompt is sent and no real provider is contacted.</p><div class="actions">${button("Restart onboarding preview", "home", "btn quiet")}</div>`;
}
function body() {
  if (["credential"].includes(state.screen)) return credential();
  if (resultScreens.includes(state.screen)) return result();
  if (state.screen === "cancelled") return `${header("Setup closed", "Return to the invoking workspace. Unsaved access details have been discarded.")}<p class="help">End of prototype. No production settings changed.</p>${button("Try setup again", "home", "btn primary")}`;
  if (state.screen === "editor") return `${header("Full connection editor", "The existing expert editor remains available from this flow.")}<div class="callout"><strong>Retained controls</strong><ul><li>Connection name, rename, and provider identity</li><li>Surface, API format, and endpoint</li><li>Template variables and override resets</li><li>Environment and credential-header references</li><li>Active credential source, replace, clear stored key, and sign out</li></ul></div><p class="help">Design handoff to the existing editor; these expert operations are not rebuilt in this prototype. Returning preserves your setup draft.</p><div class="actions">${button("Back to connection setup", "credential", "btn primary")}</div>`;
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
  return `<section class="inline-page"><header><div class="eyebrow">Welcome to Evener</div><h1 style="margin-top:14px">Choose your first provider</h1><p>These are available choices. Connect one; the others can wait.</p></header><div class="section-label">Popular providers</div>${popular.map(id => `<div class="inline-row">${providerButton(id, state.provider === id && showing)}${state.provider === id && showing ? `<div class="inline-form">${body()}</div>` : ""}</div>`).join("")}${state.provider && !popular.includes(state.provider) && showing ? `<div class="inline-row"><div class="inline-form">${body()}</div></div>` : ""}${browseLinks()}</section>`;
}
function guided() {
  const step = state.screen === "success" || (state.screen === "done" && state.lastOutcome === "success") ? 3 : state.provider ? 2 : 1;
  return `<section class="guided"><aside class="guided-rail"><div class="brand"><span class="brand-mark">≋</span>evener</div><h2>Bring your<br>favorite models.</h2><div class="guided-steps">${["Provider", "Access", "Ready"].map((name, i) => `<div class="guided-step ${step === i + 1 ? "active" : ""}"><b>${step > i + 1 ? "✓" : i + 1}</b>${name}</div>`).join("")}</div><p class="guided-note">You only need one provider.<br>Add others later in Settings.</p></aside><div class="guided-content">${body()}</div></section>`;
}
function directory() {
  return `<section class="directory ${state.provider ? "has-selection" : ""}"><aside class="directory-list"><h3>Available providers</h3><label for="provider-search" class="section-label">Find a provider</label><input id="provider-search" class="search" type="search" placeholder="Search providers"><div class="section-label">All providers · common first</div><div class="provider-list" id="search-results">${Object.keys(providers).map(id => `<div data-search="${safe(`${providers[id].name} ${providers[id].sub}`.toLowerCase())}">${providerButton(id, state.provider === id)}</div>`).join("")}</div><p id="no-results" class="no-results" hidden>No matching provider. Try a different name.</p></aside><div class="directory-content">${state.screen === "directory" ? `<div class="empty-detail"><span class="mark" aria-hidden="true">↗</span><h2 tabindex="-1" data-heading>Connect your first provider</h2><p class="muted">Choose one provider to get started.<br>We’ll help you get the access it needs.</p></div>` : body()}</div></section>`;
}
function render(focus = true) {
  const toolbar = `<header class="study-bar"><div class="row"><a href="index.html">← Six options</a><span class="study-name">${direction.toUpperCase()} · ${concept.name}</span></div><div class="study-controls">${button("Reset", "home", "btn quiet")}<label for="demo-outcome">Check result</label><select id="demo-outcome">${[["success", "Success"], ["auth", "Access rejected"], ["endpoint", "Endpoint unavailable"], ["unsupported", "Check unsupported"], ["saved", "Saved, not checked"], ["missing", "Missing credentials"], ["configuration", "Configuration failure"], ["save-failure", "Save failure"], ["partial-save", "Partial save"]].map(([value, text]) => `<option value="${value}" ${state.outcome === value ? "selected" : ""}>${text}</option>`).join("")}</select>${button("Fill demo details", "fill-demo", "btn")}</div></header><div class="study-note"><strong>DESIGN MOCKUP</strong> · ${concept.thesis} Nothing is saved or sent. Use demo details, never real credentials. Provider catalog is a representative sample.</div>`;
  let canvas;
  if (state.screen === "cancelled" || state.screen === "done") canvas = `<section class="panel">${body()}</section>`;
  else if (direction === "b") canvas = inline();
  else if (direction === "c") canvas = guided();
  else if (direction === "f") canvas = directory();
  else if (direction === "d") canvas = `<div class="model-stage"><div class="row spread"><h1>New session</h1><span class="tag">Your first session</span></div><div class="composer">What would you like to work on?<div class="row spread" style="margin-top:26px"><span class="tag">${state.family || "Choose a model"} ▾</span><span aria-hidden="true">↑</span></div></div><div class="panel model-picker">${body()}</div></div>`;
  else canvas = `<section class="panel">${body()}</section>`;
  if (state.screen === concept.home) canvas = `<section class="onboarding-start"><div class="first-run-status" data-empty-state><strong>No providers connected yet</strong><p>Choose one below. We’ll guide you through access and into your first session.</p></div>${canvas}</section>`;
  app.innerHTML = `${toolbar}<div class="app-shell"><aside class="rail"><div class="brand"><span class="brand-mark">≋</span>evener</div><nav aria-label="Workspace context"><div class="rail-item">+ New session</div><div class="rail-item">Sessions</div><div class="rail-item">Projects</div><div class="rail-item active">Get started</div></nav><div class="rail-bottom">Local host<br><span class="small">Design study · September 2026</span></div></aside><main class="workspace"><div class="workspace-top"><span>Welcome / Connect your first provider</span><span>Preview ${direction.toUpperCase()}</span></div><div class="stage">${canvas}</div></main></div>`;
  restoreDraft();
  bind();
  const search = document.getElementById("provider-search");
  if (direction === "f" && search) { search.value = directoryQuery; search.dispatchEvent(new Event("input")); }
  if (focus) {
    const target = state.screen === "directory" ? app.querySelector(`[data-provider="${returnProvider}"]`) || search : app.querySelector("[data-heading]");
    target?.focus();
    target?.scrollIntoView({ block: "nearest" });
  }
}
function rememberDraft() {
  const form = document.getElementById("connect-form");
  if (!form) return;
  drafts.set(`${state.provider}:${state.method}:${state.source}`, {
    values: [...form.querySelectorAll("input, select, textarea")].map(el => [el.id || el.name, el.value]),
    advanced: document.getElementById("advanced").open,
  });
}
function restoreDraft() {
  const draft = drafts.get(`${state.provider}:${state.method}:${state.source}`);
  const form = document.getElementById("connect-form");
  if (!draft || !form) return;
  for (const [key, value] of draft.values) {
    const input = [...form.querySelectorAll("input, select, textarea")].find(el => (el.id || el.name) === key);
    if (input) input.value = value;
  }
  document.getElementById("advanced").open = draft.advanced;
}
function choose(id) { rememberDraft(); state.provider = id; state.screen = "credential"; state.saved = false; state.source = "key"; state.method = state.entryMethod || "signin"; render(); }
function go(screen) { state.screen = screen; render(); }
function finishCheck() {
  state.saved = !["save-failure", "partial-save", "missing"].includes(state.outcome);
  state.lastOutcome = state.outcome;
  go(state.outcome);
}
function bind() {
  app.querySelectorAll("[data-provider]").forEach(el => el.addEventListener("click", () => choose(el.dataset.provider)));
  app.querySelectorAll("[data-action]").forEach(el => el.addEventListener("click", () => action(el.dataset.action, el)));
  document.getElementById("demo-outcome")?.addEventListener("change", e => { state.outcome = e.target.value; });
  document.getElementById("provider-search")?.addEventListener("input", e => {
    const term = e.target.value.toLowerCase().trim();
    if (direction === "f") directoryQuery = e.target.value;
    const rows = [...document.querySelectorAll("[data-search]")];
    rows.forEach(el => { el.hidden = !el.dataset.search.includes(term); });
    document.getElementById("no-results").hidden = rows.some(el => !el.hidden);
  });
  document.getElementById("connect-form")?.addEventListener("invalid", e => {
    const disclosure = e.target.closest("details");
    if (disclosure) disclosure.open = true;
  }, true);
  document.getElementById("connect-form")?.addEventListener("submit", e => {
    e.preventDefault();
    if (!e.target.reportValidity()) return;
    rememberDraft();
    const p = connection();
    const endpoint = document.getElementById("endpoint")?.value;
    const override = e.target.querySelector('[name="advanced-url"]')?.value;
    const env = e.target.querySelector('[name="key-env"]')?.value;
    const header = e.target.querySelector('[name="credential-header"]')?.value;
    state.destination = override || endpoint || p.url || "Provider endpoint resolved from the entered cloud values";
    state.credentialSource = document.getElementById("custom-auth")?.value === "none" || (p.mode === "local" && !document.getElementById("api-key")?.value.trim()) ? "No new credential supplied. Inspect existing host access before continuing." : state.source === "host" || env || header ? "Host environment or header reference. The live flow must show its resolved source, never its secret." : "New credential supplied for this connection (simulated). Confirm whether host access takes precedence.";
    if (p.codex) go("oauth");
    else if (p.mode === "custom" || (endpoint && endpoint !== p.url) || (override && override !== p.url) || env || header || state.source === "host") go("review-destination");
    else finishCheck();
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
  document.getElementById("cloud-auth")?.dispatchEvent(new Event("change"));
  document.getElementById("custom-auth")?.dispatchEvent(new Event("change"));
}
function action(name, element) {
  rememberDraft();
  if (name === "home") { drafts.clear(); directoryQuery = ""; returnProvider = null; state.provider = null; state.family = null; state.saved = false; state.entryMethod = null; state.source = "key"; state.lastOutcome = null; state.selectedModel = null; go(concept.home); }
  else if (name === "change-provider") { returnProvider = state.provider; state.provider = null; go(direction === "f" ? "directory" : "picker"); }
  else if (name === "cancel") { drafts.clear(); state.saved = false; state.lastOutcome = null; state.provider = null; go("cancelled"); }
  else if (name === "done") { state.selectedModel = document.getElementById("next-model")?.value || null; drafts.clear(); go("done"); }
  else if (name === "source-host" || name === "source-key") { state.source = name === "source-host" ? "host" : "key"; render(); }
  else if (name === "picker" && direction === "e") { state.entryMethod = "key"; go("picker"); }
  else if (name === "custom") choose("custom");
  else if (name === "chatgpt") { state.entryMethod = "signin"; choose("openai"); }
  else if (name === "signin-method" || name === "key-method") { state.method = name === "key-method" ? "key" : "signin"; render(); }
  else if (name.startsWith("model-")) { const id = name.slice(6); state.family = providers[id].family; choose(id); }
  else if (name === "check-detected") { state.provider = "anthropic"; finishCheck(); }
  else if (name === "check" || name === "confirm-destination") finishCheck();
  else if (name === "demo-oauth") { state.saved = true; go("checking"); }
  else if (name === "show-key" || name === "show-json") {
    const input = document.getElementById(name === "show-json" ? "credential-json" : "api-key");
    const show = input.type === "password";
    input.type = show ? "text" : "password";
    element.textContent = show ? "Hide" : "Show";
    element.setAttribute("aria-label", `${show ? "Hide" : "Show"} ${name === "show-json" ? "credential JSON" : "API key"}`);
    element.setAttribute("aria-pressed", String(show));
  } else if (name === "fill-demo") {
    const demo = { "api-key": "demo-key-not-a-secret", endpoint: "https://gateway.example.com/v1", project: "demo-project", region: "us-central1", resource: "demo-resource", "credential-json": '{"demo":"not real credentials"}' };
    for (const [id, value] of Object.entries(demo)) { const input = document.getElementById(id); if (input && (!input.value || id === "api-key")) input.value = value; }
    const format = document.getElementById("custom-format");
    if (format) format.selectedIndex = 1;
  } else go(name);
}
render(false);
