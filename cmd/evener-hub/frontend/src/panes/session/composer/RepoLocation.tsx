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
// Both truncatable spans middle-truncate under pressure, not end-truncate: the
// path's and the reference's ENDINGS - the project directory, the branch - are
// the parts an end-truncation kept hiding, and the parts a person scanning the
// line needs. The split is DOM the CSS acts on (the .head span yields and
// ellipsis-clamps; the .tail span never shrinks), so an unpressured line renders
// the same unbroken text it always did.
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

import type { AppwireClientLike } from "@evener/appwire-client";
import { useEffect, useState } from "react";
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
  head: requireClass(styles.head, "repoLocation.module.css", "head"),
  tail: requireClass(styles.tail, "repoLocation.module.css", "tail"),
};

// --- Middle truncation ------------------------------------------------------
//
// A middle-truncating span is a head/tail pair, not one text node: the head
// ellipsis-clamps under pressure while the tail never shrinks, so the cut lands
// wherever the width puts it and the string's ending stays on screen (the same
// grammar ToolRow.tsx's collapsed summary and toolcallitem.module.css's
// .clampedHead/.clampedTail pair established).

/** The head's share of the default cut, in code points - ToolRow.tsx's own
 * middleTruncationCut uses the same 60%. */
const HEAD_SHARE = 0.6;

/** Splits `text` at `cut` (a code-point index) into its head and tail halves,
 * or returns null when the cut would empty either side - a one-sided split is
 * not a middle truncation. The cut first walks left off boundary whitespace:
 * head and tail render as separate spans, and CSS white-space processing drops
 * a space at a span edge, so every space must stay interior to one half. */
function splitMiddle(text: string, cut: number): [string, string] | null {
  const chars = Array.from(text);
  let position = cut;
  while (position > 0 && position < chars.length) {
    if (/\s/.test(chars[position - 1] ?? "") || /\s/.test(chars[position] ?? "")) position -= 1;
    else break;
  }
  if (position <= 0 || position >= chars.length) return null;
  return [chars.slice(0, position).join(""), chars.slice(position).join("")];
}

/** The default cut: ~60% of the code points to the head. */
function proportionalCut(text: string): number {
  return Math.ceil(Array.from(text).length * HEAD_SHARE);
}

/** The repo reference's cut: at its `#`, so the branch marker and everything
 * after it ride the never-shrinking tail and the branch cannot ellipsize away.
 * -1 (no marker) leaves the reference to its fallback: one head span. */
function branchMarkerCut(text: string): number {
  return Array.from(text).indexOf("#");
}

function MiddleTruncated({ text, cut }: { text: string; cut: number }) {
  const split = splitMiddle(text, cut);
  if (split === null) return <span className={CLASS.head}>{text}</span>;
  const [head, tail] = split;
  return (
    <>
      <span className={CLASS.head}>{head}</span>
      <span className={CLASS.tail}>{tail}</span>
    </>
  );
}

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
  const reference = remote !== null ? `${remote.owner}/${remote.repo}#${branch}` : "";

  return (
    <div className={CLASS.line} data-testid="composer-repo-location">
      <span className={CLASS.path} title={cwd} data-testid="composer-repo-path">
        <MiddleTruncated text={cwd} cut={proportionalCut(cwd)} />
      </span>
      {branch !== "" &&
        (remote !== null ? (
          <a
            className={CLASS.link}
            href={remote.repoUrl}
            target="_blank"
            rel="noopener noreferrer"
            // The full reference lives on the anchor, not on the span inside it:
            // a title on the span would shadow this one for any hover over the
            // visible text, which is most of the link, and the tooltip would
            // then only restate what is already on screen. This way the hover
            // says both what the link does and the whole reference, which
            // matters when the line is narrow enough to ellipsize it.
            title={`Open ${reference} on ${FORGE_LABELS[remote.forge]}`}
            data-testid="composer-repo-link"
          >
            <ForgeMark forge={remote.forge} />
            <span className={CLASS.ref} data-testid="composer-repo-ref">
              <MiddleTruncated text={reference} cut={branchMarkerCut(reference)} />
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
