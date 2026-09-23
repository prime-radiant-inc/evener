import type { InputItem, MutationReceipt } from "@evener/appwire-client";

export type ConversationMutationKind = "send" | "steer" | "queue" | "interrupt";

export interface ConversationMutationRequest {
	readonly kind: ConversationMutationKind;
	readonly hubId: string;
	readonly targetRef: string;
	readonly threadId: string;
	readonly instanceId: string;
	readonly input: InputItem[];
	readonly expectedQueueRevision?: number;
}

export interface ConversationMutationSubmitter {
	submit(request: ConversationMutationRequest): Promise<MutationReceipt | undefined>;
}
