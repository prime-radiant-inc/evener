// A pick's confirmation belongs to the surface the reader comes back to. The
// Model sheet can be opened from the conversation or from the Session sheet,
// and the two return the reader somewhere different: the conversation's toast
// sits under a full-height Session sheet, where nobody sees it. The sheet that
// opened the picker registers a sink for its session and answers whether it is
// the visible destination at the moment the pick lands; a pick whose sheet is
// gone or covered by another conversation declines, and the caller falls back
// to the conversation's own toast.
import { useEffect, useRef } from "react";
import type { ToastMessage } from "../Toast";

/** Shows the message on the surface that opened the picker and returns true,
 * or returns false when that surface is not the reader's destination now. */
export type PickAnnouncer = (message: ToastMessage) => boolean;

const announcers = new Map<string, { owner: object; announce: PickAnnouncer }[]>();

export function providePickAnnouncer(key: string, owner: object, announce: PickAnnouncer): void {
	const list = announcers.get(key) ?? [];
	const mine = list.find((entry) => entry.owner === owner);
	if (mine) mine.announce = announce;
	else list.push({ owner, announce });
	announcers.set(key, list);
}

export function releasePickAnnouncer(key: string, owner: object): void {
	const list = announcers.get(key);
	if (!list?.some((entry) => entry.owner === owner)) return;
	const rest = list.filter((entry) => entry.owner !== owner);
	if (rest.length > 0) announcers.set(key, rest);
	else announcers.delete(key);
}

/** Offers the confirmation to the newest surface registered for the session.
 * The newest owner answers, as a host registry does: a session open twice
 * confirms on the copy on top. */
export function announcePick(key: string, message: ToastMessage): boolean {
	return announcers.get(key)?.at(-1)?.announce(message) ?? false;
}

/** Registers `announce` for `key` while the calling screen is mounted. */
export function useProvidePickAnnouncer(key: string, announce: PickAnnouncer): void {
	const owner = useRef({}).current;
	useEffect(() => {
		providePickAnnouncer(key, owner, announce);
		return () => releasePickAnnouncer(key, owner);
	}, [key, owner, announce]);
}
