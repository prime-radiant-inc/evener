// DevHarness is Wave 1's live end-to-end proof: it exercises the whole
// protocol core (AppwireClient, reducer, connection/threads stores) against
// a real hub with no UI investment beyond monospace text. Wave 2 replaces
// this with the real workspace shell.
import { useEffect, useState } from "react";
import { AppwireClient } from "../protocol/client";
import { rpcURLFromLocation } from "../protocol/transport";
import { connectionStore, useConnectionStore } from "../stores/connection";
import { useThreadsStore } from "../stores/threads";
import type { Thread } from "../protocol/types.gen";
import styles from "./DevHarness.module.css";

// bootstrapClient wires a real AppwireClient into connectionStore exactly
// once for the app's lifetime, and kicks off its connect() handshake.
//
// Guarded two ways: MODE === "test" short-circuits unconditionally, because
// jsdom (unlike a real browser-less environment) DOES implement a global
// WebSocket that would otherwise dial the page's own origin for real —
// App.test.tsx (Task 1, owned by another track) renders <App> with no
// FakeClient wired, so this must never construct a real client under
// vitest, not even a fire-and-forgotten one. The client-already-set check
// covers the remaining case (dev/prod): idempotency across a remount (e.g.
// Fast Refresh) so a live connection is never dropped and re-dialed.
function bootstrapClient(): void {
  if (import.meta.env.MODE === "test") return;
  if (connectionStore.getState().client) return;
  const client = new AppwireClient({ url: rpcURLFromLocation(window.location) });
  connectionStore.getState().connect(client);
  void client.connect();
}

export function DevHarness() {
  useEffect(bootstrapClient, []);

  const connectionState = useConnectionStore((s) => s.state);
  const client = useConnectionStore((s) => s.client);
  const threads = useThreadsStore((s) => s.threads);
  const ensureThread = useThreadsStore((s) => s.ensureThread);
  const releaseThread = useThreadsStore((s) => s.releaseThread);

  const [threadList, setThreadList] = useState<Thread[]>([]);
  const [listError, setListError] = useState<string | null>(null);
  const [selectedRef, setSelectedRef] = useState<string | null>(null);

  // Re-lists on every transition into "ready", including a post-reconnect
  // one, so the thread list repopulates without a page reload.
  useEffect(() => {
    if (!client || connectionState !== "ready") return;
    let cancelled = false;
    client
      .request("thread/list", {})
      .then((resp) => {
        // Go's encoding/json renders a nil slice with no `omitempty` as JSON
        // null (app_threadlist.go's `var threads []appwire.Thread` is nil
        // until something matches) — a live hub with zero threads genuinely
        // sends {"data":null}. types.gen.ts's `data: Thread[]` promises a
        // real array because Go's static type gives the codegen nothing
        // else to say; the legacy client already guards this the same way
        // (assets/appwire.js: `resp.data || []`).
        if (!cancelled) setThreadList(resp.data ?? []);
      })
      .catch((err: unknown) => {
        if (!cancelled) setListError(err instanceof Error ? err.message : String(err));
      });
    return () => {
      cancelled = true;
    };
  }, [client, connectionState]);

  // Refcounted per the threads store's contract (see stores/threads.ts):
  // tracks the selected ref only while it's selected, and releases it the
  // moment selection changes or the harness unmounts.
  useEffect(() => {
    if (!selectedRef) return;
    void ensureThread(selectedRef);
    return () => releaseThread(selectedRef);
  }, [selectedRef, ensureThread, releaseThread]);

  const selectedModel = selectedRef ? threads.get(selectedRef) : undefined;

  return (
    <div className={styles.harness}>
      <p>connection: {connectionState}</p>
      {listError && <p>thread/list error: {listError}</p>}
      <ul>
        {threadList.map((t) => (
          <li key={t.serf.ref}>
            <button type="button" onClick={() => setSelectedRef(t.serf.ref)}>
              {t.serf.ref} — {t.preview}
            </button>
          </li>
        ))}
      </ul>
      {selectedRef && <pre>{selectedModel ? JSON.stringify(selectedModel, null, 2) : "loading…"}</pre>}
    </div>
  );
}
