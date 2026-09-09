# Native media caption confirmation

## Bounded verdict

The retained installed-app evidence confirms the narrow caption fix. In
`before-install.ax.json`, both filename caption nodes occupy 95 pt high frames
at y=587. In `caption-installed.ax.json`, the same full filename labels occupy
38 pt high frames at the same y position. The thumbnails remain 112 x 112 and
their accessibility labels preserve the complete filenames.

The installed full-screen viewer is confirmed by
`corrected-gallery-first.ax.json`/`.png` and
`corrected-gallery-second.ax.json`/`.png`: each viewer shows the complete
filename over two lines, `1 of 2` / `2 of 2`, a 44 pt Done control, and 44 pt
Previous/Next controls. The screenshots show the corresponding light and dark
images rendered in the viewer. No new obvious clipping, overlap, or inaccessible
control was visible in these states.

The question sheet remains readable in `question-sheet.ax.json`/`.png`, with
the hub identity, question text, answer controls, and Send answers action
visible. This evidence does not qualify the complete UX or the broader
conversation journey.

## Open presentation concern

The message bubble visibly contains the canonical attachment descriptions,
including long filenames and `(attached image ...)` markers. That content is
separate from the thumbnail caption and remains verbose in the captured app.
This is an open content-presentation concern. The evidence does not justify
arbitrary string stripping; any future change needs an explicit content
presentation decision while preserving the canonical message text.

## Artifact identity

`caption-install.json` records source `841220ee2f96e49481faacbdda71ca5d53dfff3d`,
app binary SHA-256
`9fc1a5685c16bd2f8db2ac7d857bd89b1f9c67e3710b2ffbd68dd79d73ebce03`, and
`main.jsbundle` SHA-256
`bd76e0510677862d7986746d2fb1a9f26c6194a5c671e129633778c8f81f28ae`.
