# output-image-lightbox-and-pane: open previews in lightbox and side pane

**What this covers**: web UI inline output-image v1 card `output-image-lightbox-and-pane`. A rendered output image opens in the existing image lightbox on thumbnail click and offers the open-beside affordance when its descriptor has a valid same-origin stable URL.

## Pre-state

- Use any deterministic local session from `read-image-tool-result-inline.md`, `written-image-inline-after-reload.md`, or `shell-generated-image-path-inline.md` that renders an output image thumbnail.
- The descriptor URL must be same-origin relative and stable, such as `/doc/image?session=<sid>&path=out.png` or `/s/<sid>/images/<sha>`.
- Load the session in the normal hub workspace, not an isolated iframe that disables `window.SerfPanes`.

## Steps

1. Locate an output-image thumbnail under a completed tool row (`.tool-output-images .user-image-card`).
2. Click the image card itself.
3. Close the lightbox with Escape or its close gesture.
4. Locate the open-beside control associated with the same output image (`.open-beside-btn`, visually the quiet `⇲` control beside the image card).
5. Click the open-beside control.
6. Inspect the workspace side-pane area.

## Expected

- Clicking the thumbnail opens exactly one `#image-lightbox` overlay showing the selected image and caption; Escape closes it.
- The open-beside control is present for the valid same-origin image URL and is not nested inside the image-card button.
- Clicking open-beside does not open the lightbox and does not navigate the main session away. It opens the image URL in a side pane through `SerfPanes`/the host open-beside bridge.
- Falsification: clicking the thumbnail does not open the lightbox, the open-beside control is missing for a valid same-origin output image URL, clicking `⇲` opens the lightbox instead of the side pane, or an external/protocol-relative URL receives an open-beside/rendering affordance.

## Cleanup

- Close the lightbox/side pane and shut down the deterministic scenario session if it was created only for this card.

## Sharp edges

- Open-beside is intentionally unavailable in contexts without the pane host. Run this card in the normal workspace shell so absence of `.open-beside-btn` is meaningful.
