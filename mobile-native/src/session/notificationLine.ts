// What a delegate or job notification says in the transcript (spec 8.2, 9):
// one line naming who and what happened ("Fix race in tree settle finished"),
// at most two lines beneath (the report, the failure, the note), and what
// tapping it does: open the subagent, or show the job's output in place. Pure,
// so the copy rules live apart from how NotificationCards draws them.
import {
	type EvenerDelegateInfo,
	isNotificationRemnant,
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

function said(subject: string, outcome: NotificationOutcome | undefined, fallback?: string): string {
	const verb = outcome ? VERBS[outcome] : fallback;
	return verb ? `${subject} ${verb}` : subject;
}

/** What a steer's text between notifications reads as: itself, or, for
 * notification markup that didn't parse, a neutral line in its place. */
export function notificationText(text: string): string {
	return isNotificationRemnant(text) ? "A notification couldn't be read" : text;
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
				: // A report whose ending this client doesn't know still reported.
					{ headline: said(subject, notification.outcome, "reported"), failed };
			const detail = notification.message ?? (notification.excerpt || undefined);
			if (detail) line.detail = detail;
			if (ref) line.subagent = { ref, title: subject };
			return line;
		}
		case "job": {
			const subject = notification.intent || notification.description || "Background job";
			const line: NotificationLine = { headline: said(subject, notification.outcome), failed };
			if (failed) {
				// -1 is the daemon's signalled-not-exited sentinel, never an exit
				// status; the title already says the command was killed.
				const { exitCode } = notification;
				line.detail =
					exitCode === undefined || exitCode === -1 ? notification.title : `${notification.title} · exit ${exitCode}`;
			}
			// A delegate job's excerpt is its report's envelope; the parser has
			// already read the report out of it.
			const output = notification.message ?? notification.excerpt;
			if (output) line.output = output;
			return line;
		}
		case "watch": {
			// A watch's prose opens with the sentence its title already says
			// ("Timer fired after 300s."); what follows is the watch's own note.
			const note = (notification.prose ?? "").split("\n").slice(1).join("\n").trim();
			return { headline: notification.title, failed, ...(note ? { detail: note } : {}) };
		}
		default: {
			// A watch delivery's secondary, or a replayed observer callback's body.
			const detail = notification.secondary || notification.message || notification.excerpt;
			return { headline: notification.title, failed, ...(detail ? { detail } : {}) };
		}
	}
}
