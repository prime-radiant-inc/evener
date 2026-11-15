import type { NativeMutationIdentityChange } from "../nativeMutationRuntime";
import { owningSessionRef } from "./returnToSession";

type StackState = { index: number; routes: readonly { name: string; params?: unknown }[] };

type Owner = {
	hubId: string;
	documentRef: string;
	openedMessageRef: string;
	currentRef: string;
};

/** A live message target outranks an older route; an owner's pending identity
 * outranks the identity captured when the document opened. */
export function resolveOwnerIdentity(
	owner: Owner,
	getState?: () => StackState,
	subscribe?: (listener: (change: NativeMutationIdentityChange) => void) => () => void,
	onChange?: (ref: string) => void,
): { current: () => string; unsubscribe: () => void } {
	let current = owner.currentRef;
	const unsubscribe =
		subscribe?.((change) => {
			if (change.hubId !== owner.hubId || change.from !== current) return;
			current = change.to;
			onChange?.(current);
		}) ?? (() => {});
	const state = getState?.();
	if (state && current === owner.openedMessageRef) {
		const ownerRef = owningSessionRef(state as Parameters<typeof owningSessionRef>[0], {
			hubId: owner.hubId,
			documentRef: owner.documentRef,
			openedMessageRef: owner.openedMessageRef,
			messageRef: current,
		});
		const route = state.routes.slice(0, state.index).findLast((candidate) => {
			if (candidate.name !== "Conversation" && candidate.name !== "Subagent") return false;
			const params = candidate.params as { hubId?: unknown; ref?: unknown } | undefined;
			return params?.hubId === owner.hubId && params.ref === ownerRef;
		});
		const pending = (route?.params as { pendingIdentity?: unknown } | undefined)?.pendingIdentity;
		if (typeof pending === "string" && pending) current = pending;
	}
	onChange?.(current);
	return { current: () => current, unsubscribe };
}
