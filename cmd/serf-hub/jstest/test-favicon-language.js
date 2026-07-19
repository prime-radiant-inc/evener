// Favicon language: pinned dark-theme constants (the icon renders against
// dark browser chrome regardless of page theme). Base circle is NEUTRAL —
// post-recolor blue means working, so a blue base would read "working" at rest.
const fs = require("fs");
const js = fs.readFileSync(__dirname + "/../assets/notifications.js", "utf8");
const thread = fs.readFileSync(__dirname + "/../templates/thread.html", "utf8");
function assert(cond, msg) { if (!cond) { console.error("FAIL: " + msg); process.exit(1); } }

assert(/%237e8593/.test(js), "PLAIN_FAVICON base is neutral #7e8593");
assert(/fill='#7e8593'/.test(js), "buildFaviconDataURI base circle is neutral");
assert(/needs_you:\s*"#e0af68"/.test(js), "needs_you dot is amber");
assert(/working:\s*"#7aa2f7"/.test(js), "working dot is blue");
assert(/error:\s*"#f7768e"/.test(js), "error dot stays red");
assert(!/%237aa2f7/.test(thread), "thread.html favicon is no longer blue");
assert(/%237e8593/.test(thread), "thread.html favicon is neutral");
console.log("ok favicon language");
process.exit(0);
