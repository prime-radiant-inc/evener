// Light theme tints .sb-row[data-state="awaiting|active|warning"] with an
// explicit [data-theme="light"] override, but the errored row had none — it
// fell through to the base 5% error-mix, which reads too faint against the
// light background. Assert the parallel light-theme errored override exists.
const fs = require("fs");
const css = fs.readFileSync(__dirname + "/../assets/style.css", "utf8");
if (!/\[data-theme="light"\]\s+\.sb-row\[data-state="errored"\]/.test(css)) {
  console.log("FAIL: light theme needs an explicit errored sidebar-row tint (parallel to awaiting/active/warning)");
  process.exit(1);
}
console.log("PASS: light-theme errored tint present");
process.exit(0);
