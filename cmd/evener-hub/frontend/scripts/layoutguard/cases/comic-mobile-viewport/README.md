# Comic mobile viewport geometry

Run from `cmd/evener-hub/frontend`:

```sh
node scripts/layoutguard/run.mjs comic-mobile-viewport
```

The ordinary `layoutguard` command discovers this case, so it runs in
`make test-web-browser`. It mounts real `ComicPicker` and `ComicSession`
components with static store data and a never-connected real client, not mocked
network E2E. Empty and 50-row pickers run at 320, 390 and 1360px. Native CDP
wheel/touch gestures must expose the lower row and pagination to hit-testing;
a native click must select the exact lower-row ref. No programmatic scrolling
is used to reach the targets.

Headless desktop Chrome resolves `100vh` and `100dvh` equally, even with mobile
emulation. The case records those native dimensions, then resolves viewport-unit
tokens in the actual Comic stylesheet against a **modeled** 760px large viewport
and 620px dynamic viewport. The browser applies the resulting rules through its
real cascade. The root, composer, editor and submit control must fit within 620px,
with positive reader space and unchanged literal source slices. Removing the
model also checks native resolution. This is not physical mobile browser chrome,
software keyboard, Safari or touch-inertia qualification.

Before the repair, native wheel and touch could not scroll the mobile picker,
and the modeled session/composer ended at 760px. With dynamic minimum height and
a bounded picker scrollport, the same case passes at all three widths.
