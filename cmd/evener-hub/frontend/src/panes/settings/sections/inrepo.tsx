import type { LaunchConfigResolved, RepoLaunchConfigStatus } from "@evener/appwire-client";
import { friendlyErrorMessage } from "@evener/appwire-client";
import { useEffect, useRef, useState } from "react";
import { launchConfigStoreForHost } from "../../../stores/launchConfig";
import { Button, CodeBlock, InlineNotice, Loader } from "../../../widgets";
import { requireClass } from "../../../widgets/internal/requireClass";
import styles from "./inrepo.module.css";
import { useHostScopedLoad, useLaunchConfigRefresh } from "./useConnectedEffect";

const CLASS = {
  root: requireClass(styles.root, "inrepo.module.css", "root"),
  help: requireClass(styles.help, "inrepo.module.css", "help"),
  status: requireClass(styles.status, "inrepo.module.css", "status"),
  note: requireClass(styles.note, "inrepo.module.css", "note"),
  hash: requireClass(styles.hash, "inrepo.module.css", "hash"),
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

export function RepositoryConfigSection({ host, cwd }: { host: string; cwd: string }) {
  return <RepositoryConfigContent key={JSON.stringify([host, cwd])} host={host} cwd={cwd} />;
}

function RepositoryConfigContent({ host, cwd }: { host: string; cwd: string }) {
  const store = launchConfigStoreForHost(host);
  const [status, setStatus] = useState<Status>({ phase: "empty" });
  const [trustError, setTrustError] = useState<string | null>(null);
  const [trusting, setTrusting] = useState(false);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  async function refresh(target: string, isCancelled: () => boolean): Promise<void> {
    setTrustError(null);
    const trimmed = target.trim();
    if (!trimmed) {
      setStatus({ phase: "empty" });
      return;
    }
    setStatus({ phase: "loading" });
    try {
      const resolved: LaunchConfigResolved = await store.getState().resolve(trimmed);
      if (isCancelled()) return;
      setStatus({ phase: "resolved", repo: resolved.repo });
    } catch (err) {
      if (isCancelled()) return;
      setStatus({ phase: "error", message: friendlyErrorMessage(err) });
    }
  }

  const { reload } = useHostScopedLoad(host, (_blank, isCancelled) => refresh(cwd, isCancelled), [store, cwd]);

  // A change to the SELECTED host's launch config - the working directory's own
  // launch.toml, or a trust decision made elsewhere - must reach this pane
  // rather than leaving a stale resolution on screen. `reload` re-resolves the
  // working directory the user has committed.
  useLaunchConfigRefresh(host, reload);

  async function handleTrust(hash: string): Promise<void> {
    setTrusting(true);
    setTrustError(null);
    try {
      await store.getState().trustRepo(cwd.trim(), hash);
      if (mounted.current) reload();
    } catch (err) {
      if (!mounted.current) return;
      setTrustError(`Trust failed: ${friendlyErrorMessage(err)}`);
    } finally {
      if (mounted.current) setTrusting(false);
    }
  }

  return (
    <div className={CLASS.root}>
      <h3>Repository settings</h3>
      <p className={CLASS.help}>
        Shared through <code>.evener/launch.toml</code>. Review this file before allowing it to configure sessions.
      </p>
      <div
        className={CLASS.status}
        aria-live="polite"
        data-testid="inrepo-status"
        data-state={status.phase === "resolved" ? (status.repo?.trust ?? "absent") : status.phase}
      >
        {status.phase === "empty" && <p className={CLASS.note}>Enter a working directory.</p>}
        {status.phase === "loading" && <Loader label="Loading" />}
        {status.phase === "error" && (
          <InlineNotice
            tone="danger"
            live="off"
            action={<Button onClick={() => reload()}>Retry repository review</Button>}
          >
            Failed to load: {status.message}
          </InlineNotice>
        )}
        {status.phase === "resolved" && <ResolvedStatus repo={status.repo} trusting={trusting} onTrust={handleTrust} />}
        {trustError && (
          <InlineNotice tone="danger" live="off" data-reason="trust-failed">
            {trustError}
          </InlineNotice>
        )}
      </div>
    </div>
  );
}

function ResolvedStatus({
  repo,
  trusting,
  onTrust,
}: {
  repo: RepoLaunchConfigStatus | undefined;
  trusting: boolean;
  onTrust: (hash: string) => void;
}) {
  if (!repo || repo.trust === "absent") {
    return (
      <p className={CLASS.note}>
        No <code>.evener/launch.toml</code> in this directory.
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
      {repo.preview && <CodeBlock text={repo.preview} />}
      {repo.trust !== "trusted" && (
        <Button type="button" disabled={trusting} onClick={() => repo.hash && onTrust(repo.hash)}>
          Trust this file
        </Button>
      )}
    </>
  );
}
