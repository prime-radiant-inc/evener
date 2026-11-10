import { type RefObject, useEffect, useMemo, useRef } from "react";
import type { FlatList } from "react-native";
import type { MobileConversation } from "../projectedRows";
import { ReaderRestoreAttempts, readerKey } from "../readerPosition";
import { atEnd, type useLiveEndFollow } from "./liveEndFollow";
import { type FindState, findMatches, stepMatch } from "./findInSession";
import type { TimelineRow } from "../timeline";

/** What Find reads from its screen: the list and its reading position, the
 * connection and the store's older-history paging. */
export interface FindInSessionInputs {
	find: FindState | null;
	setFind(find: FindState | null): void;
	timelineRows: readonly TimelineRow[];
	conversation: MobileConversation | null;
	focused: boolean;
	status: string;
	loadingOlder: boolean;
	olderPage: string | null;
	service: unknown;
	connected: boolean;
	olderHistory: { permanent: boolean };
	isCurrentReader(): boolean;
	findOlder(): void;
	follow: ReturnType<typeof useLiveEndFollow>;
	timeline: RefObject<FlatList | null>;
	listOffset: RefObject<number>;
	readerContentHeight: RefObject<number>;
	readerViewportHeight: RefObject<number>;
	listUnderBar: { contentInset?: Parameters<typeof atEnd>[0]["contentInset"] };
	captureSuppressed: RefObject<boolean>;
}

/** Find in session (ruling 29): the matches among the loaded rows, stepping
 * between them, seeking older history when none is left, and bringing the
 * current match into view. */
export function useFindInSession({
	find,
	setFind,
	timelineRows,
	conversation,
	focused,
	status,
	loadingOlder,
	olderPage,
	service,
	connected,
	olderHistory,
	isCurrentReader,
	findOlder,
	follow,
	timeline,
	listOffset,
	readerContentHeight,
	readerViewportHeight,
	listUnderBar,
	captureSuppressed,
}: FindInSessionInputs) {
	const findQuery = find?.query ?? "";
	const findHits = useMemo(
		() => findMatches(timelineRows, findQuery, conversation?.delegates),
		[timelineRows, findQuery, conversation?.delegates],
	);
	const findKey = find?.key ?? null;
	const findIndex = findKey === null ? -1 : timelineRows.findIndex((row) => readerKey(row) === findKey);
	const findCurrent = findIndex < 0 ? null : findIndex;
	function stepFind(direction: 1 | -1) {
		if (!find) return;
		const next = stepMatch(findHits, findCurrent, direction);
		if (next !== null)
			setFind({
				...find,
				key: readerKey(timelineRows[next]),
				seeking: false,
				exhausted: false,
			});
		// Nothing older is loaded: look in older history.
		else if (direction === -1) setFind({ ...find, seeking: true, exhausted: false });
	}
	// While seeking, find the newest match older than the current one (or
	// the newest of all), loading one older page at a time until a match
	// appears or history ends.
	useEffect(() => {
		if (!isCurrentReader() || !find?.seeking || !focused || status !== "open" || !conversation) return;
		// A resumed search first reloads its saved boundary; newer matches
		// in the fresh window have already been searched.
		const next = find.key !== null && findCurrent === null ? null : stepMatch(findHits, findCurrent, -1);
		if (next !== null) {
			setFind({ ...find, key: readerKey(timelineRows[next]), seeking: false });
			return;
		}
		if (loadingOlder) return;
		const pageKey = olderPage;
		if (!pageKey) {
			// Stepped past the oldest match: say so. With none at all, the
			// label already reads "No matches".
			setFind({ ...find, seeking: false, exhausted: findHits.length > 0 });
			return;
		}
		// Failure or disconnection leaves the search incomplete. Demand resumes
		// when this reader is active, without another Find step.
		if (!service || !connected || olderHistory.permanent) return;
		// Paging older history reads away from the live end, so new rows stop
		// pulling the list down while find looks.
		if (follow.state.current.following) {
			findLeftTheEnd.current = true;
			follow.dispatch({ type: "unfollow" });
		}
		findOlder();
	});
	// A search that ends with no match, or find closing, gives the end back to
	// a list find unfollowed, when the list is still there. A jump to a match
	// is reading, and keeps it.
	const findLeftTheEnd = useRef(false);
	useEffect(() => {
		if (!findLeftTheEnd.current || (find !== null && (find.seeking || find.key !== null))) return;
		findLeftTheEnd.current = false;
		const stillAtEnd = atEnd({
			contentOffset: { y: listOffset.current },
			contentSize: { height: readerContentHeight.current },
			layoutMeasurement: { height: readerViewportHeight.current },
			contentInset: listUnderBar.contentInset,
		});
		if (stillAtEnd) follow.dispatch({ type: "follow" });
	});
	// The current match comes into view, 30% down the list. A row the list
	// hasn't measured fails the jump (onScrollToIndexFailed, while
	// findJumping holds); the list then moves near it and tries again, for as
	// long as each try measures rows closer to it, as the reader's restore
	// does (ReaderRestoreAttempts.retryUnmeasured). A retry belongs to the
	// match it started for: a new match or closing find cancels it, and it
	// finds its row again when it runs, since older pages may have prepended.
	const findJumping = useRef(false);
	const findAttempts = useRef(new ReaderRestoreAttempts());
	const findRetryFrame = useRef<number | null>(null);
	const findCurrentNow = useRef(findCurrent);
	useEffect(() => {
		findCurrentNow.current = findCurrent;
	});
	function cancelFindRetry() {
		if (findRetryFrame.current !== null) cancelAnimationFrame(findRetryFrame.current);
		findRetryFrame.current = null;
	}
	function retryFindMatch() {
		findRetryFrame.current = null;
		if (findCurrentNow.current !== null) scrollToFindMatch(findCurrentNow.current);
	}
	function scrollToFindMatch(index: number) {
		findLeftTheEnd.current = false;
		follow.dispatch({ type: "unfollow" });
		// The reading position follows the jump, so nothing pulls the list back.
		captureSuppressed.current = false;
		findJumping.current = true;
		timeline.current?.scrollToIndex({ index, viewPosition: 0.3, animated: true });
		findJumping.current = false;
	}
	// biome-ignore lint/correctness/useExhaustiveDependencies: only a new current match scrolls; rows prepending above it keep it in view.
	useEffect(() => {
		cancelFindRetry();
		findAttempts.current.reset();
		if (findCurrent !== null) scrollToFindMatch(findCurrent);
		return cancelFindRetry;
	}, [findKey]);
	return { findHits, findKey, findCurrent, stepFind, findJumping, findAttempts, findRetryFrame, retryFindMatch };
}
