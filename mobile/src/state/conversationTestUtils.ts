import type { MutationReceipt } from "@evener/appwire-client";
import {
	type ConversationMutationRequest,
	createConversationStore,
} from "./conversation";

export const directConversationMutationSubmitter = {
	async submit(request: ConversationMutationRequest): Promise<MutationReceipt> {
		switch (request.kind) {
			case "send":
				return request.service.send(request.input);
			case "steer":
				return request.service.steer(request.input, request.expectedQueueRevision);
			case "queue":
				return request.service.queue(request.input);
			case "interrupt":
				return request.service.interrupt();
		}
	},
};

export function createTestConversationStore() {
	return createConversationStore({
		mutationSubmitter: directConversationMutationSubmitter,
		targetHubId: "test-hub",
	});
}
