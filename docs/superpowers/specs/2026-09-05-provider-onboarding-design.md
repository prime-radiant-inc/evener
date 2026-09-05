# In-place provider onboarding

Jesse approved the in-place mockup and requested implementation, automated and live testing, and a PR in an isolated worktree.

The normal Start an agent surface is the onboarding surface. When no visible provider has usable credential configuration, show an inline Connect provider action without automatically opening a dialog. This is state-driven on every visit, not a first-run flag. An empty welcome surface routes to the composer when setup is needed; existing sessions are never displaced. Preserve the prompt and working directory through setup.

Reuse the existing API-key and OAuth editors. Offer provider instances from the registry, including configured instances that need repair, with supported auth modes. A successful save is distinct from a successful explicit connection test. Failures remain recoverable; do not erase credentials or interpret failed status requests as absent credentials. Keyless and optional-auth providers do not require credentials. Refresh availability and the model catalog after credential changes, including notifications from other clients and reconnections.

Use the existing working-directory picker and create-directory confirmation for the first project. Keep normal configured defaults and model selection. Do not add a wizard, a first-run flag, a second project registry, new provider protocols, or backward compatibility. Match existing widgets, pane chrome, and mobile layout.

Verification: behavioral frontend tests at the AppWire boundary; canonical frontend and browser gates; merge gate and vet; an isolated running hub with fresh user state for no-credential, save/test/recovery, directory creation, and session-start browser exercise. Distinguish a scripted provider exercise from an actual external-provider request in the final evidence.
