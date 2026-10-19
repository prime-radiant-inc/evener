import { useIsFocused } from "@react-navigation/native";
import { useCallback, useEffect, useMemo, useRef, useState, useSyncExternalStore } from "react";
import type { NavigationPinSectionDescriptor } from "@evener/appwire-client";
import { useConnection } from "./ConnectionProvider";
import { organizationJournal } from "./nativeOrganization";
import { reconnectDelay } from "./hubConnection";
import { NavigationActions } from "./navigationActions";
import { NavigationPages } from "./navigationPages";
import { followPinCatalog, refreshPinNavigation } from "./pinNavigation";

const noSnapshot = () => null;
const noSubscription = () => () => {};
const sectionKey = (section: NavigationPinSectionDescriptor) => section.id;

export function usePinNavigation(hubId: string, sessionRef?: string, sectionId?: string) {
	const { client, activeProfile, state } = useConnection();
	const focused = useIsFocused();
	const belongs = activeProfile?.id === hubId;
	const ready = belongs && !!client && state === "ready";
	const journal = useMemo(() => organizationJournal(hubId), [hubId]);
	const pages = useMemo(
		() =>
			client && activeProfile?.id === hubId
				? new NavigationPages<NavigationPinSectionDescriptor>(
						client,
						{ resource: "pin_catalog" },
						"pin_sections",
						sectionKey,
						50,
						// The catalog's own follower re-reads only stale
						// states, so a failed read needs its own retry.
						{ readRetry: true },
					)
				: null,
		[client, activeProfile?.id, hubId],
	);
	const binding = useMemo(
		() => ({ client, pages, ready, focused, hubId, sessionRef, sectionId }),
		[client, pages, ready, focused, hubId, sessionRef, sectionId],
	);
	const owner = useRef<typeof binding | null>(binding);
	owner.current = binding;
	const isCurrent = useCallback(() => owner.current === binding && binding.ready && binding.focused, [binding]);
	const [readback, setReadback] = useState<{
		owner: typeof binding;
		value: Awaited<ReturnType<typeof refreshPinNavigation>>;
	} | null>(null);
	// A failed reconciliation strands the pin screens on a manual Check again
	// while the app sits idle; the hook re-runs reconcile() itself on the same
	// capped backoff the rest of the app's failed reads use, and retires once
	// a read lands. Only the full reconciliation read clears the action's
	// uncertainty and publishes the readback, so the catalog pages' own retry
	// cannot stand in for it.
	const retryTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
	const retryCount = useRef(0);
	/** Cancel the pending timer, keeping the failure count: a reconcile that
	 * passes through pending (its own retry, or a mutation) must not restart
	 * the backoff curve. */
	const cancelReconcileTimer = useCallback(() => {
		if (retryTimer.current !== null) {
			clearTimeout(retryTimer.current);
			retryTimer.current = null;
		}
	}, []);
	const clearReconcileRetry = useCallback(() => {
		cancelReconcileTimer();
		retryCount.current = 0;
	}, [cancelReconcileTimer]);
	useEffect(
		() => () => {
			if (retryTimer.current !== null) clearTimeout(retryTimer.current);
		},
		[],
	);
	const read = useMemo(() => {
		if (!client || !pages) return null;
		return async (checkpoint?: Parameters<typeof refreshPinNavigation>[2]["checkpoint"], confirmReceipt = false) => {
			const value = await refreshPinNavigation(client, pages, {
				checkpoint,
				sessionRef,
				sectionId,
				current: isCurrent,
				confirmReceipt,
			});
			if (isCurrent()) setReadback({ owner: binding, value });
		};
	}, [client, pages, binding, isCurrent, sessionRef, sectionId]);
	const actions = useMemo(() => {
		if (!client || !read) return null;
		return new NavigationActions(
			client,
			async (_receipt, checkpoint) => {
				if (!checkpoint) throw Error("Pin recovery was not saved.");
				await read(checkpoint, true);
			},
			isCurrent,
			(checkpoint) => read(checkpoint),
			journal,
		);
	}, [client, read, journal, isCurrent]);
	const armReconcileRetry = useCallback(() => {
		const settled = actions?.getSnapshot();
		if (retryTimer.current !== null) return;
		// A settled clean state asks nothing more; the effect retires the
		// count through clearReconcileRetry.
		if (settled && !settled.uncertain && !settled.pending) return;
		// The binding that armed this retry owns it: a late completion from a
		// retired screen must not occupy the current owner's timer.
		const arming = binding;
		retryCount.current += 1;
		retryTimer.current = setTimeout(() => {
			if (owner.current !== arming) {
				retryTimer.current = null;
				return;
			}
			retryTimer.current = null;
			// Re-arm from the attempt's own settled outcome: a failure that
			// never entered pending (a journal read that still throws)
			// publishes uncertain-not-pending and must ask again, and success
			// retires through the effect like any landing.
			const again = () => {
				if (owner.current === arming) armReconcileRetry();
			};
			if (isCurrent()) void actions?.reconcile().finally(again);
		}, reconnectDelay(retryCount.current));
	}, [actions, isCurrent, binding]);
	const page = useSyncExternalStore(pages?.subscribe ?? noSubscription, pages?.getSnapshot ?? noSnapshot);
	const action = useSyncExternalStore(actions?.subscribe ?? noSubscription, actions?.getSnapshot ?? noSnapshot);
	// The reconciliation's own outcome arms or retires its retry: success clears
	// it, a failure re-arms it while the screen can still act. The failure
	// count belongs to the owner: it survives pending transitions and re-focuses
	// (consecutive failures keep growing the backoff) and resets only when the
	// actions or the binding change — a new screen starts its curve fresh.
	const retryOwner = useRef(actions);
	if (retryOwner.current !== actions) {
		retryOwner.current = actions;
		cancelReconcileTimer();
		retryCount.current = 0;
	}
	useEffect(() => {
		if (!actions) return;
		if (action?.uncertain && !action.pending && ready && focused) armReconcileRetry();
		// Retire only on a settled clean state: restore() publishes a brief
		// not-uncertain flicker while a reconciliation starts, and retiring
		// there would reset the curve on every consecutive failure.
		else if (!action?.uncertain && !action?.pending) clearReconcileRetry();
	}, [actions, action?.uncertain, action?.pending, ready, focused, armReconcileRetry, clearReconcileRetry]);
	// A mutation's own confirmation re-reads everything, so catalog changes
	// wait for it; the follower drains on each action state change.
	useEffect(() => {
		if (!pages || !read || !actions) return;
		const follower = followPinCatalog(pages, read, () => isCurrent() && !actions.getSnapshot().pending);
		const unsubscribe = actions.subscribe(follower.drain);
		return () => {
			unsubscribe();
			follower.stop();
		};
	}, [pages, read, actions, isCurrent]);
	useEffect(() => {
		if (ready && focused) void actions?.reconcile();
		return () => {
			if (owner.current === binding) owner.current = null;
			actions?.dispose();
			pages?.cancel();
		};
	}, [actions, ready, focused, pages, binding]);
	const observed =
		readback?.owner.pages === pages &&
		readback.owner.hubId === hubId &&
		readback.owner.sessionRef === sessionRef &&
		readback.owner.sectionId === sectionId
			? readback.value
			: null;
	const confirmed = !!observed && readback?.owner === binding;
	return {
		client,
		activeProfile,
		belongs,
		ready,
		focused,
		pages,
		page,
		actions,
		action,
		observed,
		confirmed,
		isCurrent,
	};
}
