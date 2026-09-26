// The guided connect flow itself is ProviderConnection; this module is the
// lazy-chunk entry point every surface opens it through (see
// ConnectProviderDialogBoundary.tsx) and owns the one handoff the flow makes
// out of itself. Existing connections are managed in the settings pane's
// credentials section - its instance rows and their sheet are the real editor -
// so an affordance that offers that hands the user there instead of standing up
// a second, narrower copy of it inside this dialog.
import { navigate, paneToURL } from "../../../../shell/routing";
import { ProviderConnection } from "./ProviderConnection";

export interface ConnectProviderDialogProps {
  onClose(): void;
  onConnected(name?: string): void;
}

export function ConnectProviderDialog(props: ConnectProviderDialogProps) {
  // Close before navigating: whichever surface mounted this dialog (the spawn
  // pane, the model-switch trigger, the credentials section) keeps it mounted
  // on top of the pane it is handing the user to, so leaving it open would put
  // a dialog over the settings pane the user was just sent to.
  function openProviderSettings(): void {
    props.onClose();
    const url = paneToURL("settings", { section: "credentials" });
    if (url) navigate(url);
  }
  return <ProviderConnection {...props} onOpenSettings={openProviderSettings} />;
}
