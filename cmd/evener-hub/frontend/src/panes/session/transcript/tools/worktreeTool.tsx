// Descriptors for manage_worktree and find_session_transcripts — the last two
// tools the agent runtime can emit that had no descriptor of their own and so
// fell through to toolRenderers.ts's DEFAULT_DESCRIPTOR, whose summary is
// `item.toolName`. The whole transcript row read `manage_worktree`.
//
// That is worse for this tool than for most. manage_worktree carries seven
// operations (create/list/switch/exit/remove/prune/dispose) plus `force` and
// `force_dirty`, and the Go docs describe force_dirty as overriding "the
// refusal to discard uncommitted changes" — so a read-only listing and a
// removal that throws away someone's work rendered as the same single word,
// distinguishable only by expanding the row. The row has to lead with the
// consequential step.
//
// Ground truth, verified against agent/session_tools_worktree.go: every
// operation returns a map with a `status` string, and the statuses are
// created/listed/switched/unchanged/exited/removed/pruned/already_disposed.
// Because Exec returns a plain map (not a tool.StateResult), the registry's
// toolValueToString json.MarshalIndents it, so item.output here is real
// parseable JSON — same situation web_fetch is in, and unlike the
// human-formatted text most tools in this directory return.
//
// The settled result is preferred over the arguments wherever it disagrees:
// a `switch` that turned out to be a no-op reads "Already in", not
// "Switched to", and an `already_disposed` dispose never claims a
// dirty-discard, because nothing was torn down to discard.

import { clip, parseArgs, str, worktreeSummary } from "@evener/appwire-client";
import { MCPToolArguments } from "../MCPToolArguments";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { HeadClippedOutputBody } from "./bodies";

registerToolRenderer({
  match: "manage_worktree",
  summary: worktreeSummary,
  fold: "consequential", // creates/switches/removes a tree: a mutation
  // The output really is parseable JSON here (see this file's header), but
  // whether each operation deserves its own structured body is a bigger
  // question than the row this fix is about - a head-clipped dump is honest
  // and complete in the meantime.
  body: HeadClippedOutputBody,
});

// find_session_transcripts is read-only, so its row carries far less risk —
// but a bare tool name told a reader nothing about what was searched for
// either. Its Exec returns human-formatted TEXT (not JSON), ending in either
// "N match (scope: …)" or "No matching sessions (scope: …)", which is where
// the count comes from.
//
// `children_of` leads when both are present, matching the tool's own
// precedence: asking for one session's children is a different question from
// a text search, and the parent ref is the more specific answer.
const MATCH_COUNT = /(\d+)\s+match/;
const NO_MATCHES = /No matching sessions/;

function findSessionsCount(output: string | undefined): number | undefined {
  if (output === undefined || output === "") return undefined;
  if (NO_MATCHES.test(output)) return 0;
  const found = output.match(MATCH_COUNT);
  return found?.[1] === undefined ? undefined : Number(found[1]);
}

function findSessionsSummary(item: { argumentsJSON?: string; output?: string }): string {
  const args = parseArgs(item.argumentsJSON);
  const childrenOf = str(args, "children_of");
  const query = str(args, "query");

  let lead: string;
  let noun: string;
  if (childrenOf !== undefined && childrenOf !== "") {
    lead = `Searched sessions spawned by ${childrenOf}`;
    noun = "matches";
  } else if (query !== undefined && query !== "") {
    lead = `Searched sessions for "${clip(query, 60)}"`;
    noun = "matches";
  } else {
    // No query and no parent: the tool's plain catalog listing, which reports
    // sessions rather than matches — there was nothing to match against.
    lead = "Listed recent sessions";
    noun = "sessions";
  }

  const count = findSessionsCount(item.output);
  return count === undefined ? lead : `${lead} · ${count} ${noun}`;
}

// The row's body composes the request arguments above the head-clipped
// output (matching defaultToolBody's own arrangement in toolRenderers.ts) so
// "what was searched for" is answerable from the expanded card, not just the
// summary line above it.
function FindSessionTranscriptsBody(props: ToolRenderProps) {
  return (
    <>
      <MCPToolArguments {...props} />
      <HeadClippedOutputBody {...props} />
    </>
  );
}

registerToolRenderer({
  match: "find_session_transcripts",
  // A read-only search: folds and only counts (toolFoldPolicy.test.ts pins
  // every registered tool's policy).
  fold: "quiet",
  summary: findSessionsSummary,
  body: FindSessionTranscriptsBody,
});
