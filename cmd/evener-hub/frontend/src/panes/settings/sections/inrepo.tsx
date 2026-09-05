// Settings -> In-repo config (#11): resolve() a working directory's
// .evener/launch.toml and let the user trust it before the hub will apply it
// (parity-m7-settings.md §11). Appwire: evener/launch/{resolve,trustRepo}.
//
// Directory selection resolves only after explicit confirmation.
import { useEffect, useRef, useState } from "react";
import { friendlyErrorMessage } from "../../../protocol/errors";
import type { LaunchConfigResolved, RepoLaunchConfigStatus } from "../../../protocol/types.gen";
import { directoryActions, extensionsStore } from "../../../stores/extensions";
import { launchConfigStore } from "../../../stores/launchConfig";
import { Button, FormRow, Loader, PathField } from "../../../widgets";
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
      setStatus({ phase: "error", message: friendlyErrorMessage(err) });
    }
  }

  // A delayed connection reads the latest committed directory.
  const cwdRef = useRef(cwd);
  useEffect(() => {
    cwdRef.current = cwd;
  }, [cwd]);

  // Defer initial resolution until the client is ready.
  useConnectedEffect(() => refresh(cwdRef.current), []);

  function handleCommit(path: string): void {
    setCwd(path);
    void refresh(path);
  }

  async function handleTrust(hash: string): Promise<void> {
    setTrusting(true);
    setTrustError(null);
    try {
      await launchConfigStore.getState().trustRepo(cwd.trim(), hash);
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
          id="inrepo-cwd"
          value={cwd}
          onChange={handleCommit}
          directory={directoryActions}
          complete={(prefix, includeFiles) => extensionsStore.getState().completePaths(prefix, includeFiles)}
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
