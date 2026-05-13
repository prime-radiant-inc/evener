const fs = require("fs");

const SRC = fs.readFileSync("../assets/renderer.js", "utf8");

function assert(cond, msg) {
  if (!cond) {
    console.error("FAIL: " + msg);
    process.exit(1);
  }
}

assert(
  /json\.ref\s*\|\|\s*json\.child_session_id\s*\|\|\s*json\.session_id/.test(SRC),
  "fork navigation should prefer source-qualified json.ref before child_session_id"
);

console.log("PASS: renderer fork navigation preserves source-qualified refs");
