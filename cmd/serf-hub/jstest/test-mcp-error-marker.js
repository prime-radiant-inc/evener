// Regression: MCP-namespaced tool names (server__tool, e.g.
// linear__search_issues) have no dedicated entry in toolRenderers, so they
// fall through to __default__. A Channel-B server-reported error (see
// agent/internal/mcp.TestMCPManager_ChannelBError_IsErrorTypedResult on the
// Go side) must still reach the default renderer as an error marker: the
// executor returns the error through the error path, ExecResult.IsError is
// true, and session_tools.go maps that to TOOL_CALL_END's `error` field
// (session_tools.go:445-448) — consumed here as `data.error`.
const assert = require("assert");
const { JSDOM } = require("jsdom");

const dom = new JSDOM(`<!DOCTYPE html><html><body>
  <div class="workspace-actions">
    <button data-tasks-trigger><span class="panel-toggle-label">tasks</span></button>
    <button data-details-trigger><span class="panel-toggle-label">details</span></button>
  </div>
  <header class="workspace-header" data-session-id="01TEST"></header>
  <div id="conversation"
       data-session-id="01TEST"
       data-state="idle"></div>
  <form data-input-form data-session-id="01TEST">
    <textarea class="message-input"></textarea>
  </form>
</body></html>`, {
  runScripts: "outside-only",
  pretendToBeVisual: true,
  url: "http://127.0.0.1:9180/s/01TEST",
});

const { window } = dom;
window.marked = { parse: (t) => t };
require("./load-renderer").evalRenderer(window);

const { toolRendererFor } = window.SerfRendererInternal;

const r = toolRendererFor("linear__search_issues"); // MCP names fall through to __default__
const marker = r.result({ error: "[MCP Error] upstream 400" }, "");
assert.strictEqual(marker, "error", "MCP-namespaced tool with error must render the error marker");
const ok = r.result({}, "done");
assert.strictEqual(ok, "ok", "MCP-namespaced tool without error still renders ok");

console.log("PASS test-mcp-error-marker.js");
process.exit(0);
