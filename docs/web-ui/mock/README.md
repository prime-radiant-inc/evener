# Mobile fluency mock (v8-A, final revision)

`evener-fluent-prototype-v8a.html` is the interactive design reference for the
mobile fluency pass — the mock Jesse reviewed through twenty-one published
revisions and approved as the direction ("not all of the interaction is working
quite right, but you've got the idea"). It is the visual reference for the
phone and tablet work described in
[design-system.md §12](../design-system.md).

It is one self-contained HTML file: inline CSS and JavaScript, no network
dependencies, dark theme only. Open it directly in a browser (`open
evener-fluent-prototype-v8a.html`). The device toggle in its legend switches
between Phone 390×844, Tablet ↕ 768×1024, Tablet ↔ 1024×768, and the Duo
interior; inside each view the header, composer, and sidebar interactions work.

The live copy is also served at
`https://artifacts.trout-rigel.ts.net/p/evener-fluent-prototype-v8a/`. The
hosting page wraps the artifact in a sandboxed iframe that blocks DOM
inspection, so measure or automate against this committed file, not the hosted
copy.

## How to read it

The mock's anatomy, hierarchy, and tone are the contract — not its pixels.
Where the mock and [design-system.md](../design-system.md) disagree, the style
guide wins; it records every ruling made after the mock was frozen:

- The radius ladder supersedes the mock's pill composer and circular send.
- The New-session floating action at the list's bottom-right postdates the mock.
- The tablet board column reads the one `sidebarWidth` preference (default
  280px), not the mock's viewport-proportional width.
- The mock auto-collapses the tablet board on narrow viewports; the shipped
  contract keeps the column persistent across the 768–899px band.
- Effort and model are set on the settings surface and shown as facts in the
  Overview sheet, never as chat chrome.
