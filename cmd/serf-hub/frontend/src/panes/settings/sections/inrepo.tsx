// Settings -> In-repo config (#11): resolve() a working directory's
// .serf/launch.toml and let the user trust it before the hub will apply it
// (parity-m7-settings.md §11). Appwire: serf/launch/{resolve,trustRepo}.
//
// The cwd field re-resolves on blur/Enter, not per keystroke - the Input
// widget's typed props have no onBlur/onKeyDown (only onChange, matching
// every other controlled widget in this set), so this wraps it in a plain
// div: React's onBlur/onKeyDown both bubble from a focused descendant (blur
// via the native focusout event since React 17), so catching them one level
// up needs no widget API change. Unlike every other free-text path input in
// this settings cluster, this field intentionally gets no directory-picker
// assist (parity floor's own note - matches the legacy exactly).
import { type KeyboardEvent, useState } from "react";
import type { LaunchConfigResolved, RepoLaunchConfigStatus } from "../../../protocol/types.gen";
import { launchConfigStore } from "../../../stores/launchConfig";
import { Button, FormRow, Input } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./inrepo.module.css";
import { useConnectedEffect } from "./useConnectedEffect";

const CLASS = {
  root: requireClass(styles.root, "inrepo.module.css", "root"),
  help: requireClass(styles.help, "inrepo.module.css", "help"),
  status: requireClass(styles.status, "inrepo.module.css", "status"),
  note: requireClass(styles.note, "inrepo.module.css", "note"),
  hash: requireClass(styles.hash, "inrepo.module.css", "hash"),
  preview: requireClass(styles.preview, "inrepo.module.css", "preview"),
  error: requireClass(styles.error, "inrepo.module.css", "error"),
};

type Status =
  | { phase: "empty" }
  | { phase: "loading" }
  | { phase: "error"; message: string }
  | { phase: "resolved"; repo: RepoLaunchConfigStatus | undefined };

function errorMessage(err: unknown): string {
  return err instanceof Error ? err.message : String(err);
}

const TRUST_NOTE: Record<string, string> = {
  untrusted: "Untrusted — review and approve below.",
  changed: "Trusted before, but the file has changed. Review and approve again.",
  rejected: "Previously rejected. Trust to apply.",
};

export interface InRepoSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
}

export function InRepoSection(_props: InRepoSectionProps) {
  const [cwd, setCwd] = useState(() => localStorage.getItem("lastCwd") ?? "");
  const [status, setStatus] = useState<Status>({ phase: "empty" });
  const [trustError, setTrustError] = useState<string | null>(null);
  const [trusting, setTrusting] = useState(false);

  async function refresh(target: string): Promise<void> {
    setTrustError(null);
    const trimmed = target.trim();
    if (!trimmed) {
      setStatus({ phase: "empty" });
      return;
    }
    setStatus({ phase: "loading" });
    try {
      const resolved: LaunchConfigResolved = await launchConfigStore.getState().resolve(trimmed);
      setStatus({ phase: "resolved", repo: resolved.repo });
    } catch (err) {
      setStatus({ phase: "error", message: errorMessage(err) });
    }
  }

  // Runs once, on mount, with the localStorage-seeded value - later
  // re-resolves are driven by blur/Enter (handleCommit below), never by
  // this effect re-running, so `cwd`/`refresh` are deliberately not deps.
  // useConnectedEffect (not a bare useEffect): a direct deep link to
  // /settings/inrepo can mount this section before AppShell's own connect()
  // handshake finishes; without this, the initial resolve() would fail with
  // "no client connected" and (unlike a later blur/Enter commit, which
  // would by then succeed) there is no automatic retry - see that hook's
  // own doc comment.
  useConnectedEffect(() => refresh(cwd), []);

  function handleCommit(): void {
    void refresh(cwd);
  }

  function handleKeyDown(event: KeyboardEvent<HTMLDivElement>): void {
    if (event.key === "Enter") (document.activeElement as HTMLElement | null)?.blur();
  }

  async function handleTrust(hash: string): Promise<void> {
    setTrusting(true);
    setTrustError(null);
    try {
      await launchConfigStore.getState().trustRepo(cwd.trim(), hash);
      await refresh(cwd);
    } catch (err) {
      setTrustError(`Trust failed: ${errorMessage(err)}`);
    } finally {
      setTrusting(false);
    }
  }

  return (
    <div className={CLASS.root}>
      <h2>In-repo config (.serf/launch.toml)</h2>
      <p className={CLASS.help}>
        Per-project launch config shipped inside the working directory. Hub only applies it after you confirm trust.
      </p>
      {/* biome-ignore lint/a11y/noStaticElementInteractions: catches blur/Enter bubbling up from the Input below (see this file's own top comment) - not itself an interactive element */}
      <div onBlur={handleCommit} onKeyDown={handleKeyDown}>
        <FormRow label="Working dir" htmlFor="inrepo-cwd">
          <Input id="inrepo-cwd" value={cwd} onChange={(e) => setCwd(e.target.value)} placeholder="/path/to/project" />
        </FormRow>
      </div>
      <div className={CLASS.status} aria-live="polite">
        {status.phase === "empty" && <p className={CLASS.note}>Enter a working directory.</p>}
        {status.phase === "loading" && <p className={CLASS.note}>Loading…</p>}
        {status.phase === "error" && <p className={CLASS.error}>Failed to load: {status.message}</p>}
        {status.phase === "resolved" && (
          <ResolvedStatus cwd={cwd.trim()} repo={status.repo} trusting={trusting} onTrust={handleTrust} />
        )}
        {trustError && <p className={CLASS.error}>{trustError}</p>}
      </div>
    </div>
  );
}

function ResolvedStatus({
  cwd,
  repo,
  trusting,
  onTrust,
}: {
  cwd: string;
  repo: RepoLaunchConfigStatus | undefined;
  trusting: boolean;
  onTrust: (hash: string) => void;
}) {
  if (!repo || repo.trust === "absent") {
    return (
      <p className={CLASS.note}>
        No <code>.serf/launch.toml</code> in <code>{cwd}</code>.
      </p>
    );
  }

  return (
    <>
      {repo.trust === "trusted" ? (
        <p className={CLASS.note}>
          Trusted. Hash <span className={CLASS.hash}>{repo.hash}</span>.
        </p>
      ) : (
        <p className={CLASS.note}>{TRUST_NOTE[repo.trust] ?? repo.trust}</p>
      )}
      {repo.preview && <pre className={CLASS.preview}>{repo.preview}</pre>}
      {repo.trust !== "trusted" && (
        <Button type="button" disabled={trusting} onClick={() => repo.hash && onTrust(repo.hash)}>
          Trust this file
        </Button>
      )}
    </>
  );
}
