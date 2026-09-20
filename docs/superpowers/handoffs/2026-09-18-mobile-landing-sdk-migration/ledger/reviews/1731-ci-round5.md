# CI on #1731 @78ccc026c (run 35306881505): web-test red, 3 tests in src/panes/session/composer/Composer.integration.test.tsx
- "a pending ask hides and inerts the composer's input row, and the dock is no longer the composer's child": expected <div ...> to be null
- "the composer un-hides once the pending ask resolves through the normal send path": same
- "resolving the pending ask announces the composer's restoration via this component's own aria-live region": expected +0 to be 1
All three build a pending ask without askPending on the model (the same fixture class as the 13 fixed in round 5, missed because they live in the Composer integration suite, not the ask-dock suite).
