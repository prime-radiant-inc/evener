import type { InputAttachment, InputItem, MutationReceipt } from "@evener/appwire-client";

export type ConversationMutationKind = "send" | "steer" | "queue" | "interrupt";

// Preserve the editing source alongside the translated wire input so a later
// recovery surface can restore marker anchors and attachment metadata exactly.
export interface ConversationComposerSnapshot {
	readonly text: string;
	readonly attachments: readonly InputAttachment[];
}

export interface ConversationMutationRequest {
	readonly kind: ConversationMutationKind;
	readonly hubId: string;
	readonly targetRef: string;
	readonly threadId: string;
	readonly instanceId: string;
	readonly input: InputItem[];
	readonly composer?: ConversationComposerSnapshot;
	readonly expectedQueueRevision?: number;
}

export interface ConversationMutationSubmitter {
	submit(request: ConversationMutationRequest): Promise<MutationReceipt | undefined>;
}
