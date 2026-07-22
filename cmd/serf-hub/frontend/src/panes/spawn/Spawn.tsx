// The spawn pane: starts a new session. T1 ships a MINIMAL working pane
// (prompt + working-directory + Spawn) on the startThread/preflight seams,
// replacing AppShell's old "not available yet" welcome fallback. T2 fills the
// full form (the 6-chip bar, dir-picker recents/completions, sticky defaults,
// the schema engine, attachments, preflight, ?dir=/?prompt= prefill).
import { useState } from "react";
import { useClient } from "../../shell/clientContext";
import type { PaneProps } from "../../shell/paneRegistry";
import { navigate, paneToURL } from "../../shell/routing";
import { Button, PaneScaffold, PathPicker, Textarea, useToasts } from "../../widgets";
import { startThread } from "./startThread";

// No params yet: /new resolves to spawn with an empty param object. T2 adds
// the ?dir=/?prompt= prefill (read from window.location.search, not params).
export type SpawnPaneParams = Record<string, never>;

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

export default function Spawn(_props: PaneProps<SpawnPaneParams>) {
  const client = useClient();
  const toasts = useToasts();
  const [prompt, setPrompt] = useState("");
  const [cwd, setCwd] = useState("");
  const [busy, setBusy] = useState(false);

  async function listChildren(path: string): Promise<string[]> {
    const prefix = path.endsWith("/") ? path : `${path}/`;
    const resp = await client.request("serf/dirs/complete", { prefix });
    return resp.data;
  }

  async function handleSpawn(): Promise<void> {
    if (busy) return;
    setBusy(true);
    try {
      const { ref } = await startThread(client, { cwd, prompt });
      const url = paneToURL("session", { ref });
      if (url) navigate(url);
    } catch (err) {
      toasts.push("error", `Spawn failed: ${errorMessage(err)}`);
    } finally {
      setBusy(false);
    }
  }

  return (
    <PaneScaffold title="New session">
      <Textarea
        value={prompt}
        onChange={(event) => setPrompt(event.target.value)}
        placeholder="What should the agent work on?"
        aria-label="Prompt"
        autoGrow
      />
      <PathPicker value={cwd} onChange={setCwd} listChildren={listChildren} placeholder="Working directory" />
      <Button variant="primary" onClick={() => void handleSpawn()} disabled={busy}>
        Spawn
      </Button>
    </PaneScaffold>
  );
}
