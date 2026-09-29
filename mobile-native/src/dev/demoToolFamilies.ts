// The recorded wire corpora (agent/testdata/*wire), replayed as the items of
// one demo session, so every tool family the phone summarizes can be seen
// and screenshotted in the shapes the daemon really sends rather than in
// hand-written ones. Each corpus is what apptranscript projects for a
// recorded run, served as it is: a tool call's announcement (in progress)
// and its result are two items sharing a callId, as a transcript read serves
// them, and the phone pairs them.
import { readFileSync } from "node:fs";
import type { ThreadItem } from "@evener/appwire-client";

/** One corpus file under agent/testdata. */
function corpus(file: string): unknown {
	return JSON.parse(readFileSync(new URL(`../../../agent/testdata/${file}`, import.meta.url), "utf8"));
}

type Cases = { item: ThreadItem }[];

/** Every recorded item: the core tools, then subagents and the task list,
 * then subagent and job notifications, then system events and steers. */
export function recordedToolFamilies(): ThreadItem[] {
	const tools = corpus("toolwire/calls.json") as { items: ThreadItem[] };
	const subagents = corpus("subagentwire/calls.json") as { items: ThreadItem[] };
	const notifications = corpus("notificationwire/steering.json") as Cases;
	const events = corpus("systemeventwire/events.json") as { items: Cases };
	return [
		...tools.items,
		...subagents.items,
		...notifications.map((recorded) => recorded.item),
		...events.items.map((recorded) => recorded.item),
	];
}
