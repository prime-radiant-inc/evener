// Transactional projection from canonical MobileConversation into a bounded,
// immutable display/evidence snapshot. Canonical values remain available only
// through opaque operational lookups; source-derived display text is redacted
// before UTF-8 truncation.

import type {
  ActivityState,
  MobileConversation,
  MobileTimelineItem,
} from "../conversation/model";
import { boundDisplayText, DISPLAY_LIMITS } from "./display-text";
import type {
  ActivityMarkerDisplayItem,
  BoundedDisplayText,
  ConversationDisplayItem,
  DisplayTone,
  EvidenceDisplayItem,
  EvidenceSection,
  LiveConversationView,
  LiveQuestionView,
  NarrativeDisplayItem,
} from "./model";

function conversationTone(status: string): DisplayTone {
  if (status === "running") return "running";
  if (status === "error" || status === "failed") return "failed";
  if (status === "ready" || status === "idle") return "idle";
  return "unknown";
}

function activityTone(state: ActivityState): DisplayTone {
  if (state === "running") return "running";
  if (state === "failed") return "failed";
  return "success";
}

function bounded(
  source: string,
  limit: number,
  policy: "plain" | "redacted" = "redacted",
): BoundedDisplayText {
  return Object.freeze(boundDisplayText(source, limit, policy));
}

function storeTruncation(
  value: BoundedDisplayText,
  itemId: string,
  truncatedItemIds: ReadonlySet<string>,
): BoundedDisplayText {
  if (value.truncated || !truncatedItemIds.has(itemId)) return value;
  return Object.freeze({ ...value, truncated: true });
}

function fixed(
  source: string,
  limit: number = DISPLAY_LIMITS.feedLabel,
): BoundedDisplayText {
  return bounded(source, limit, "plain");
}

function durationLabel(
  durationMs: number | undefined,
): BoundedDisplayText | null {
  if (
    durationMs === undefined ||
    !Number.isFinite(durationMs) ||
    durationMs < 0
  ) {
    return null;
  }
  return fixed(`${Math.round(durationMs)} ms`);
}

export class ProjectionCapacityError extends Error {
  constructor() {
    super("projection capacity exceeded");
    this.name = "ProjectionCapacityError";
  }
}

// --- runtime-immutable map wrapper (I2) --------------------------------------

// A genuinely immutable map: set/delete/clear are defined (so casts to Map
// hit them) but throw without mutating the underlying data. The backing Map
// is hidden behind a #private field — no escape hatch via casts or property
// enumeration.
class FrozenMap<K, V> implements ReadonlyMap<K, V> {
  #map: Map<K, V>;

  constructor(entries: Iterable<[K, V]>) {
    this.#map = new Map(entries);
  }

  get size(): number {
    return this.#map.size;
  }

  get(key: K): V | undefined {
    return this.#map.get(key);
  }

  has(key: K): boolean {
    return this.#map.has(key);
  }

  keys(): MapIterator<K> {
    return this.#map.keys();
  }

  values(): MapIterator<V> {
    return this.#map.values();
  }

  entries(): MapIterator<[K, V]> {
    return this.#map.entries();
  }

  forEach(
    callback: (value: V, key: K, map: ReadonlyMap<K, V>) => void,
    thisArg?: unknown,
  ): void {
    this.#map.forEach((value, key) => {
      callback.call(thisArg, value, key, this);
    });
  }

  [Symbol.iterator](): MapIterator<[K, V]> {
    return this.#map.entries();
  }

  get [Symbol.toStringTag](): string {
    return "FrozenMap";
  }

  // Mutation guards — present so `as Map<K,V>` casts invoke these, not a
  // silent mutation of a real Map. They always throw, never mutate.
  set(_key: K, _value: V): this {
    throw new TypeError("Cannot mutate a frozen map");
  }

  delete(_key: K): boolean {
    throw new TypeError("Cannot mutate a frozen map");
  }

  clear(): void {
    throw new TypeError("Cannot mutate a frozen map");
  }
}

// --- tuple registry (C1: exact nested Maps, no delimiter composites) --------

// A registry of values keyed by exact string tuples, implemented as nested
// Maps. Each dimension is a separate Map key — hostile delimiters or NUL
// in any component cannot alias with another tuple.
class TupleRegistry<V> {
  private root = new Map<string, unknown>();
  private _size = 0;

  get(path: readonly string[]): V | undefined {
    let node: Map<string, unknown> | undefined = this.root;
    for (let i = 0; i < path.length - 1; i++) {
      const key = path[i] as string;
      node = node.get(key) as Map<string, unknown> | undefined;
      if (node === undefined) return undefined;
    }
    const last = path[path.length - 1] as string;
    return node?.get(last) as V | undefined;
  }

  has(path: readonly string[]): boolean {
    let node: Map<string, unknown> | undefined = this.root;
    for (let i = 0; i < path.length - 1; i++) {
      const key = path[i] as string;
      node = node.get(key) as Map<string, unknown> | undefined;
      if (node === undefined) return false;
    }
    const last = path[path.length - 1] as string;
    return node?.has(last) ?? false;
  }

  set(path: readonly string[], val: V): boolean {
    let node: Map<string, unknown> = this.root;
    for (let i = 0; i < path.length - 1; i++) {
      const key = path[i] as string;
      let next = node.get(key) as Map<string, unknown> | undefined;
      if (next === undefined) {
        next = new Map<string, unknown>();
        node.set(key, next);
      }
      node = next;
    }
    const last = path[path.length - 1] as string;
    const isNew = !node.has(last);
    if (isNew) this._size++;
    node.set(last, val);
    return isNew;
  }

  // Delete all entries under a given scope (first path element). Returns
  // freed values for allocated-key cleanup.
  deleteScope(scope: string): V[] {
    const values: V[] = [];
    const collect = (node: Map<string, unknown>) => {
      for (const [, v] of node) {
        if (v instanceof Map) {
          collect(v as Map<string, unknown>);
        } else {
          values.push(v as V);
          this._size--;
        }
      }
    };
    const sub = this.root.get(scope);
    if (sub !== undefined) {
      collect(sub as Map<string, unknown>);
      this.root.delete(scope);
    }
    return values;
  }

  clear(): void {
    this.root.clear();
    this._size = 0;
  }

  get size(): number {
    return this._size;
  }

  *entries(): IterableIterator<[string[], V]> {
    const walk = function* (
      node: Map<string, unknown>,
      prefix: string[],
    ): IterableIterator<[string[], V]> {
      for (const [k, v] of node) {
        const p = [...prefix, k];
        if (v instanceof Map) {
          yield* walk(v as Map<string, unknown>, p);
        } else {
          yield [p, v as V];
        }
      }
    };
    yield* walk(this.root, []);
  }
}

// --- operational map ---------------------------------------------------------

export interface QuestionLink {
  readonly callId: string;
  readonly questionKey: string;
}

export interface OptionLink {
  readonly callId: string;
  readonly questionKey: string;
  readonly label: string;
  readonly detail: string;
}

export interface ConversationOperationalMap {
  readonly itemKeys: ReadonlyMap<string, string>;
  readonly questionKeys: ReadonlyMap<string, QuestionLink>;
  readonly optionKeys: ReadonlyMap<string, OptionLink>;
  readonly evidenceKeys: ReadonlyMap<string, string>;
}

export interface ConversationDisplaySnapshot {
  readonly view: LiveConversationView;
  readonly operational: ConversationOperationalMap;
}

export interface ConversationProjectOptions {
  ref: string;
  olderCursor: string | null;
  projectLabel: string;
  updatedLabel: string | null;
  truncatedItemIds: ReadonlySet<string>;
}

export interface LiveConversationProjector {
  project(
    conv: MobileConversation,
    options: ConversationProjectOptions,
  ): ConversationDisplaySnapshot;
  reset(scope?: string): void;
  dispose(): void;
}

export type OpaqueKeyAllocator = () => string;

let moduleAllocatorCounter = 0;

function defaultAllocator(): OpaqueKeyAllocator {
  const prefix = `p${moduleAllocatorCounter++}`;
  let n = 0;
  return () => `${prefix}-${++n}`;
}

function itemHasEvidence(item: MobileTimelineItem): boolean {
  switch (item.kind) {
    case "activity":
      return Boolean(
        item.detail.arguments ||
          item.detail.output ||
          item.detail.error ||
          item.detail.exitCode !== undefined,
      );
    case "notice":
      return (
        item.text !== "" &&
        !(
          item.origin === "system" &&
          (item.family === "hidden-instruction" ||
            item.family === "system-prelude")
        )
      );
    case "attachments":
      return item.items.length > 0;
    case "user":
    case "assistant":
    case "question":
    case "failure":
      return false;
  }
}

const DEFAULT_MAX_REGISTRY = 10_000;

export function createLiveConversationProjector(options?: {
  allocator?: OpaqueKeyAllocator;
  maxRegistrySize?: number;
}): LiveConversationProjector {
  const alloc = options?.allocator ?? defaultAllocator();
  const maxRegistrySize = options?.maxRegistrySize ?? DEFAULT_MAX_REGISTRY;
  const keyRegistry = new TupleRegistry<string>();
  const sequenceRegistry = new TupleRegistry<string>();
  const allocatedKeys = new Set<string>();
  let totalIdentities = 0;

  function countNewIdentities(
    items: readonly MobileTimelineItem[],
    scope: string,
  ): number {
    const seen = new TupleRegistry<true>();
    let count = 0;
    const check = (path: string[]): void => {
      if (seen.has(path)) return;
      seen.set(path, true);
      if (!keyRegistry.has(path)) count += 1;
    };
    check([scope, "thread"]);
    for (const item of items) {
      if (item.kind === "question") {
        if (item.batch.questions.length === 0) continue;
        for (const question of item.batch.questions) {
          check([scope, "qitem", item.batch.callId, question.key]);
          check([scope, "question", item.batch.callId, question.key]);
          for (const option of question.options) {
            check([
              scope,
              "option",
              item.batch.callId,
              question.key,
              option.label,
              option.detail,
            ]);
          }
        }
      } else {
        check([scope, "item", item.id]);
        if (itemHasEvidence(item)) check([scope, "evidence", item.id]);
      }
    }
    return count;
  }

  return {
    project(
      conv: MobileConversation,
      opts: ConversationProjectOptions,
    ): ConversationDisplaySnapshot {
      const scope = opts.ref;
      const newCount = countNewIdentities(conv.items, scope);
      if (totalIdentities + newCount > maxRegistrySize) {
        throw new ProjectionCapacityError();
      }

      const stagedKeys = new TupleRegistry<string>();
      const stagedSequences = new TupleRegistry<string>();
      const stagedAllocated = new Set<string>();

      const stageKey = (path: string[]): string => {
        const committed = keyRegistry.get(path);
        if (committed !== undefined) return committed;
        const staged = stagedKeys.get(path);
        if (staged !== undefined) return staged;
        const key = alloc();
        if (allocatedKeys.has(key) || stagedAllocated.has(key)) {
          throw new ProjectionCapacityError();
        }
        stagedAllocated.add(key);
        stagedKeys.set(path, key);
        return key;
      };

      const stageSequence = (path: string[]): string => {
        const committed = sequenceRegistry.get(path);
        if (committed !== undefined) return committed;
        const staged = stagedSequences.get(path);
        if (staged !== undefined) return staged;
        const sequence = alloc();
        if (allocatedKeys.has(sequence) || stagedAllocated.has(sequence)) {
          throw new ProjectionCapacityError();
        }
        stagedAllocated.add(sequence);
        stagedSequences.set(path, sequence);
        return sequence;
      };

      const questions: LiveQuestionView[] = [];
      const questionDisplayKeys = new Map<string, Map<string, string>>();
      const operationalQuestions = new Map<string, QuestionLink>();
      const operationalOptions = new Map<string, OptionLink>();
      const seenQuestions = new Set<string>();

      for (const item of conv.items) {
        if (item.kind !== "question" || item.batch.questions.length === 0) {
          continue;
        }
        const callId = item.batch.callId;
        for (const question of item.batch.questions) {
          const questionIdentity = JSON.stringify([callId, question.key]);
          if (seenQuestions.has(questionIdentity)) {
            throw new Error("Indistinguishable duplicate question");
          }
          seenQuestions.add(questionIdentity);
          const questionKey = stageKey([
            scope,
            "question",
            callId,
            question.key,
          ]);
          let batchKeys = questionDisplayKeys.get(callId);
          if (batchKeys === undefined) {
            batchKeys = new Map();
            questionDisplayKeys.set(callId, batchKeys);
          }
          batchKeys.set(question.key, questionKey);
          operationalQuestions.set(
            questionKey,
            Object.freeze({ callId, questionKey: question.key }),
          );

          const seenOptions = new Set<string>();
          const displayOptions = question.options.map((option) => {
            const optionIdentity = JSON.stringify([
              option.label,
              option.detail,
            ]);
            if (seenOptions.has(optionIdentity)) {
              throw new Error("Indistinguishable duplicate option");
            }
            seenOptions.add(optionIdentity);
            const optionKey = stageKey([
              scope,
              "option",
              callId,
              question.key,
              option.label,
              option.detail,
            ]);
            operationalOptions.set(
              optionKey,
              Object.freeze({
                callId,
                questionKey: question.key,
                label: option.label,
                detail: option.detail,
              }),
            );
            return Object.freeze({
              key: optionKey,
              label: bounded(option.label, DISPLAY_LIMITS.questionOptionLabel),
              detail: bounded(
                option.detail,
                DISPLAY_LIMITS.questionOptionDetail,
              ),
            });
          });
          questions.push(
            Object.freeze({
              key: questionKey,
              header: bounded(question.header, DISPLAY_LIMITS.questionHeader),
              prompt: bounded(question.question, DISPLAY_LIMITS.questionPrompt),
              options: Object.freeze(displayOptions),
              multiple: question.multiSelect,
              why:
                question.why === undefined
                  ? null
                  : bounded(
                      question.why,
                      DISPLAY_LIMITS.questionSupportingText,
                    ),
              ifUnanswered:
                question.ifUnanswered === undefined
                  ? null
                  : bounded(
                      question.ifUnanswered,
                      DISPLAY_LIMITS.questionSupportingText,
                    ),
            }),
          );
        }
      }

      const items: ConversationDisplayItem[] = [];
      const evidence: EvidenceDisplayItem[] = [];
      const operationalItems = new Map<string, string>();
      const operationalEvidence = new Map<string, string>();

      const addEvidence = (
        sourceItem: MobileTimelineItem,
        family: ActivityMarkerDisplayItem["sourceKind"],
        title: BoundedDisplayText,
        sectionInputs: ReadonlyArray<{
          heading: string;
          body: string | undefined;
          limit: number;
        }>,
      ): string | null => {
        const populated = sectionInputs.filter(
          (
            section,
          ): section is { heading: string; body: string; limit: number } =>
            section.body !== undefined && section.body !== "",
        );
        if (populated.length === 0) return null;
        const evidenceKey = stageKey([scope, "evidence", sourceItem.id]);
        const sections: EvidenceSection[] = populated.map((section) =>
          Object.freeze({
            heading: fixed(section.heading, DISPLAY_LIMITS.evidenceHeading),
            body: bounded(section.body, section.limit),
          }),
        );
        evidence.push(
          Object.freeze({
            key: evidenceKey,
            family,
            title,
            sections: Object.freeze(sections),
            redacted: true,
          }),
        );
        operationalEvidence.set(evidenceKey, sourceItem.id);
        return evidenceKey;
      };

      const addNarrative = (
        sourceItem: Exclude<
          MobileTimelineItem,
          { kind: "activity" | "notice" | "attachments" }
        >,
        row: Omit<NarrativeDisplayItem, "key" | "sequence">,
        path: string[],
      ): void => {
        const key = stageKey(path);
        items.push(
          Object.freeze({
            ...row,
            key,
            sequence: stageSequence(path),
          }),
        );
        operationalItems.set(key, sourceItem.id);
      };

      const addMarker = (
        sourceItem: Extract<
          MobileTimelineItem,
          { kind: "activity" | "notice" | "attachments" }
        >,
        row: Omit<ActivityMarkerDisplayItem, "key" | "sequence">,
      ): void => {
        const path = [scope, "item", sourceItem.id];
        const key = stageKey(path);
        items.push(
          Object.freeze({
            ...row,
            key,
            sequence: stageSequence(path),
          }),
        );
        operationalItems.set(key, sourceItem.id);
      };

      for (const item of conv.items) {
        switch (item.kind) {
          case "user": {
            const body = storeTruncation(
              bounded(item.text, DISPLAY_LIMITS.userMessage),
              item.id,
              opts.truncatedItemIds,
            );
            addNarrative(
              item,
              {
                sourceKind: "user",
                body,
                label: fixed("You"),
                tone: "idle",
                streaming: false,
                questionKey: null,
              },
              [scope, "item", item.id],
            );
            break;
          }
          case "assistant": {
            const body = storeTruncation(
              bounded(item.markdown, DISPLAY_LIMITS.assistantProse),
              item.id,
              opts.truncatedItemIds,
            );
            addNarrative(
              item,
              {
                sourceKind: "assistant",
                body,
                label: fixed("Assistant"),
                tone: item.streaming ? "running" : "idle",
                streaming: item.streaming,
                questionKey: null,
              },
              [scope, "item", item.id],
            );
            break;
          }
          case "question": {
            if (item.batch.questions.length === 0) break;
            for (const question of item.batch.questions) {
              const questionKey =
                questionDisplayKeys.get(item.batch.callId)?.get(question.key) ??
                null;
              addNarrative(
                item,
                {
                  sourceKind: "question",
                  body: bounded(
                    question.question,
                    DISPLAY_LIMITS.questionPrompt,
                  ),
                  label: bounded(
                    question.header,
                    DISPLAY_LIMITS.questionHeader,
                  ),
                  tone: "attention",
                  streaming: false,
                  questionKey,
                },
                [scope, "qitem", item.batch.callId, question.key],
              );
            }
            break;
          }
          case "failure":
            addNarrative(
              item,
              {
                sourceKind: "failure",
                body: bounded(item.detail, DISPLAY_LIMITS.failureBody),
                label: bounded(item.title, DISPLAY_LIMITS.failureTitle),
                tone: "failed",
                streaming: false,
                questionKey: null,
              },
              [scope, "item", item.id],
            );
            break;
          case "activity": {
            if (item.family === "tool") {
              const label = bounded(item.label, DISPLAY_LIMITS.feedLabel);
              const previewSource =
                item.state === "failed"
                  ? item.detail.error
                  : item.detail.output;
              const evidenceKey = addEvidence(
                item,
                "tool",
                bounded(item.label, DISPLAY_LIMITS.detailLabel),
                [
                  {
                    heading: "Arguments",
                    body: item.detail.arguments,
                    limit: DISPLAY_LIMITS.toolDetail,
                  },
                  {
                    heading: "Output",
                    body: item.detail.output,
                    limit: DISPLAY_LIMITS.toolDetail,
                  },
                  {
                    heading: "Error",
                    body: item.detail.error,
                    limit: DISPLAY_LIMITS.toolDetail,
                  },
                  {
                    heading: "Exit code",
                    body:
                      item.detail.exitCode === undefined
                        ? undefined
                        : String(item.detail.exitCode),
                    limit: DISPLAY_LIMITS.toolDetail,
                  },
                ],
              );
              const preview =
                previewSource === undefined || previewSource === ""
                  ? null
                  : storeTruncation(
                      bounded(previewSource, DISPLAY_LIMITS.toolPreview),
                      item.id,
                      opts.truncatedItemIds,
                    );
              addMarker(item, {
                sourceKind: "tool",
                semanticKind: "tool",
                label,
                preview,
                duration: durationLabel(item.detail.durationMs),
                tone: activityTone(item.state),
                state: item.state,
                evidenceKey,
              });
            } else if (item.family === "reasoning") {
              const output = item.detail.output;
              const evidenceKey = addEvidence(
                item,
                "reasoning",
                fixed("Reasoning", DISPLAY_LIMITS.detailLabel),
                [
                  {
                    heading: "Details",
                    body: output,
                    limit: DISPLAY_LIMITS.reasoningDetail,
                  },
                ],
              );
              addMarker(item, {
                sourceKind: "reasoning",
                semanticKind: "reasoning",
                label: fixed("Reasoning"),
                preview:
                  output === undefined || output === ""
                    ? null
                    : storeTruncation(
                        bounded(output, DISPLAY_LIMITS.reasoningPreview),
                        item.id,
                        opts.truncatedItemIds,
                      ),
                duration: durationLabel(item.detail.durationMs),
                tone: activityTone(item.state),
                state: item.state,
                evidenceKey,
              });
            } else {
              const evidenceKey = addEvidence(
                item,
                "unknown",
                fixed("Activity", DISPLAY_LIMITS.detailLabel),
                [
                  {
                    heading: "Details",
                    body: item.detail.output,
                    limit: DISPLAY_LIMITS.unknownDetail,
                  },
                ],
              );
              addMarker(item, {
                sourceKind: "unknown",
                semanticKind: "activity",
                label: fixed("Activity"),
                preview: null,
                duration: durationLabel(item.detail.durationMs),
                tone: activityTone(item.state),
                state: item.state,
                evidenceKey,
              });
            }
            break;
          }
          case "notice": {
            if (item.origin === "system") {
              if (
                item.family === "hidden-instruction" ||
                item.family === "system-prelude"
              ) {
                addMarker(item, {
                  sourceKind: "system",
                  semanticKind: "system-context",
                  label: fixed("System context"),
                  preview: null,
                  duration: null,
                  tone: "idle",
                  state: "unavailable",
                  evidenceKey: null,
                });
                break;
              }
              if (item.family === "diagnostic") {
                const evidenceKey = addEvidence(
                  item,
                  "diagnostic",
                  fixed("Diagnostic", DISPLAY_LIMITS.detailLabel),
                  [
                    {
                      heading: "Details",
                      body: item.text,
                      limit: DISPLAY_LIMITS.diagnosticDetail,
                    },
                  ],
                );
                addMarker(item, {
                  sourceKind: "diagnostic",
                  semanticKind: "system-activity",
                  label: fixed("Diagnostic"),
                  preview: bounded(item.text, DISPLAY_LIMITS.diagnosticPreview),
                  duration: null,
                  tone: "idle",
                  state: "completed",
                  evidenceKey,
                });
                break;
              }
              if (item.family === "unknown-system") {
                const evidenceKey = addEvidence(
                  item,
                  "system",
                  fixed("System activity", DISPLAY_LIMITS.detailLabel),
                  [
                    {
                      heading: "Details",
                      body: item.text,
                      limit: DISPLAY_LIMITS.unknownDetail,
                    },
                  ],
                );
                addMarker(item, {
                  sourceKind: "system",
                  semanticKind: "system-activity",
                  label: fixed("System activity"),
                  preview: null,
                  duration: null,
                  tone: "idle",
                  state: "unavailable",
                  evidenceKey,
                });
                break;
              }
              const lifecycle = item.family === "lifecycle";
              const evidenceKey = addEvidence(
                item,
                "system",
                fixed(
                  lifecycle ? "System activity" : "Notice",
                  DISPLAY_LIMITS.detailLabel,
                ),
                [
                  {
                    heading: "Details",
                    body: item.text,
                    limit: lifecycle
                      ? DISPLAY_LIMITS.lifecycleDetail
                      : DISPLAY_LIMITS.noticeDetail,
                  },
                ],
              );
              addMarker(item, {
                sourceKind: "system",
                semanticKind: lifecycle ? "system-activity" : "warning-notice",
                label: fixed(lifecycle ? "System activity" : "Notice"),
                preview: bounded(item.text, DISPLAY_LIMITS.noticePreview),
                duration: null,
                tone: item.tone === "warning" ? "attention" : "idle",
                state: "completed",
                evidenceKey,
              });
              break;
            }
            const warning = item.family === "warning";
            const evidenceKey = addEvidence(
              item,
              "notice",
              fixed("Notice", DISPLAY_LIMITS.detailLabel),
              [
                {
                  heading: "Details",
                  body: item.text,
                  limit: DISPLAY_LIMITS.noticeDetail,
                },
              ],
            );
            addMarker(item, {
              sourceKind: "notice",
              semanticKind: warning ? "warning-notice" : "notice",
              label: fixed("Notice"),
              preview: bounded(item.text, DISPLAY_LIMITS.noticePreview),
              duration: null,
              tone: warning ? "attention" : "idle",
              state: "completed",
              evidenceKey,
            });
            break;
          }
          case "attachments": {
            const metadata = item.items
              .map((attachment) =>
                [attachment.name ?? "attachment", attachment.mediaType]
                  .filter((value): value is string => Boolean(value))
                  .join(" — "),
              )
              .join("\n");
            const labelSource = item.items
              .map((attachment) => attachment.name ?? "attachment")
              .join(", ");
            const evidenceKey = addEvidence(
              item,
              "attachment",
              bounded(labelSource, DISPLAY_LIMITS.detailLabel),
              [
                {
                  heading: "Metadata",
                  body: metadata,
                  limit: DISPLAY_LIMITS.attachmentMetadata,
                },
              ],
            );
            addMarker(item, {
              sourceKind: "attachment",
              semanticKind: "attachment",
              label: bounded(labelSource, DISPLAY_LIMITS.attachmentLabel),
              preview: null,
              duration: null,
              tone: "idle",
              state: "completed",
              evidenceKey,
            });
            break;
          }
        }
      }

      const evidenceCounts = new Map<string, number>();
      for (const entry of evidence) {
        evidenceCounts.set(entry.key, (evidenceCounts.get(entry.key) ?? 0) + 1);
      }
      for (const item of items) {
        if (!("evidenceKey" in item) || item.evidenceKey === null) continue;
        if (
          evidenceCounts.get(item.evidenceKey) !== 1 ||
          !operationalEvidence.has(item.evidenceKey)
        ) {
          throw new Error("Invalid evidence projection");
        }
      }

      const threadKey = stageKey([scope, "thread"]);
      const title = conv.name ?? conv.preview;
      const view: LiveConversationView = Object.freeze({
        threadKey,
        title: bounded(title, DISPLAY_LIMITS.titleProject),
        project: bounded(opts.projectLabel, DISPLAY_LIMITS.titleProject),
        status: bounded(conv.status, DISPLAY_LIMITS.statusUpdated),
        items: Object.freeze(items),
        evidence: Object.freeze(evidence),
        questions: Object.freeze(questions),
        olderAvailable: opts.olderCursor !== null,
        tone: conversationTone(conv.status),
        updatedLabel:
          opts.updatedLabel === null
            ? null
            : bounded(opts.updatedLabel, DISPLAY_LIMITS.statusUpdated),
      });

      const snapshot: ConversationDisplaySnapshot = Object.freeze({
        view,
        operational: Object.freeze({
          itemKeys: new FrozenMap(operationalItems),
          questionKeys: new FrozenMap(operationalQuestions),
          optionKeys: new FrozenMap(operationalOptions),
          evidenceKeys: new FrozenMap(operationalEvidence),
        }),
      });

      // Commit only after the complete immutable feed/evidence/operational
      // snapshot has been built and validated successfully.
      for (const [path, key] of stagedKeys.entries()) {
        keyRegistry.set(path, key);
        allocatedKeys.add(key);
      }
      for (const [path, sequence] of stagedSequences.entries()) {
        sequenceRegistry.set(path, sequence);
        allocatedKeys.add(sequence);
      }
      totalIdentities += stagedKeys.size;
      return snapshot;
    },

    reset(scope?: string): void {
      if (scope === undefined) {
        keyRegistry.clear();
        sequenceRegistry.clear();
        allocatedKeys.clear();
        totalIdentities = 0;
        return;
      }
      for (const key of keyRegistry.deleteScope(scope)) {
        allocatedKeys.delete(key);
      }
      for (const sequence of sequenceRegistry.deleteScope(scope)) {
        allocatedKeys.delete(sequence);
      }
      totalIdentities = keyRegistry.size;
    },

    dispose(): void {
      keyRegistry.clear();
      sequenceRegistry.clear();
      allocatedKeys.clear();
      totalIdentities = 0;
    },
  };
}
