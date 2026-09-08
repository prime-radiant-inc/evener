# iPad final artifact qualification

Device: iPad simulator `6462A0FC-A190-4F08-A0CC-9B7E6C792AEC`. Artifact: Release `Evener.app` from commit `7944778e0`.

## Observed

- Pre-install bundle hashes matched the supplied artifact: app binary `964c2bb36433c222362ad6ebc923229b441d613d9475abd8abe92e2930a8ab95`; JavaScript bundle `af754632eef5f34e9b231e3cd9c30282d5aeb038003479e921993c42629d7c48`.
- Installation succeeded through XcodeBuildMCP on the specified iPad only.
- A clean launch and a terminate/relaunch cycle both showed the empty `Evener · Hubs` screen with `Saved hubs`, `Add hub`, four empty inputs, and disabled review/save actions.
- Post-install hashes matched the pre-install hashes exactly.
- A direct `idb` tap on the observed Hub name field at simulator point `(160,292)` revealed the iPad software keyboard; a direct tap on the keyboard dismissal control returned to the unchanged empty form. No text was entered.

Private captures: `/tmp/evener-ipad-final-current.png` and `/tmp/evener-ipad-cold-relaunch.png`.

## Not qualified

The direct accessibility tree exposed the simulator’s `DockFolderViewService` rather than the app controls. Keyboard reveal/dismiss is qualified by the direct simulator interaction above; largest text, landscape, VoiceOver, pairing, and reader cold-restore remain unqualified on iPad. The final device state was left at empty Hubs, normal text, portrait.
