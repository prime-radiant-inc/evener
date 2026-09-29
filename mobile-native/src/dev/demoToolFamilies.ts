// The recorded wire corpora (agent/testdata/*wire), replayed as the items of
// one demo session, so every tool family the phone summarizes can be seen
// and screenshotted in the shapes the daemon really sends rather than in
// hand-written ones. Each corpus is what apptranscript projects for a
// recorded run, served as it is: a tool call's announcement (in progress)
// and its result are two items sharing a callId, as a transcript read serves
// them. The package folds the two into one step by their ids' shape
// (item_tool_* and item_tool_result_*, reducer.ts), so each item keeps its
// recorded id, tagged so ids reused across and within corpora stay apart.
import { readFileSync } from "node:fs";
import type { ThreadItem } from "@evener/appwire-client";

/** One corpus file under agent/testdata. */
function corpus(file: string): unknown {
	return JSON.parse(readFileSync(new URL(`../../../agent/testdata/${file}`, import.meta.url), "utf8"));
}

type Cases = { item: ThreadItem }[];

/** A corpus's items, each id tagged with the corpus and its place there,
 * so none collides: the corpora, and the cases within one, reuse ids. */
function tagged(items: ThreadItem[], name: string): ThreadItem[] {
	return items.map((item, index) => ({ ...item, id: `${item.id}_${name}_${index}` }));
}

/** Every recorded item: the core tools, then subagents and the task list,
 * then subagent and job notifications, then system events and steers. */
export function recordedToolFamilies(): ThreadItem[] {
	const tools = corpus("toolwire/calls.json") as { items: ThreadItem[] };
	const subagents = corpus("subagentwire/calls.json") as { items: ThreadItem[] };
	const notifications = corpus("notificationwire/steering.json") as Cases;
	const events = corpus("systemeventwire/events.json") as { items: Cases };
	return [
		...tagged(tools.items, "toolwire"),
		...tagged(subagents.items, "subagentwire"),
		...tagged(
			notifications.map((recorded) => recorded.item),
			"notificationwire",
		),
		...tagged(
			events.items.map((recorded) => recorded.item),
			"systemeventwire",
		),
	];
}
