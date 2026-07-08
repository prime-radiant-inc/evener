# written-image-inline-after-reload: keep structured written images inline after replay

**What this covers**: web UI inline output-image v1 card `written-image-inline-after-reload`. A structured file-writing tool writes a supported image under the session cwd; the live web UI shows the image inline under the producing tool row, and a page reload reconstructs the same preview from transcript/session state.

## Pre-state

- Use a local deterministic scenario harness or scripted provider at the LLM boundary; do not use live provider credentials or network access.
- Start a fresh session with cwd `$WORK`, where `$WORK` is a temp directory owned by the scenario.
- The scripted model turn calls `write_file` (or another structured file-writing tool) with `file_path: "out.png"` and valid PNG bytes/content that the real tool writes under `$WORK`.
- `serf-hub` is serving the session UI with AppWire enabled.

## Steps

1. Open the session in the web UI and wait for the `write_file` row to complete.
2. Confirm `$WORK/out.png` exists and is a supported image (PNG, JPEG, GIF, or WebP; SVG is out of scope for v1).
3. In the conversation, locate the completed `write_file` row.
4. Verify the row contains `.tool-output-images` with one `.user-image-card img.user-image-thumb` whose `src` is a same-origin relative `/doc/image?session=<sid>&path=out.png` URL.
5. Reload the page, or close and reopen the same session URL.
6. Locate the same `write_file` row after replay/reload.

## Expected

- Before reload, the `write_file` row shows one inline thumbnail for `out.png` under the tool row.
- After reload, the same thumbnail is still present under the same `write_file` row and still resolves through `/doc/image` inside the session cwd boundary.
- AppWire carries only output-image descriptors for the file-backed image, not generated file bytes/base64 data.
- Falsification: the image appears live but disappears after reload, appears under a different row, resolves outside `/doc/image`, or uses an external/protocol-relative URL.

## Cleanup

- Shut down the scripted session and remove `$WORK` plus any scenario state directory.

## Sharp edges

- The reload assertion is the important distinction from live-only notification rendering: it proves transcript-backed `thread/read` enrichment can rediscover the file-backed descriptor.
