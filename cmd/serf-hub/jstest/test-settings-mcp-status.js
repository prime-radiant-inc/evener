// Asserts the MCP settings partial (templates/partials/settings/mcp.html)
// server-renders the probed-status list fed by settingsData.Mcps/.McpsError,
// instead of leaving those Go-computed fields discarded. mcp.html is a Go
// html/template, not a hand-written client asset — there is no established
// JSDOM harness in this suite for rendering Go templates (test-settings.js
// and test-settings-dir-picker.js both load hand-written assets/*.js files
// into JSDOM, never a .html template), so this is a static structural check
// on the template source, mirroring test-color-system-css.js's plain
// regex-assertion style rather than a DOM-rendering test.
const fs = require("fs");
const path = require("path");

const html = fs.readFileSync(path.resolve(__dirname, "../templates/partials/settings/mcp.html"), "utf8");

const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

// The caption, verbatim, as specified by the task.
pass(/as probed from the hub/.test(html), 'caption "as probed from the hub" is present');

// A real server-rendered {{range}} over .Mcps — the computed-and-discarded bug
// being fixed — with the four required columns: name, transport, status,
// last error.
pass(/\{\{range\s+\.Mcps\}\}/.test(html), "template ranges over .Mcps (server-rendered, not client-JS-only)");
pass(/\{\{\.Name\}\}/.test(html), "row renders .Name");
pass(/\{\{\.Transport\}\}/.test(html), "row renders .Transport (transport column)");
pass(/\{\{\.Status\}\}/.test(html), "row renders .Status (status column)");
pass(/\{\{if\s+\.Error\}\}[\s\S]*?\{\{\.Error\}\}/.test(html), "row conditionally renders .Error (last-error column)");

// The probe status drives the existing .status-badge.status-<value> CSS
// convention (already styled for available/unreachable/missing).
pass(/status-badge status-\{\{\.Status\}\}/.test(html), "status is rendered via the existing status-badge convention");

// Both the recoverable-error state (.McpsError) and the empty state (no
// servers configured) must render something, not silently disappear.
pass(/\{\{if\s+\.McpsError\}\}/.test(html), "template branches on .McpsError");
pass(/No MCP servers configured/.test(html), "empty-state copy is present for a zero-server config");

// The pre-existing editable sections (MCP config files / inline MCP
// servers, client-JS-rendered from launchconfig.getLayer) must survive
// untouched — the new block is additive, not a replacement.
pass(/id="mcps-form"/.test(html), "existing #mcps-form editable section still present");
pass(/launchconfig\.getLayer/.test(html), "existing client-JS launchconfig wiring still present");
pass(/MCP config files/.test(html), "existing 'MCP config files' collection still present");
pass(/Inline MCP servers/.test(html), "existing 'Inline MCP servers' collection still present");

if (failures.length === 0) {
  console.log("PASS test-settings-mcp-status");
  process.exit(0);
}
for (const failure of failures) console.log(failure);
process.exit(1);
