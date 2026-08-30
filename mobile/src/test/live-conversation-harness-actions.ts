/**
 * Typed harness actions and the live conversation harness API.
 *
 * This module is the contract surface between deterministic Node/jsdom tests
 * (live-conversation-harness-actions.test.ts) and the actual production
 * `LiveConceptHost` / `ConversationFrame` composition. It does NOT render
 * concept components directly; it drives the real production store types and
 * the real `ConversationFrameState`/`LiveComposerView` projection, then
 * observes the resulting frame state.
 *
 * Every action is validated before dispatch: unknown fields/actions are
 * rejected. The harness seeds a ready frame with a known draft and last-good
 * key digest, so each `drive(action)` call starts from a fresh, deterministic
 * ready state.
 */

import type { ConversationFrameState } from "../live-concepts/conversation/contract";
import type {
  ComposerMode,
  ConversationMutationKind,
} from "../live-concepts/conversation/primitives";

export type HarnessAction =
  | { readonly type: "items/prepend" }
  | { readonly type: "items/replace-authoritative" }
  | { readonly type: "items/evict"; readonly key: string }
  | {
      readonly type: "items/stream";
      readonly key: string;
      readonly delta: string;
    }
  | { readonly type: "conversation/loading"; readonly generation: number }
  | { readonly type: "conversation/empty"; readonly generation: number }
  | { readonly type: "connection/offline" }
  | {
      readonly type: "connection/reconnecting";
      readonly generation: number;
    }
  | {
      readonly type: "read/fail";
      readonly generation: number;
      readonly lastGood: "retain" | "none";
    }
  | {
      readonly type: "mutation/pending";
      readonly kind: ConversationMutationKind;
      readonly draftSnapshot: string;
    }
  | {
      readonly type: "mutation/fail";
      readonly kind: ConversationMutationKind;
      readonly draftSnapshot: string;
    }
  | { readonly type: "projection/malformed"; readonly generation: number }
  | {
      readonly type: "projection/publish";
      readonly generation: number;
      readonly fixture: "pathological-39" | "variable-500";
    }
  | {
      readonly type: "viewport/set";
      readonly innerHeight: number;
      readonly offsetTop: number;
      readonly height: number;
      readonly safeBottom: number;
    };

export interface HarnessObservation {
  readonly acceptedGeneration: number;
  readonly phase: ConversationFrameState["phase"];
  readonly lastGoodKeyDigest: string | null;
  readonly draft: string;
  readonly surface: "sessions" | "conversation" | "work";
  readonly navigationReachable: boolean;
  readonly composer: {
    readonly draftEditable: boolean;
    readonly primaryActionEnabled: boolean;
    readonly mode: ComposerMode;
  };
  readonly retry: {
    readonly visible: boolean;
    readonly invocationCount: number;
  };
  readonly mutation: {
    readonly kind: ConversationMutationKind;
    readonly status: "pending" | "failed";
  } | null;
  readonly alertCount: number;
  readonly compatibilityError: string | null;
}

export interface LiveConversationHarnessApi {
  dispatch(action: HarnessAction): Promise<HarnessObservation>;
  snapshot(): HarnessObservation;
}

/**
 * Known action type set, used to reject unknown actions and unknown fields.
 * Kept in sync with the HarnessAction union by construction.
 */
const KNOWN_ACTION_TYPES = new Set<HarnessAction["type"]>([
  "items/prepend",
  "items/replace-authoritative",
  "items/evict",
  "items/stream",
  "conversation/loading",
  "conversation/empty",
  "connection/offline",
  "connection/reconnecting",
  "read/fail",
  "mutation/pending",
  "mutation/fail",
  "projection/malformed",
  "projection/publish",
  "viewport/set",
]);

/** Validate a HarnessAction before dispatch. Throws on unknown type/field. */
export function validateHarnessAction(
  action: unknown,
): asserts action is HarnessAction {
  if (typeof action !== "object" || action === null || Array.isArray(action)) {
    throw new Error("harness action must be a plain object");
  }
  const a = action as Record<string, unknown>;
  if (typeof a.type !== "string") {
    throw new Error("harness action missing string type");
  }
  if (!KNOWN_ACTION_TYPES.has(a.type as HarnessAction["type"])) {
    throw new Error(`unknown harness action type: ${a.type}`);
  }
  const allowed: Record<string, readonly string[]> = {
    "items/prepend": [],
    "items/replace-authoritative": [],
    "items/evict": ["key"],
    "items/stream": ["key", "delta"],
    "conversation/loading": ["generation"],
    "conversation/empty": ["generation"],
    "connection/offline": [],
    "connection/reconnecting": ["generation"],
    "read/fail": ["generation", "lastGood"],
    "mutation/pending": ["kind", "draftSnapshot"],
    "mutation/fail": ["kind", "draftSnapshot"],
    "projection/malformed": ["generation"],
    "projection/publish": ["generation", "fixture"],
    "viewport/set": ["innerHeight", "offsetTop", "height", "safeBottom"],
  };
  const allowedFields = allowed[a.type] ?? [];
  const allowedSet = new Set(["type", ...allowedFields]);
  for (const key of Object.keys(a)) {
    if (!allowedSet.has(key)) {
      throw new Error(`unknown field "${key}" on action "${a.type}"`);
    }
  }
  // Type-specific value validation
  switch (a.type) {
    case "items/evict":
      if (typeof a.key !== "string")
        throw new Error('items/evict requires string "key"');
      break;
    case "items/stream":
      if (typeof a.key !== "string")
        throw new Error('items/stream requires string "key"');
      if (typeof a.delta !== "string")
        throw new Error('items/stream requires string "delta"');
      break;
    case "conversation/loading":
    case "conversation/empty":
    case "connection/reconnecting":
    case "projection/malformed":
      if (typeof a.generation !== "number")
        throw new Error(`${a.type} requires number "generation"`);
      break;
    case "read/fail":
      if (typeof a.generation !== "number")
        throw new Error('read/fail requires number "generation"');
      if (a.lastGood !== "retain" && a.lastGood !== "none")
        throw new Error('read/fail requires lastGood "retain"|"none"');
      break;
    case "mutation/pending":
    case "mutation/fail":
      if (typeof a.kind !== "string")
        throw new Error(`${a.type} requires string "kind"`);
      if (typeof a.draftSnapshot !== "string")
        throw new Error(`${a.type} requires string "draftSnapshot"`);
      break;
    case "projection/publish":
      if (typeof a.generation !== "number")
        throw new Error('projection/publish requires number "generation"');
      if (a.fixture !== "pathological-39" && a.fixture !== "variable-500")
        throw new Error(
          'projection/publish requires fixture "pathological-39"|"variable-500"',
        );
      break;
    case "viewport/set":
      if (typeof a.innerHeight !== "number")
        throw new Error('viewport/set requires number "innerHeight"');
      if (typeof a.offsetTop !== "number")
        throw new Error('viewport/set requires number "offsetTop"');
      if (typeof a.height !== "number")
        throw new Error('viewport/set requires number "height"');
      if (typeof a.safeBottom !== "number")
        throw new Error('viewport/set requires number "safeBottom"');
      break;
  }
}
