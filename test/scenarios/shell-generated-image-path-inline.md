# shell-generated-image-path-inline: preview a shell-created image path

**What this covers**: web UI inline output-image v1 card `shell-generated-image-path-inline`. A shell command writes an image under the session cwd and prints its path; the server conservatively recognizes and validates the path, and the web UI renders the image inline under the shell row.

## Pre-state

- Use a local deterministic scenario harness or scripted provider at the LLM boundary; do not use live provider credentials or network access.
- Start a fresh session with cwd `$WORK`, a temp directory owned by the scenario.
- The scripted model turn calls `exec_command`/`shell` with a command that creates a tiny supported image under `$WORK`, then prints a simple relative path such as `created ./plot.png`.
- `serf-hub` is serving the session UI with AppWire enabled.

## Steps

1. Open the session in the web UI and wait for the shell row to complete.
2. Confirm `$WORK/plot.png` exists and is a supported PNG/JPEG/GIF/WebP image.
3. Locate the completed shell row whose output text contains `plot.png`.
4. Inspect that row for `.tool-output-images` containing one `.user-image-card img.user-image-thumb`.
5. Inspect the thumbnail `src`.

## Expected

- The shell row contains both the original textual output (`plot.png`) and one inline preview under that same row.
- The preview URL is same-origin relative and server-generated, normally `/doc/image?session=<sid>&path=plot.png`.
- The server, not the frontend, performs path inference and validation. The frontend does not parse shell output text to create previews.
- Falsification: the shell row contains path text but no thumbnail for the generated image, or the thumbnail is created client-side from arbitrary shell text without a validated descriptor.

## Cleanup

- Shut down the scripted session and remove `$WORK` plus any scenario state directory.

## Sharp edges

- Keep shell inference conservative: print a plain relative path under cwd. URLs, traversal, missing files, non-images, and SVG are covered by `unsafe-image-path-ignored.md` and lower-level resolver tests.
