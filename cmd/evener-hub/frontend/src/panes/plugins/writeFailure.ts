// A hub-busy refusal keeps its shared copy; every other failure names the action.
import { errorText } from "@evener/appwire-client";
import { HUB_WRITE_BUSY, isHubWriteBusy } from "@evener/appwire-client/state/extensions";

export function writeFailureText(err: unknown, label: string): string {
  return isHubWriteBusy(err) ? HUB_WRITE_BUSY : `${label}: ${errorText(err)}`;
}
