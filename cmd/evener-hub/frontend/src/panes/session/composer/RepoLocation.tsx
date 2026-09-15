// RepoLocation: the session composer's one-line origin strip - the working
// directory this session runs in, and the repository and branch checked out
// there. It sits directly under the prompt card (Composer.tsx renders it below
// the form), so "where is this agent working, and on what" is answerable
// without opening Session details.
//
// When the origin remote is a forge the web UI recognizes
// (GitHub/GitLab/Bitbucket), the repository is named the way a forge does -
// `owner/repo#branch` - and links to the forge's repo page, carrying that
// forge's mark. The cwd stays beside it because it is the one thing the repo
// reference cannot say; a worktree session's cwd is not its project root, and a
// repo can be cloned anywhere. Without a recognized forge the branch alone is
// shown, as plain text. The cwd is never a link.
//
// Facts come from the hub's evener/git/head method keyed on the session's cwd,
// resolved once per cwd and cached by state, failing soft to "no branch shown"
// (see shell/gitLocation.ts). This is display metadata only: nothing here is
// sent anywhere, and a slow or failed lookup simply renders the cwd alone.
//
// The lookup runs only for a session whose cwd is on THIS hub's filesystem
// (the `local` prop). A source-backed session's cwd belongs to another host, so
// resolving it here could show an unrelated local repository that happens to
// share the path; such a session still shows its working dir, just no branch.
import { useEffect, useState } from "react";
import type { AppwireClientLike } from "../../../protocol/clientLike";
import { useClient } from "../../../shell/clientContext";
import { resolveGitLocation } from "../../../shell/gitLocation";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ForgeMark } from "./ForgeMark";
import styles from "./repoLocation.module.css";
import { FORGE_LABELS, parseRepoRemote } from "./repoRemote";

export interface RepoLocationProps {
  cwd: string;
  // Whether cwd is on this hub's own filesystem. A source-backed session's cwd
  // belongs to another host, so resolving its branch here could surface an
  // unrelated local repository that merely shares the path; those sessions get
  // the working dir alone.
  local: boolean;
}

const CLASS = {
  line: requireClass(styles.line, "repoLocation.module.css", "line"),
  path: requireClass(styles.path, "repoLocation.module.css", "path"),
  branch: requireClass(styles.branch, "repoLocation.module.css", "branch"),
  ref: requireClass(styles.ref, "repoLocation.module.css", "ref"),
  link: requireClass(styles.link, "repoLocation.module.css", "link"),
};

// The last resolved lookup, tagged with the cwd it belongs to AND the client
// that answered: a response for a cwd the composer has since left, or one the
// previous hub answered before this pane reconnected, must never be displayed
// against the current cwd or hub.
interface ResolvedLocation {
  client: AppwireClientLike;
  cwd: string;
  branch: string;
  originUrl: string;
}

export function RepoLocation({ cwd, local }: RepoLocationProps) {
  const client = useClient();
  const [resolved, setResolved] = useState<ResolvedLocation | null>(null);

  useEffect(() => {
    if (!local || cwd.trim() === "") return undefined;
    let active = true;
    void resolveGitLocation(client, cwd).then((location) => {
      if (active) setResolved({ client, cwd, ...location });
    });
    return () => {
      active = false;
    };
  }, [client, cwd, local]);

  if (cwd.trim() === "") return null;

  const current = local && resolved !== null && resolved.client === client && resolved.cwd === cwd ? resolved : null;
  const branch = current?.branch ?? "";
  const remote = current !== null && current.originUrl !== "" ? parseRepoRemote(current.originUrl) : null;

  return (
    <div className={CLASS.line} data-testid="composer-repo-location">
      <span className={CLASS.path} title={cwd} data-testid="composer-repo-path">
        {cwd}
      </span>
      {branch !== "" &&
        (remote !== null ? (
          <a
            className={CLASS.link}
            href={remote.repoUrl}
            target="_blank"
            rel="noopener noreferrer"
            title={`Open ${remote.owner}/${remote.repo} on ${FORGE_LABELS[remote.forge]}`}
            data-testid="composer-repo-link"
          >
            <ForgeMark forge={remote.forge} />
            <span
              className={CLASS.ref}
              data-testid="composer-repo-ref"
              title={`${remote.owner}/${remote.repo}#${branch}`}
            >
              {`${remote.owner}/${remote.repo}#${branch}`}
            </span>
          </a>
        ) : (
          <span className={CLASS.branch} title={branch} data-testid="composer-repo-branch">
            {branch}
          </span>
        ))}
    </div>
  );
}
