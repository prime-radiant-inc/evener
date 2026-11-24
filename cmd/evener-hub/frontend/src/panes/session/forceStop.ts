// Force-stop recovery as one sequence. A stop failure is rethrown (the dialog
// stays open on it); a refresh failure is reported and swallowed.
import { sessionActionError } from "@evener/appwire-client";
import { threadsStore } from "../../stores/threads";

export async function forceStopAndRefresh(sessionRef: string, report: (message: string) => void): Promise<void> {
  try {
    await threadsStore.getState().forceStop(sessionRef);
  } catch (err) {
    report(sessionActionError("Couldn't force shutdown session", err));
    throw err;
  }
  try {
    await threadsStore.getState().refreshThread(sessionRef);
  } catch (err) {
    report(sessionActionError("Session stopped; couldn't refresh its view", err));
  }
}
