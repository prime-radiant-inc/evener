# unsafe-image-path-ignored: do not preview out-of-cwd image candidates

**What this covers**: web UI inline output-image v1 card `unsafe-image-path-ignored`. A shell command prints an out-of-cwd image path, traversal path, or external-looking candidate; the backend rejects it with the same containment boundary as `/doc/file`, and the web UI leaves the tool row otherwise intact with no preview.

## Pre-state

- Use a local deterministic scenario harness or scripted provider at the LLM boundary; do not use live provider credentials or network access.
- Start a fresh session with cwd `$WORK`, a temp directory owned by the scenario.
- Create a valid image outside `$WORK`, for example `$OUTSIDE/outside.png`.
- The scripted model turn calls `exec_command`/`shell` that prints candidates such as `$OUTSIDE/outside.png`, `../outside.png`, and `https://example.invalid/outside.png`, but does not create a valid image under `$WORK`.
- `serf-hub` is serving the session UI with AppWire enabled.

## Steps

1. Open the session in the web UI and wait for the shell row to complete.
2. Locate the completed shell row containing the unsafe candidate text.
3. Inspect the row for `.tool-output-images` and any `img.user-image-thumb`.
4. Optionally call the corresponding `/doc/image?session=<sid>&path=../outside.png` route directly and verify it rejects the traversal/out-of-cwd request.

## Expected

- The shell row remains visible with its text output; invalid output-image candidates do not fail or hide the tool row.
- No `.tool-output-images` block and no output thumbnail is rendered for the out-of-cwd, traversal, external, missing, non-image, or SVG candidate.
- The UI does not fetch or display any image outside the session cwd and does not accept arbitrary external/protocol-relative descriptor URLs.
- Falsification: any thumbnail renders for `$OUTSIDE/outside.png`, `../outside.png`, an external URL, or another path outside the session cwd.

## Cleanup

- Shut down the scripted session and remove `$WORK`, `$OUTSIDE`, and any scenario state directory.

## Sharp edges

- This scenario should pass by omission: the correct behavior is no preview, not an error banner. Invalid candidates must not fail the tool row.
