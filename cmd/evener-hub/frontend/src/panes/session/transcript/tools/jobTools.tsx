// Descriptors for job_* and delegate_send follow-up calls.
import type { ItemModel } from "@evener/appwire-client";
import {
  clip,
  delegateSendBase,
  delegateSendEarlierLabel,
  delegateSendEarlierResponses,
  delegateSendResponse,
  delegateSendSummary,
  delegateSendTarget,
  delegateSendWaitIgnoredReason,
  isDelegateSendResult,
  jobListSummary,
  jobStatusSummary,
  jobStopSummary,
  parseArgs,
  str,
  toolStepSummary,
} from "@evener/appwire-client";
import { CopyButton } from "../../../../widgets";
import { jobStatusDisplay } from "../../chrome/activityFormat";
import { EntityRef } from "../EntityRef";
import { UserMessageView } from "../messages/UserMessageItem";
import type { ToolRenderProps } from "../toolRenderers";
import { registerToolRenderer } from "../toolRenderers";
import { HeadClippedOutputBody } from "./bodies";
import { DelegateStatusBody } from "./delegateStatus";

const ID_CLIP = 26;

type JsonObject = Record<string, unknown>;

function asJsonObject(value: unknown): JsonObject | undefined {
  return typeof value === "object" && value !== null && !Array.isArray(value) ? (value as JsonObject) : undefined;
}

interface JobListState {
  items: JsonObject[];
  total: number;
}

// jobListState validates the current direct StateResult.State shape produced
// by jobListTool: {items:[{id,...}], count, total, ...}. Stored transcripts
// using the retired jobs/job_id shape remain readable through the separate
// legacy branch. There is no wrapper key because ExecuteCall marshals State
// itself.
function jobListState(raw: unknown): JobListState | undefined {
  const state = asJsonObject(raw);
  if (!state) return undefined;

  const values = Array.isArray(state.items) ? state.items : Array.isArray(state.jobs) ? state.jobs : undefined;
  if (!values) return undefined;
  const identityField = Array.isArray(state.items) ? "id" : "job_id";

  const items: JsonObject[] = [];
  for (const value of values) {
    const item = asJsonObject(value);
    if (!item || typeof item[identityField] !== "string") return undefined;
    items.push(item);
  }

  const total =
    typeof state.total === "number" ? state.total : typeof state.count === "number" ? state.count : items.length;
  return { items, total };
}

function textField(object: JsonObject, key: string): string | undefined {
  const value = object[key];
  return typeof value === "string" && value !== "" ? value : undefined;
}

function JobListBody({ item, live }: ToolRenderProps) {
  const state = jobListState(item.raw);
  if (!state) return <HeadClippedOutputBody item={item} live={live} />;

  return (
    <div data-testid="job-list-structured">
      {state.items.length === 0 ? (
        <div>No jobs.</div>
      ) : (
        state.items.map((job) => {
          const identity = textField(job, "id") ?? textField(job, "job_id");
          if (identity === undefined) return null;
          const rawStatus = textField(job, "status");
          const reason = textField(job, "reason");
          const fields = [
            textField(job, "type"),
            rawStatus === undefined ? undefined : jobStatusDisplay(rawStatus, reason),
            textField(job, "phase"),
          ].filter((field): field is string => field !== undefined);
          const description = textField(job, "description");
          return (
            <div key={identity} data-testid="job-list-row">
              <EntityRef id={identity} />
              {fields.length > 0 ? ` · ${fields.join(" · ")}` : ""}
              {description ? ` — ${description}` : ""}
            </div>
          );
        })
      )}
      <div data-testid="job-list-total">
        {state.items.length} of {state.total} jobs
      </div>
    </div>
  );
}

registerToolRenderer({
  match: (name) => name === "job_status" || name === "job_read_output",
  icon: "job",
  // Background work is state a reader tracks across a turn, so every job
  // row stays on its own line rather than folding into a run.
  fold: "never",
  summary: jobStatusSummary,
  body: DelegateStatusBody,
});

registerToolRenderer({
  match: "job_list",
  icon: "job",
  fold: "never",
  summary: jobListSummary,
  body: JobListBody,
});

registerToolRenderer({
  match: "job_stop",
  icon: "job",
  fold: "never",
  summary: jobStopSummary,
  body: HeadClippedOutputBody,
});

// The target transcript ref enables the row's open-in-pane action.
function delegateSendTranscriptRef(item: ItemModel): string | undefined {
  if (!isDelegateSendResult(item.raw)) return undefined;
  const ref = item.raw.transcript_ref;
  return ref !== undefined && ref.trim() !== "" ? ref : undefined;
}

// DelegateSendBody renders the exchange as a two-party conversation through
// the transcript's own slack-lean message view: the sent message as an
// outgoing bubble from the agent to the delegate, then any earlier results
// its wait carried and - when the call waited for one - the delegate's reply,
// each as an incoming bubble below it. The
// section testids (delegate-send-message/-response) are the longstanding
// contract of this body and are unchanged.
function DelegateSendBody(props: ToolRenderProps) {
  const { item } = props;
  const args = parseArgs(item.argumentsJSON);
  const message = str(args, "message");
  const response = delegateSendResponse(item);
  // Results the caller had not yet received, which the wait carried ahead of
  // its own reply (#3906), oldest first.
  const earlier = delegateSendEarlierResponses(item);
  const waitIgnoredReason = delegateSendWaitIgnoredReason(item);
  const target = clip(delegateSendTarget(item), ID_CLIP);

  if (!message && !response && earlier.length === 0) return null;
  return (
    <div data-testid="delegate-send-body">
      {message ? (
        <section data-testid="delegate-send-message">
          <UserMessageView
            item={{ ...item, text: message }}
            speaker="agent"
            name={target === "" ? "Agent → delegate" : `Agent → ${target}`}
            timeIso={item.startedAt}
            opensExchange={false}
            actions={<CopyButton text={message} label="Copy message" />}
          />
        </section>
      ) : null}
      {earlier.map((entry, index) => {
        const label = delegateSendEarlierLabel(entry, index, earlier.length);
        const { text } = entry;
        return (
          // biome-ignore lint/suspicious/noArrayIndexKey: earlier results are a fixed, ordered list
          <section key={index} data-testid="delegate-send-earlier-response">
            <UserMessageView
              // An earlier result arrived before this send; the send's own
              // times would date it wrongly, so the bubble shows none.
              item={{ ...item, text, startedAt: undefined, completedAt: undefined }}
              speaker="agent"
              name={target === "" ? `Delegate, ${label}` : `${target} (delegate, ${label})`}
              opensExchange={false}
              actions={<CopyButton text={text} label={`Copy ${label}`} />}
            />
          </section>
        );
      })}
      {response ? (
        <section data-testid="delegate-send-response">
          <UserMessageView
            item={{ ...item, text: response }}
            speaker="agent"
            name={target === "" ? "Delegate" : `${target} (delegate)`}
            timeIso={item.completedAt ?? item.startedAt}
            opensExchange={false}
            actions={<CopyButton text={response} label="Copy response" />}
          />
        </section>
      ) : null}
      {waitIgnoredReason ? <div data-testid="delegate-send-wait-ignored">Wait ignored: {waitIgnoredReason}</div> : null}
    </div>
  );
}

registerToolRenderer({
  match: (name) => name === "delegate_send" || name === "job_send_message",
  icon: "send",
  fold: "never",
  summary: delegateSendSummary,
  openTranscriptRef: delegateSendTranscriptRef,
  // The summary quotes the delegate target verbatim before the status meta
  // ("Sent a message to delegate <id> · <status>"), so the "open transcript"
  // control rides INLINE between the delegate it opens and the running-state
  // words that describe it (toolRenderers.ts's openTranscriptInline
  // contract) - the complete base prefix, matching summary()'s own text
  // exactly, so ToolRow can verify it with startsWith rather than search for
  // it. Undefined when there is no transcript to open, so the row never
  // builds a dead anchor-split wrapper for a button it will render nothing
  // for (ToolCallItem's own fileDocParams-gating idiom).
  openTranscriptInline: (item) => (delegateSendTranscriptRef(item) !== undefined ? delegateSendBase(item) : undefined),
  body: DelegateSendBody,
});

// Generic fallback for any other job_*-family tool not explicitly
// registered anywhere - "match by predicate" per this project's own locked
// ToolRendererDescriptor doc comment. job_watch has its own exact-match
// descriptor (jobWatch.tsx, mockups 23-job-watch §A-D); exact matches
// always win (toolRenderers.ts's own precedence rule), so this only ever
// resolves for a job_* name none of the specific descriptors claimed.
registerToolRenderer({
  match: (name) => name.startsWith("job_"),
  icon: "job",
  fold: "never",
  // The package's words for a job tool this build doesn't know: which
  // operation it ran, never the raw tool name.
  summary: (item: ItemModel) => toolStepSummary(item),
  body: HeadClippedOutputBody,
});
