Extract effective transcript-display layer resolution and transition publication from the web store into a small browser-side module. Viewport routing, capture/restore, announcements, remount behavior, and the public Zustand store remain unchanged. This prepares the host boundary for SDK adoption.

Validation: 30 existing transcript-display tests, full `make test-web`, independent review, and RoboRev branch review2682 all passed. After #2049 landed, the restacked commit has an identical full tree to the reviewed head and an unchanged range-diff.
