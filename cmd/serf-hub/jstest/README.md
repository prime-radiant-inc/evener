# renderer.js JSDOM tests

These tests load `../assets/renderer.js` into a JSDOM window, mock
`EventSource` + `fetch`, fire captured event streams, and assert on the
DOM that the renderer builds. They cover the parts of the rendering
contract that are easiest to break: steering classification, task_list
system-line prose, full-list pointer rendering, and the sidebar's
per-task expandable detail.

## Running

```sh
cd cmd/serf-hub/jstest
npm init -y > /dev/null && npm install jsdom --silent
node test-renderer.js
node test-renderer-advanced.js
node test-diagnostics.js
node test-appwire-diagnostics.js
node test-sidebar.js
```

Each script exits 0 on success and prints the rendered HTML on failure.
