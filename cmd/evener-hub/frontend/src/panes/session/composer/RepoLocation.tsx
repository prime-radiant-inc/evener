// RepoLocation: the session composer's one-line origin strip - the working
// directory this session runs in, and the git branch checked out there. It sits
// directly under the prompt card (Composer.tsx renders it below the form), so
// "where is this agent working, and on what branch" is answerable without
// opening Session details.
//
// The branch is a link to its forge repo page when the origin remote is one the
// web UI recognizes (GitHub/GitLab/Bitbucket), carrying that forge's mark;
// otherwise it is plain text. The cwd is never a link - only the branch is.
//
// Facts come from the hub's evener/git/head method keyed on the session's cwd,
// resolved once per cwd and cached by state, failing soft to "no branch shown"
// (see shell/gitLocation.ts). This is display metadata only: nothing here is
// sent anywhere, and a slow or failed lookup simply renders the cwd alone.
import { useEffect, useState } from "react";
import { useClient } from "../../../shell/clientContext";
import { resolveGitLocation } from "../../../shell/gitLocation";
import { requireClass } from "../../../widgets/internal/requireClass";
import { ForgeMark } from "./ForgeMark";
import styles from "./repoLocation.module.css";
import { FORGE_LABELS, parseRepoRemote } from "./repoRemote";

export interface RepoLocationProps {
  cwd: string;
}

const CLASS = {
  line: requireClass(styles.line, "repoLocation.module.css", "line"),
  path: requireClass(styles.path, "repoLocation.module.css", "path"),
  branch: requireClass(styles.branch, "repoLocation.module.css", "branch"),
  link: requireClass(styles.link, "repoLocation.module.css", "link"),
};

// The last resolved lookup, tagged with the cwd it belongs to: a response for a
// cwd the composer has since left must never be displayed against the new one.
interface ResolvedLocation {
  cwd: string;
  branch: string;
  originUrl: string;
}

export function RepoLocation({ cwd }: RepoLocationProps) {
  const client = useClient();
  const [resolved, setResolved] = useState<ResolvedLocation | null>(null);

  useEffect(() => {
    if (cwd.trim() === "") return undefined;
    let active = true;
    void resolveGitLocation(client, cwd).then((location) => {
      if (active) setResolved({ cwd, ...location });
    });
    return () => {
      active = false;
    };
  }, [client, cwd]);

  if (cwd.trim() === "") return null;

  const current = resolved !== null && resolved.cwd === cwd ? resolved : null;
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
            <span className={CLASS.branch} data-testid="composer-repo-branch">
              {branch}
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
