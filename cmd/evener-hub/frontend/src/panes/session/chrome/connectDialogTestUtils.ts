// Shared test helper for the two model-switch surfaces that can open the
// connect-provider dialog (ModelSwitch.test.tsx, ModelSwitchTrigger.test.tsx).
//
// "Connect another provider" mounts ConnectProviderDialog from its own lazy
// chunk (panes/spawn/connectDialogChunk). The FIRST dynamic import of that
// chunk pays Vite's transform of the dialog and its transitive graph
// (instanceDialogs, oauthDialogs, oauthFlow, CredentialsSection), and on a
// loaded machine that one-time transform can outlast testing-library's default
// 1s async-query budget. Letting the next findBy* absorb it is what made this
// flow flake under parallel load (issue #1369): the failure named the query,
// but what had not happened yet was the import.
//
// So await the import itself - the boundary's own Suspense transition - rather
// than a fixed tick, and do it BEFORE the click so the boundary's lazily-mounted
// import then resolves from an already-evaluated module. That ordering is
// load-bearing, not stylistic: starting this import alongside the boundary's own
// import() makes two concurrent evaluations of the chunk, and Vite's SSR runner
// re-enters the module in that case (observed as "Cannot access
// '__vite_ssr_import_14__' before initialization" thrown from
// ConnectProviderDialog's render under a parallel run).
//
// Callers that expect the import to REJECT (the chunk-boundary retry tests,
// which await the failure panel instead) must not use this: awaiting a rejected
// chunk would reject here.
import { screen } from "@testing-library/react";
import type { UserEvent } from "@testing-library/user-event";
import { loadConnectDialog } from "../../spawn/connectDialogChunk";

export async function openConnectDialog(user: UserEvent): Promise<void> {
  const chunkLoaded = loadConnectDialog();
  await chunkLoaded;
  await user.click(await screen.findByRole("button", { name: "Connect another provider" }));
}
