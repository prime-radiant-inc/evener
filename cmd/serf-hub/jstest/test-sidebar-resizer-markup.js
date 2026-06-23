// App shell must expose a full-height sidebar border resizer, not a browser corner handle.
const fs = require("fs");
const path = require("path");
const app = fs.readFileSync(path.resolve(__dirname, "../templates/app.html"), "utf8");
const failures = [];
const pass = (cond, msg) => { if (!cond) failures.push("FAIL: " + msg); };

pass(app.includes('id="sidebar-resizer"'), "app shell should include #sidebar-resizer next to #sidebar");
pass(app.includes('class="sidebar-resizer"'), "sidebar resizer should carry .sidebar-resizer");
pass(app.includes('role="separator"'), "sidebar resizer should use separator semantics");
pass(app.includes('aria-controls="sidebar"'), "sidebar resizer should point at #sidebar");

const sidebarIdx = app.indexOf('id="sidebar"');
const resizerIdx = app.indexOf('id="sidebar-resizer"');
const workspaceIdx = app.indexOf('id="workspace"');
pass(sidebarIdx >= 0 && resizerIdx > sidebarIdx && resizerIdx < workspaceIdx, "sidebar resizer should sit between sidebar and workspace");

if (failures.length > 0) {
  for (const f of failures) console.log(f);
  process.exit(1);
}
console.log("PASS: sidebar full-border resizer markup");
process.exit(0);
