// Shared loader for the renderer's no-bundler <script> bundle.
//
// renderer.js is split across several files that share the
// window.SerfRendererInternal namespace and must be evaluated in dependency
// order — exactly as templates/app.html loads them. Centralizing the file
// list here means adding a module touches one place instead of every test
// harness that evals the renderer.
const fs = require("fs");
const path = require("path");

// Dependency order: leaf helpers first, then consumers, then the core/bootstrap
// in renderer.js last. Must match the <script> order in templates/app.html.
const RENDERER_FILES = [
  "icons.js",
  "renderer-format.js",
  "renderer-tools.js",
  "renderer-panels.js",
  "renderer.js",
];

function rendererSources() {
  return RENDERER_FILES.map((f) =>
    fs.readFileSync(path.resolve(__dirname, "../assets", f), "utf8")
  );
}

// evalRenderer evals the whole renderer bundle into the given JSDOM window,
// in load order. Use this instead of eval-ing renderer.js directly so the
// shared SerfRendererInternal namespace is populated before renderer.js runs.
function evalRenderer(window) {
  for (const src of rendererSources()) window.eval(src);
}

module.exports = { evalRenderer, rendererSources, RENDERER_FILES };
