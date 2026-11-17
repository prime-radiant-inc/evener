import type {
	ModelControlSnapshot,
	Thread,
	ThreadModelSetParams,
	ThreadModelSetResponse,
	ThreadReadResponse,
} from "@evener/appwire-client";

const BOOT_GENERATION = "test-boot";

export function modelControlRead(thread: Thread): ThreadReadResponse {
	const authority = {
		instanceId: thread.evener.instanceId ?? "test-instance",
		bootGeneration: BOOT_GENERATION,
	};
	const modelControl: ModelControlSnapshot = {
		authority,
		revision: 1,
		active: {
			modelProvider: "scripted",
			model: "current",
			reasoningEffortLevels: [],
			supportsReasoning: false,
		},
	};
	return {
		thread: {
			...thread,
			evener: {
				...thread.evener,
				instanceId: authority.instanceId,
				capabilities: { ...thread.evener.capabilities, modelControlVersion: 1 },
				modelControl,
			},
		},
		bootGeneration: authority.bootGeneration,
	};
}

export function acceptedModelControl(params: ThreadModelSetParams): ThreadModelSetResponse {
	return {
		modelControlVersion: 1,
		disposition: "accepted",
		modelControl: {
			authority: {
				instanceId: params.expectedInstanceId,
				bootGeneration: params.expectedBootGeneration,
			},
			revision: 2,
			active: {
				modelProvider: params.modelProvider,
				model: params.model,
				reasoningEffortLevels: [],
				supportsReasoning: false,
			},
			desired: {
				requestSequence: 2,
				clientMutationId: params.clientMutationId,
				modelProvider: params.modelProvider,
				model: params.model,
				lifecycle: "applied",
			},
		},
	};
}
