# read-image-tool-result-inline: render tool-result image bytes under the producing row

**What this covers**: inline output-image v1. A tool result that carries image
BYTES — `read_file` on a PNG, whose bytes the daemon routes out of the text
output into a side channel (`agent/internal/tool/registry.go:197-219`
`ParseImageResult`) — reaches the browser as a *descriptor* and renders as a
thumbnail under that same tool row. AppWire must carry no bytes and no base64
for it: `appwire.OutputImage` (`appwire/types.go:730-738`) has Source / Name /
MediaType / Size / URL / SHA / Path and **no data field at all**. If
`events.ToolResultOutputImage` (`agent/events/payloads.go#ToolResultOutputImage`), the hub's
sha-route stamp (`cmd/serf-hub/output_images.go:169-181`), or the transcript
byte-serving route regresses, this catches it.

**Surface**: see `docs/agentic-testing.md`, "Driving the web UI" — its selector
map is the single place these hooks are maintained. The `.tool-output-images
.user-image-card img.user-image-thumb` / `#image-lightbox` selectors this card
used to name died with the vanilla frontend (`660376f78`); the gallery is now
`ImageGallery.tsx` and the lightbox is the shared `Dialog` widget.

## Pre-state

- A freshly built hub on an isolated `$HOME` and a kernel-assigned port — see
  the Setup checklist in `docs/agentic-testing.md`. Token at
  `$HOME/.serf/auth-token` (that isolated one).
- A hermetic `$WORK` containing a small valid PNG, `fixture.png`.
- A cheap image-capable model, e.g. `anthropic/claude-haiku-4-5-20251001`.
- For the browser step only: run `make build-web` before building the hub. A
  checkout that has never run it ships a one-line `frontend/dist/PLACEHOLDER`
  and serves no app (rebuild matrix item 3 in the runbook).

## Steps

1. **Spawn (browser-free).** `POST $HUB/api/spawn` with `working_dir: $WORK`
   and a prompt that forces the read, e.g. *"Call read_file on fixture.png,
   then stop."* Poll `GET $HUB/api/sessions/local:$SID` for `state` `idle`.

2. **Descriptor shape — the exact assertions, no browser needed.** Dial
   `ws://127.0.0.1:$PORT/rpc` with `Authorization: Bearer $TOKEN`,
   `initialize`, then `thread/read{ref:"local:$SID", includeTurns:true}`.
   Find the `commandExecution` item whose `toolName` is `read_file` (by name,
   never by turn index) and read its `outputImages`.

3. **Fetch the bytes (browser-free).** `curl` each descriptor's `url` with the
   same `Authorization: Bearer $TOKEN` header — the whole mux sits behind one
   guard (`cmd/serf-hub/web.go:176,180`).

4. **Browser (qualitative).** Navigate
   `$HUB/auth?token=$TOKEN&next=/s/local:$SID`. Find the tool row
   (`[data-testid="tool-call-item"][data-tool-name="read_file"]`), **expand
   it** — see Sharp edges, the gallery is not in the DOM while collapsed —
   then read:
   ```javascript
   ({
     port: location.port,                       // page-identity check, always
     path: location.pathname,                   // /s/local:<SID>
     thumbs: document.querySelectorAll('[data-testid="image-gallery-thumb"]').length,
     srcs: Array.from(document.querySelectorAll('[data-testid="image-gallery-thumb"] img'),
                      (i) => i.getAttribute("src")),
     caption: document.querySelector('[data-testid="image-gallery-caption"]')?.textContent,
   })
   ```
   Then click a thumb and re-read for `[data-testid="image-gallery-lightbox-img"]`.

## Expected

- **Step 2 (descriptor, exact)**: at least one `outputImages` entry on the
  `read_file` item. Each carries a 64-char lowercase-hex `sha`, a `mediaType`
  starting `image/`, a positive `size`, and a `url` that is **same-origin and
  relative** — one of `/s/$SID/images/<sha>` (`output_images.go:206-208`) or
  `/doc/image?session=…&path=…` (`output_images.go:283`). Falsify: the item has
  no `outputImages` at all; any entry carries embedded bytes or a `data:` URL
  (`OutputImage` has no field to put them in, so this can only mean the wire
  type changed); or a `url` containing `://`.
- **Step 3 (bytes)**: 200, `Content-Type` an image type, and for the
  `/s/…/images/<sha>` form the body's sha256 equals the descriptor's `sha` —
  that route only serves bytes it found in the transcript by that exact hash
  (`image_serve.go:104-127`). Falsify: 404 (descriptor points at bytes the hub
  cannot serve, i.e. a thumbnail that will silently vanish in the browser —
  see Sharp edges), or 400 (`sha` is not 64 hex, `image_serve.go:20,32-35`).
- **Step 4 (browser)**: expanding the `read_file` row shows a thumbnail whose
  `src` is exactly one of the URLs asserted in step 2 — the component does no
  URL construction of its own (`ImageGallery.tsx:1-16`). A `read_file` image
  captions as its filename (`captionFor`, `ImageGallery.tsx:56-58`, `name ??
  path ?? source`). Clicking it opens `[data-testid="image-gallery-lightbox-img"]`
  inside `[role="dialog"][aria-modal="true"]`. Falsify: the expanded row shows
  the text result but no thumb; the thumb hangs off a different tool row; or
  the `src` differs from the wire's `url`.
- **Two descriptors, one image, is not a bug.** `read_file` can be described
  twice — file-backed (`source: "read-file"`, the `/doc/image` route,
  `output_images.go:49-61`) and sha-addressed (`source: "tool-result"`). The
  hub dedupes them by sha, preferring sha over URL as content identity
  (`appendOutputImagesUnique`, `app_threadread.go#appendOutputImagesUnique`), so the expected
  count is **one rendered thumb**, not two. Falsify: two thumbs of the same
  image side by side (kata `1nr4`'s regression).

## Cleanup

`POST $HUB/api/sessions/local:$SID/shutdown` (the old `/s/$SID/shutdown` shim
404s silently and leaves the daemon up), then kill the hub by the PID you
captured and `rm -rf` your own run dir. Leave any real hub untouched.

## Sharp edges

- **A tool row starts COLLAPSED, and the gallery only exists while it is
  expanded.** `<ImageGallery images={item.outputImages} />` is inside
  `{expanded && …}` (`ToolCallItem.tsx:270-286`), and nothing about carrying
  images auto-expands a row — only `descriptor.autoExpand` or a failure does
  (`:163-170`). A `read_file` that succeeded is collapsed, so a DOM query run
  straight after page load finds zero thumbs and looks exactly like a
  regression. Expand the row first.
- **The gallery silently drops any `src` the browser refuses to load**
  (`onError` → `markUnloadable`, `ImageGallery.tsx:73-77,140`), leaving the row
  looking as if no descriptor ever arrived. That is why step 3 fetches the URL
  out-of-band: a 404 there is the difference between "not rendered" and "not
  fetchable".
- **The transcript is virtualized** (`VirtualList`, `panes/session/Session.tsx`),
  so a global thumb count measures the viewport. Scope every query to the one
  row you expanded.
- Bytes for the sha route are found by rescanning the session transcript on
  each request (`image_serve.go:68-133`), so they exist only once the round
  carrying them has been written. A descriptor whose bytes are not yet on disk
  is a daemon-side hold (kata `v3dv`), not something the browser retries.
- The pure resolver/serving logic is already unit-tested — `output_images_test.go`
  (`TestOutputImagesForToolCallReadFileStructuredRead`) and
  `live_tool_result_image_test.go`
  (`TestALiveImageReadThumbnailPointsAtAServableRoute`). If those pass and this
  card fails, the break is in the wiring, not the resolver.
