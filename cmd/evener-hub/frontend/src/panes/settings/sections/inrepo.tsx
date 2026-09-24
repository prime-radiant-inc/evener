// Settings -> In-repo config (#11): resolve() a working directory's
// .evener/launch.toml and let the user trust it before the hub will apply it
// (parity-m7-settings.md §11). Appwire: evener/launch/{resolve,trustRepo}.
//
// Directory selection resolves only after explicit confirmation.

import type { LaunchConfigResolved, RepoLaunchConfigStatus } from "@evener/appwire-client";
import { friendlyErrorMessage } from "@evener/appwire-client";
import { useEffect, useRef, useState } from "react";
import { LOCAL_HOST } from "../../../stores/hostRouting";
import { launchConfigStoreForHost } from "../../../stores/launchConfig";
import { Button, FormRow, Loader, PathField } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import { HostScopedSurface } from "./hostScopedSurface";
import styles from "./inrepo.module.css";
import { useHostScopedLoad, useLaunchConfigRefresh } from "./useConnectedEffect";

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

const TRUST_NOTE: Record<string, string> = {
  untrusted: "Untrusted — review and approve below.",
  changed: "Trusted before, but the file has changed. Review and approve again.",
  rejected: "Previously rejected. Trust to apply.",
};

export interface InRepoSectionProps {
  /** Unused - kept so this component's signature matches every other
   * dispatched settings section (see Settings.tsx's SECTION_COMPONENTS map). */
  sectionId: string;
  /** The host whose own in-repo .evener/launch.toml this section resolves and
   * trusts (component 07b). Defaults to the local hub, so a direct render is
   * today's local section. */
  host?: string;
}

export function InRepoSection({ host = LOCAL_HOST }: InRepoSectionProps) {
  // The launch-config gateway for the selected host: the controller's own store
  // for the local hub, a per-host instance (over evener/host/request) for a
  // remote one. Resolved per render; the instance is stable per host. Every
  // read and write below - resolve(), trustRepo(), and the validate/complete
  // helpers the picker uses - goes through it, so a remote selection asks THAT
  // host's filesystem rather than this hub's (component 07b).
  const store = launchConfigStoreForHost(host);
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
      const resolved: LaunchConfigResolved = await store.getState().resolve(trimmed);
      setStatus({ phase: "resolved", repo: resolved.repo });
    } catch (err) {
      setStatus({ phase: "error", message: friendlyErrorMessage(err) });
    }
  }

  // A delayed connection reads the latest committed directory.
  const cwdRef = useRef(cwd);
  useEffect(() => {
    cwdRef.current = cwd;
  }, [cwd]);

  // Defer initial resolution until the client is ready, and re-run when the
  // selected host changes - or comes back after being away - so this pane
  // resolves against THAT host rather than leaving this one's on screen or its
  // own failure standing (the same [store] idiom the other host-scoped sections
  // use; a []-deps effect could reload on neither).
  const { reload } = useHostScopedLoad(host, () => refresh(cwdRef.current), [store]);

  // A change to the SELECTED host's launch config - the working directory's own
  // launch.toml, or a trust decision made elsewhere - must reach this pane
  // rather than leaving a stale resolution on screen. `reload` re-resolves the
  // working directory the user has committed.
  useLaunchConfigRefresh(host, reload);

  function handleCommit(path: string): void {
    setCwd(path);
    void refresh(path);
  }

  async function handleTrust(hash: string): Promise<void> {
    setTrusting(true);
    setTrustError(null);
    try {
      await store.getState().trustRepo(cwd.trim(), hash);
      await refresh(cwd);
    } catch (err) {
      setTrustError(`Trust failed: ${friendlyErrorMessage(err)}`);
    } finally {
      setTrusting(false);
    }
  }

  return (
    <div className={CLASS.root}>
      <h2>In-repo config (.evener/launch.toml)</h2>
      <p className={CLASS.help}>
        Per-project launch config shipped inside the working directory. Hub only applies it after you confirm trust.
      </p>
      <FormRow label="Working dir" htmlFor="inrepo-cwd">
        <PathField
          ariaLabel="Working dir"
          id="inrepo-cwd"
          value={cwd}
          onChange={handleCommit}
          directory={{
            validatePath: (path, kind) => store.getState().validatePath(path, kind),
            createDirectory: (path) => store.getState().createDirectory(path),
          }}
          complete={(prefix, includeFiles) => store.getState().completePaths(prefix, includeFiles)}
          placeholder="Choose a directory"
        />
      </FormRow>
      <div className={CLASS.status} aria-live="polite">
        {status.phase === "empty" && <p className={CLASS.note}>Enter a working directory.</p>}
        {status.phase === "loading" && <Loader label="Loading" />}
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
        No <code>.evener/launch.toml</code> in <code>{cwd}</code>.
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

/** InRepoHostScope is the In-repo config settings section scoped to the
 * settings route's selected host (component 07b): the one shared HostPicker
 * plus InRepoSection, whose resolve/trust calls and path helpers are all that
 * host's own. Local renders today's section byte-for-byte; a remote host's
 * calls go through evener/host/request. */
export function InRepoHostScope({ sectionId }: InRepoSectionProps) {
  return <HostScopedSurface>{(host) => <InRepoSection sectionId={sectionId} host={host} />}</HostScopedSurface>;
}
