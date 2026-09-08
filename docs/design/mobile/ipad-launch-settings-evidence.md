# iPad launch settings evidence — 8 September 2026

The [receipt](assets/ipad-launch-settings-receipt.json) records an actual native
project-settings journey against a disposable Evener v4 hub. Root operated the
iPad Pro 11-inch (M5), iOS 26.5 simulator using native source `d20229b75`,
process 42478. Independent packaged-SDK reads and the hub's AppWire trace
corroborate the UI observations.

The editor rejected a nonexistent plugin directory. Root then staged a valid
plugin directory, an MCP config path, an inline MCP command with one argument,
an environment variable containing an equals sign, and a catalog-selected
fallback model. The project layer remained empty before Save. After Save,
`getLayer` and `resolve` returned all five exact values; the global model default
was unchanged. MCP command validation and serialization were exercised;
no MCP server was executed.

With a new fallback edit staged, a second SDK client added an environment
variable while preserving the other project settings. Native Save refused the
stale baseline and disabled further saves until Reload. The independent
readback matched the entire external layer. After confirming Reload, native
Save persisted explicit no-fallbacks as `[]`; a later save of Use inherited value
removed that field and preserved every other project value. Reopening the
editor showed the correct selection in both cases.

The trace contains three successful native `evener/launch/setLayer` requests,
IDs 49, 69 and 75 on `conn-10`, all for layer `project`. Neither stale-save
attempt sent a native write. This is evidence for the stale-baseline guard;
it does not qualify lost-reply or interrupted-write recovery.

The first artifact displayed the conflict twice. Commit `2e37bca74` renders one
error or conflict notice. Root repeated the second-client conflict using the
rebuilt Release artifact, process 53818: the UI showed exactly one notice and
the independent readback still matched the external edit. The receipt records
the source-file hash and build inputs, including the then-uncommitted branding
configuration. Existing settings contracts passed 17 tests; the full native
gate passed 678 tests across 74 files and TypeScript.

Root removed the owned profile and verified it and every draft table remained
empty after cold launch, process 94994. An earlier install interrupted removal
before completion; repeating ordinary removal completed successfully, so that
observation required no product change. The provider, hub, seed daemon and
parent process exited; root independently checked their PIDs, owned executable
paths, listeners and credential/log removal. Private traces and reads remain
available with computed hashes in the receipt.

This qualifies the stated project-layer operations on one iPad simulator.
Global editing, two-hub settings drafts, directory completion races, connection
faults, VoiceOver, Dynamic Type, physical devices, performance and signed app
updates remain open.
