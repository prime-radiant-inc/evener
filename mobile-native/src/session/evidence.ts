// What a step in a run has to show when you tap it (spec 8.2): an edit's
// diff, the file a write wrote, a command's output, a fetched page, the skill
// an activation loaded, a task list, what a message to a subagent sent and
// what came back, a tool's arguments and result, and an error. Each reads the
// words the tool printed, not the envelope around them (the package's
// toolEvidence and delegateSteps readers, which the web's bodies read too).
// Pure, so the rules live apart from how StepEvidence draws them.
import {
	capitalized,
	delegateSendEarlierLabel,
	diffStats,
	editDiffText,
	filePathOf,
	jobStatusDisplay,
	freshNotes,
	lineCount,
	memoryFileTarget,
	parseArgs,
	prettyJSON,
	readTranscriptEnvelope,
	type ShellOutput,
	shellOutput,
	skillContext,
	str,
	toolFamily,
	toolJSONResult,
	turns,
	webFetchResult,
	worktreeMessage,
} from "@evener/appwire-client";
import type { ActivityDetail, DetailTask } from "../projectedRows";
import type { RunStep } from "../timeline";

export type Evidence =
	| { kind: "output"; text: string; lines: number }
	| { kind: "diff"; text: string; added: number; removed: number }
	| { kind: "wrote"; path: string }
	// A command that exited nonzero with no error of its own.
	| { kind: "exit"; code: number }
	// What the shell tool's footer says besides the exit: a timed-out wait, a
	// command still running, an output only partly here.
	| { kind: "note"; text: string }
	// A fetched page: the model's answer (or the page's content), from where.
	| { kind: "page"; text: string; url?: string; bytes?: number }
	// Markdown with a heading: the instructions a skill loaded.
	| { kind: "markdown"; title: string; markdown: string }
	// The task list a task_list call returned, each task with the note this
	// call added to it.
	| { kind: "tasks"; tasks: ChecklistTask[] }
	// A tool's arguments or result, pretty-printed.
	| { kind: "json"; label: "Arguments" | "Result"; text: string }
	| { kind: "error"; text: string; exitCode?: number };

/** A task in a task_list step's checklist, with the note the call added. */
export type ChecklistTask = DetailTask & { note?: string };

/** Output lines shown in the transcript before "Show all N lines". */
export const EVIDENCE_PREVIEW_LINES = 40;

// A file tool's output is a confirmation ("edited a.go: 1 replacement(s)",
// "wrote 12 bytes to a.go") that only repeats what its diff or path says. The
// file tools are the package's edit family.
const isFileTool = (label: string) => toolFamily(label) === "edit";

function diff(text: string): Evidence {
	return { kind: "diff", text, ...diffStats(text) };
}

// A tool's output as it printed it, or nothing for an empty one.
function rawOutput(text: string): Evidence[] {
	return text ? [{ kind: "output", text, lines: lineCount(text) }] : [];
}

// What the shell tool's footer says besides the exit.
function shellNotes(run: ShellOutput): Evidence[] {
	const notes: Evidence[] = [];
	if (run.windowed) notes.push({ kind: "note", text: "A long output: only its start and end are here" });
	if (run.stillRunning)
		notes.push({
			kind: "note",
			text: run.timedOut
				? "Still running in the background after its wait timed out"
				: "Still running in the background",
		});
	else if (run.timedOut) notes.push({ kind: "note", text: "Timed out" });
	return notes;
}

// A code the job tools print ("cancelled_by_request") in words ("cancelled by
// request"). A local stand-in until a shared code-to-words helper lands
// (#3327); swap this for it then.
const JOB_CODE_RE = /^[a-z]+(?:_[a-z]+)+$/;
function codeInWords(text: string): string {
	return JOB_CODE_RE.test(text) ? text.replace(/_/g, " ") : text;
}

// A line's trailing bracketed codes in words: a stop's footer, a listing
// row's "[<started · reason · exit · bytes>]".
function bracketCodesInWords(line: string): string {
	const bracket = /\[([^\]]*)\]\s*$/.exec(line);
	if (!bracket) return line;
	return `${line.slice(0, bracket.index)}[${(bracket[1] ?? "").split(" · ").map(codeInWords).join(" · ")}]`;
}

// A job listing's codes in words: each row's status column and bracketed
// codes. Only those: a command in a row's label keeps its own spelling
// ("tree_order.go"). A row reads "<id>  <type>  <status>  <label>
// [<started · reason · exit · bytes>]", its header "# …".
function jobListInWords(text: string): string {
	return text
		.split("\n")
		.map((line) => {
			const columns = bracketCodesInWords(line).split("  ");
			if (columns.length >= 4 && !line.startsWith("#")) columns[2] = codeInWords(columns[2] ?? "");
			return columns.join("  ");
		})
		.join("\n");
}

// A job stop's footer, its first line, with its codes in words. A delegate's
// provenance lines under it (who asked, a scratch path, live watches) read
// as printed.
function jobStopInWords(text: string): string {
	const [footer = "", ...rest] = text.split("\n");
	return [bracketCodesInWords(footer), ...rest].join("\n");
}

// What a tool's output shows, by its family: a command without its exit
// footer, a fetched page's answer, a skill's instructions, a task list as a
// checklist, a transcript a read returned, what a worktree operation says it
// did, a job check or watch in words, a message to a subagent as the message
// and its reply, an MCP or other tool's JSON pretty-printed; anything else as
// the tool printed it.
function outputEvidence(label: string, detail: EvidenceSource["detail"]): Evidence[] {
	const text = detail.output ?? "";
	switch (toolFamily(label)) {
		case "shell": {
			const run = shellOutput(text);
			const evidence = rawOutput(run.text);
			const code = detail.exitCode ?? run.exitCode;
			// An error of its own says the exit code with it (below). -1 is the
			// shell tool's sentinel for a command stopped by a signal or by
			// evener's runtime limit, not an exit code.
			if (code !== undefined && code !== 0 && code !== -1 && !detail.error) evidence.push({ kind: "exit", code });
			evidence.push(...shellNotes(run));
			return evidence;
		}
		case "fetch": {
			const page = webFetchResult(text);
			return page?.text === undefined ? rawOutput(text) : [{ kind: "page", ...page, text: page.text }];
		}
		case "skill": {
			const loaded = skillContext(text);
			return loaded ? [{ kind: "markdown", title: loaded.name, markdown: loaded.instructions }] : rawOutput(text);
		}
		case "tasks": {
			// No list, or one with no tasks in it: what the tool printed says more
			// than an empty checklist.
			if (!detail.tasks?.length) return rawOutput(text);
			const notes = freshNotes({ argumentsJSON: detail.arguments });
			const tasks = detail.tasks.map(({ id, status, description }) => {
				const note = notes.get(id);
				return { id, status, description, ...(note === undefined ? {} : { note }) };
			});
			return [{ kind: "tasks", tasks }];
		}
		case "transcript": {
			// The transcript itself, as the web's body shows it, and how many
			// turns the read's budget left out.
			const envelope = readTranscriptEnvelope({ output: text });
			if (envelope?.content === undefined) return rawOutput(text);
			const evidence = rawOutput(envelope.content.replace(/\n+$/, ""));
			const elided = envelope.elidedTurns ?? 0;
			if (elided > 0) evidence.push({ kind: "note", text: `${turns(elided)} left out by the read's budget` });
			// An empty read with nothing left out: what the tool printed.
			return evidence.length > 0 ? evidence : rawOutput(text);
		}
		case "worktree":
			// What the operation says it did, not the JSON around it.
			return rawOutput(worktreeMessage(text) ?? text);
		case "jobs": {
			// A job check: its status and what it runs, not the JSON around
			// them. A watch's rows, trigger and note in words, not the
			// footer around them. A list and a stop print lines of their own,
			// their codes in words; anything else reads as printed.
			if (detail.watchEvidence !== undefined) return rawOutput(detail.watchEvidence);
			if (label === "job_list") return rawOutput(jobListInWords(text));
			if (label === "job_stop") return rawOutput(jobStopInWords(text));
			const job = toolJSONResult(text);
			const status = job ? str(job, "status") : undefined;
			if (!job || !status) return rawOutput(text);
			const description = str(job, "description");
			const line = jobStatusDisplay(status, str(job, "reason"));
			return rawOutput(description ? `${line} — ${description}` : line);
		}
		case "message": {
			// A message to a subagent: what was sent, any earlier results its
			// wait carried, the delegate's reply when the send waited for one,
			// and why a wait was ignored, as the web's
			// DelegateSendBody shows the exchange. A call with no message and no
			// reply (a malformed one) shows its JSON, as any other tool's does.
			const message = str(parseArgs(detail.arguments), "message");
			const earlier = detail.sendEarlierReplies ?? [];
			if (!message && detail.sendReply === undefined && earlier.length === 0) return jsonEvidence(detail, text);
			const evidence: Evidence[] = [];
			if (message) evidence.push({ kind: "markdown", title: "Message", markdown: message });
			// Results the caller had not yet received, which the wait carried
			// ahead of its own reply (#3906), oldest first.
			earlier.forEach((reply, i) => {
				const title = capitalized(delegateSendEarlierLabel(reply, i, earlier.length));
				evidence.push({ kind: "markdown", title, markdown: reply.text });
			});
			if (detail.sendReply !== undefined)
				evidence.push({ kind: "markdown", title: "Reply", markdown: detail.sendReply });
			if (detail.sendWaitIgnored !== undefined)
				evidence.push({ kind: "note", text: `Wait ignored: ${detail.sendWaitIgnored}` });
			return evidence;
		}
		case "mcp":
		case "tool":
			return jsonEvidence(detail, text);
		default:
			return rawOutput(text);
	}
}

// A tool's arguments and result, pretty-printed when they're JSON.
function jsonEvidence(detail: ActivityDetail, text: string): Evidence[] {
	const args = detail.arguments ? prettyJSON(detail.arguments) : undefined;
	const result = text ? prettyJSON(text) : undefined;
	// Arguments that aren't a JSON object or array show as they were sent, as
	// the web's MCPToolArguments shows them.
	const evidence: Evidence[] = args
		? [{ kind: "json", label: "Arguments", text: args }]
		: rawOutput(detail.arguments?.trim() ? detail.arguments : "");
	if (result) evidence.push({ kind: "json", label: "Result", text: result });
	else evidence.push(...rawOutput(text));
	return evidence;
}

// A memory write's content is the page the person asked to save; an edit is
// the same file diff as edit_file, and other calls read as printed.
function memoryEvidence(label: string, detail: ActivityDetail): Evidence[] {
	const text = detail.output ?? "";
	switch (label) {
		case "memory_write": {
			const args = parseArgs(detail.arguments);
			const content = str(args, "content");
			if (!content) return jsonEvidence(detail, text);
			return [{ kind: "markdown", title: memoryFileTarget(args) ?? "Memory page", markdown: content }];
		}
		case "memory_edit": {
			const args = parseArgs(detail.arguments);
			const oldString = str(args, "old_string");
			const newString = str(args, "new_string");
			if (oldString === undefined && newString === undefined) return jsonEvidence(detail, text);
			return [diff(editDiffText(filePathOf(args) ?? "", oldString ?? "", newString ?? ""))];
		}
		case "memory_read":
		case "memory_search":
		case "memory_delete":
			return rawOutput(text);
		default:
			return jsonEvidence(detail, text);
	}
}

/** The parts of a step its evidence comes from. */
export type EvidenceSource = Pick<RunStep, "label" | "summaryOnly"> & {
	detail: ActivityDetail;
};

export function stepEvidence(step: EvidenceSource): Evidence[] {
	// At Intent a settled step shows only its summary (the summary-only ruling).
	if (step.summaryOnly) return [];
	const { detail } = step;
	const evidence: Evidence[] = [];
	if (toolFamily(step.label) === "memory") {
		evidence.push(...memoryEvidence(step.label, detail));
	} else if (isFileTool(step.label)) {
		const args = parseArgs(detail.arguments);
		const path = filePathOf(args);
		if (step.label === "edit_file") {
			const oldString = str(args, "old_string");
			const newString = str(args, "new_string");
			// Arguments that carry neither side (a malformed call) have no diff.
			if (oldString !== undefined || newString !== undefined)
				evidence.push(diff(editDiffText(path ?? "", oldString ?? "", newString ?? "")));
		} else if (step.label === "apply_patch") {
			const patch = str(args, "patch");
			if (patch) evidence.push(diff(patch));
		} else if (path) {
			evidence.push({ kind: "wrote", path });
		}
	} else {
		evidence.push(...outputEvidence(step.label, detail));
	}
	if (detail.error) {
		evidence.push({
			kind: "error",
			text: detail.error,
			...(detail.exitCode === undefined ? {} : { exitCode: detail.exitCode }),
		});
	}
	return evidence;
}
