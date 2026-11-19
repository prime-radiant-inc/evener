import type {
	ModelControlSnapshot,
	ThreadCapabilities,
	Thread,
	ThreadModelSetParams,
	ThreadModelSetResponse,
	ThreadReadResponse,
} from "@evener/appwire-client";

/**
 * PAST_CAPABILITIES is what the hub stamps for a session with no daemon behind
 * it (cmd/evener-hub/app_threadread.go's pastThreadCapabilities): changeModel
 * stays true because the hub resumes the session behind a model change, and it
 * carries neither modelControlVersion nor a modelControl snapshot.
 */
export const PAST_CAPABILITIES: ThreadCapabilities = {
	send: true,
	steer: false,
	interrupt: false,
	compact: true,
	clear: true,
	forkFromTurn: true,
	shutdown: true,
	changeModel: true,
	changeVisionModel: true,
	sharedNotes: true,
	queue: true,
	goal: true,
	rename: true,
	pageBefore: true,
};

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
				clientMutationId: params.clientMutationId,
				modelProvider: params.modelProvider,
				model: params.model,
				lifecycle: "applied",
			},
		},
	};
}
