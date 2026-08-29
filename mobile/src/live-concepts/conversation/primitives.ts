import type { BoundedDisplayText } from "../model";

export type ComposerMode = "send" | "steer" | "queue";
export type ConversationMutationKind = ComposerMode | "interrupt";

export interface QuestionDraft {
  readonly selectedOptionKeys: readonly string[];
  readonly note: string;
  readonly resolution: "answer" | "fallback" | "decide" | "skip" | null;
}

export interface ConversationMutationView {
  readonly kind: ConversationMutationKind;
  readonly status: "pending" | "failed";
  readonly draftSnapshot: string | null;
  readonly generation: number;
}

export interface AcceptedMutationView {
  readonly kind: ConversationMutationKind;
  readonly disposition: "applied" | "replayed";
}

export interface LiveComposerView {
  readonly draft: string;
  readonly canSend: boolean;
  readonly canSteer: boolean;
  readonly canQueue: boolean;
  readonly canInterrupt: boolean;
  readonly pending: ConversationMutationView | null;
  readonly accepted: AcceptedMutationView | null;
  readonly error: BoundedDisplayText | null;
}

export type ConversationFrameAction =
  | { readonly type: "goBack" }
  | { readonly type: "openWork" }
  | { readonly type: "openVoice" }
  | { readonly type: "openConceptSwitcher" }
  | { readonly type: "loadOlder" }
  | { readonly type: "retryRead" }
  | { readonly type: "setDraft"; readonly value: string }
  | { readonly type: "setComposerMode"; readonly mode: ComposerMode }
  | { readonly type: "submit"; readonly mode: ComposerMode }
  | { readonly type: "interrupt" }
  | { readonly type: "toggleTool"; readonly key: string }
  | {
      readonly type: "setQuestionDraft";
      readonly key: string;
      readonly value: QuestionDraft;
    }
  | { readonly type: "submitQuestion"; readonly key: string }
  | {
      readonly type: "openEvidence";
      readonly evidenceKey: string;
      readonly triggerKey: string;
    }
  | { readonly type: "closeEvidence" };
