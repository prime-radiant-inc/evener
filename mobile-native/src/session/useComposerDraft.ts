import { type RefObject, useEffect, useMemo, useSyncExternalStore } from "react";
import { ImageSelection } from "../imageSelection";
import { drafts } from "../nativeDrafts";
import { nativeImagePicker } from "../nativeImagePicker";

/** The session's composer draft document, its handoff retry and its staged
 * images. `abortOnLeave` is aborted with the image selection when the screen
 * leaves the front. */
export function useComposerDraft({
	hubId,
	sessionRef,
	focused,
	abortOnLeave,
}: {
	hubId: string;
	sessionRef: string;
	focused: boolean;
	abortOnLeave: RefObject<AbortController | null>;
}) {
	const document = useMemo(() => drafts.open({ hubId: hubId, sessionRef: sessionRef }), [hubId, sessionRef]);
	const draft = useSyncExternalStore(document.subscribe, document.getSnapshot);
	// A handoff can fail its storage write while the resume itself already moved
	// the rows. Retry it as the replacement screen mounts and whenever the draft
	// becomes writable again; the source composition stays under the old ref
	// until the destination write commits.
	useEffect(() => {
		if (draft.loaded && !draft.error) drafts.retryPendingHandoffs();
	}, [draft.loaded, draft.error, draft.record]);
	const imageSelection = useMemo(() => new ImageSelection(document, nativeImagePicker), [document]);
	const imageState = useSyncExternalStore(imageSelection.subscribe, imageSelection.getSnapshot);
	// Follows the screen in front rather than focus, so a photo being attached
	// survives one of the screen's own sheets opening.
	useEffect(() => {
		if (!focused) return;
		return () => {
			imageSelection.cancel();
			abortOnLeave.current?.abort();
		};
	}, [focused, imageSelection]);
	return { document, draft, imageSelection, imageState };
}
