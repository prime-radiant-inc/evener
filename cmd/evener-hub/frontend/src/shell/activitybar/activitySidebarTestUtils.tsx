// The sidebar as production mounts it: under the shell's client context. A
// test connects its client through connectionStore (the stores' seam), and the
// sidebar's leaf tabs read the client through that context (About's branch
// lookup); this wrapper bridges the two so a harness that hydrates through
// the stores can mount the sidebar without wiring a provider of its own. The
// composer's own test harness established the same bridge
// (panes/session/composer/composerTestUtils.tsx's Composer).
import type { ReactElement } from "react";
import { connectionStore } from "../../stores/connection";
import { ClientProvider } from "../clientContext";
import { ActivitySidebar as ActivitySidebarView } from "./ActivitySidebar";

export function ActivitySidebar(props: React.ComponentProps<typeof ActivitySidebarView>): ReactElement {
  const client = connectionStore.getState().client;
  if (!client) throw new Error("ActivitySidebar test rendered without a connected client");
  return (
    <ClientProvider client={client}>
      <ActivitySidebarView {...props} />
    </ClientProvider>
  );
}
