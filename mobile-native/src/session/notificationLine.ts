// What a delegate or job notification says in the transcript (spec 8.2, 9):
// one line naming who and what happened ("Fix race in tree settle finished"),
// at most two lines beneath (the report, the failure, the note), and what
// tapping it does: open the subagent, or show the job's output in place. Pure,
// so the copy rules live apart from how NotificationCards draws them.
import {
	decodeNotificationEntities,
	type EvenerDelegateInfo,
	type NotificationOutcome,
	type ParsedNotification,
} from "@evener/appwire-client";
import { delegateTitle } from "./subagentLine";

export interface NotificationLine {
	headline: string;
	detail?: string;
	failed: boolean;
	/** The subagent tapping opens: its own transcript, and its title. */
	subagent?: { ref: string; title: string };
	/** A job's output, shown in place when tapped. */
	output?: string;
}

const VERBS: Record<NotificationOutcome, string> = {
	completed: "finished",
	failed: "failed",
	stopped: "stopped",
};

function said(subject: string, outcome: NotificationOutcome | undefined): string {
	return outcome ? `${subject} ${VERBS[outcome]}` : subject;
}

export function notificationLine(
	notification: ParsedNotification,
	delegates: readonly EvenerDelegateInfo[] | undefined,
): NotificationLine {
	const failed = notification.outcome === "failed";
	switch (notification.type) {
		case "delegate": {
			const subagent = delegates?.find((candidate) => candidate.delegateId === notification.delegateId);
			const subject = notification.name || delegateTitle(subagent) || notification.description || "Subagent";
			const ref = subagent?.transcriptRef ?? notification.transcriptRef;
			const line: NotificationLine = notification.quiet
				? { headline: `${subject} quiet · ${notification.quiet.window}`, failed }
				: { headline: said(subject, notification.outcome), failed };
			const detail = notification.message ?? (decodeNotificationEntities(notification.excerpt) || undefined);
			if (detail) line.detail = detail;
			if (ref) line.subagent = { ref, title: subject };
			return line;
		}
		case "job": {
			const subject = notification.intent || notification.description || "Background job";
			const line: NotificationLine = { headline: said(subject, notification.outcome), failed };
			if (failed) {
				line.detail =
					notification.exitCode === undefined
						? notification.title
						: `${notification.title} · exit ${notification.exitCode}`;
			}
			const output = decodeNotificationEntities(notification.excerpt);
			if (output) line.output = output;
			return line;
		}
		case "watch": {
			// A watch's prose opens with the sentence its title already says
			// ("Timer fired after 300s."); what follows is the watch's own note.
			const note = decodeNotificationEntities(notification.prose ?? "")
				.split("\n")
				.slice(1)
				.join("\n")
				.trim();
			return { headline: notification.title, failed, ...(note ? { detail: note } : {}) };
		}
		default:
			return {
				headline: notification.title,
				failed,
				...(notification.secondary ? { detail: notification.secondary } : {}),
			};
	}
}
