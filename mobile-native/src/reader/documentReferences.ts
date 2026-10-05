// The documents a session's transcript names (spec 8.2, ruling 28): a path the
// agent writes in a message becomes a document chip under that message, and
// the session's own write of that file gives the chip its age. The Files
// sheet lists the same documents, plus the files the session wrote without
// naming them (spec 10.1).
import type { TurnModel } from "@evener/appwire-client";
import { cwdRelative, fileURLToPath } from "@evener/appwire-client/docContent";
import { lexer, type Token, type Tokens } from "marked";

/** A document the session named or wrote. */
export interface DocumentReference {
	/** The path inside the session's folder. */
	path: string;
	/** When the session last wrote it (a hub time); absent for a file it only named. */
	updatedAt?: string;
}

/** The tools that write a whole file or edit one in place, by their
 * `file_path` argument. apply_patch, the editing tool OpenAI sessions use,
 * names its files inside its `patch` text instead (patchedFiles). */
const WRITING_TOOLS = new Set(["write_file", "edit_file"]);

// A file name ends in a dot and a short extension: "plan.md", "retirement.go".
// A directory ("src/") or a bare word ("README") isn't a document.
const FILE_NAME = /[^/.\s][^/\s]*\.[A-Za-z0-9]{1,10}$/;

/** The file's path inside the session's folder, "./" dropped so two names for
 * one file agree; undefined outside the folder, where the hub serves nothing. */
export function documentPath(path: string, cwd: string): string | undefined {
	const inFolder = cwdRelative(path, cwd)?.replace(/^(?:\.\/)+/, "");
	return inFolder || undefined;
}

// What a code span or a link target names, when it names a file: no spaces,
// no scheme but file://, and a line suffix ("retirement.go:1977") dropped.
function namedFile(text: string): string | undefined {
	const value = text.trim();
	if (value === "" || /\s/.test(value)) return undefined;
	if (/^file:\/\//i.test(value)) return fileURLToPath(value) || undefined;
	if (/^[a-z][a-z0-9+.-]*:/i.test(value)) return undefined;
	const file = value.replace(/:\d+(?::\d+)?$/, "");
	return FILE_NAME.test(file) ? file : undefined;
}

// Inline code and link targets, in reading order. A link's text is read too:
// "[`docs/plan.md`](https://github.com/…/docs/plan.md)" names the local file.
function visit(tokens: readonly Token[], found: (text: string) => void): void {
	for (const token of tokens) {
		if (token.type === "codespan") found((token as Tokens.Codespan).text);
		if (token.type === "link") found((token as Tokens.Link).href);
		if (token.type === "list") for (const item of (token as Tokens.List).items) visit(item.tokens, found);
		else if (token.type === "table") {
			const table = token as Tokens.Table;
			for (const cell of [...table.header, ...table.rows.flat()]) visit(cell.tokens, found);
		} else if ("tokens" in token && Array.isArray(token.tokens)) visit(token.tokens, found);
	}
}

// Every publish hands the screen new turns, so a transcript's settled
// messages are read again and again. Lexing is the costly part, so the file
// names each markdown text holds are remembered, for the most recently used
// texts.
const NAMES_REMEMBERED = 500;
const namesByMarkdown = new Map<string, readonly string[]>();

function namedFiles(markdown: string): readonly string[] {
	const known = namesByMarkdown.get(markdown);
	if (known) {
		// Used again: it moves to the newest end, so the oldest unused go first.
		namesByMarkdown.delete(markdown);
		namesByMarkdown.set(markdown, known);
		return known;
	}
	const names: string[] = [];
	visit(lexer(markdown), (text) => {
		const file = namedFile(text);
		if (file !== undefined) names.push(file);
	});
	namesByMarkdown.set(markdown, names);
	for (const oldest of namesByMarkdown.keys()) {
		if (namesByMarkdown.size <= NAMES_REMEMBERED) break;
		namesByMarkdown.delete(oldest);
	}
	return names;
}

/** The documents one message names, in order, each once: inline code and
 * link targets that name a file inside the session's folder. A name needs a
 * directory ("docs/plan.md") unless the session wrote that file, so a passing
 * "README.md" never becomes a chip for a file that isn't there. Fenced code
 * is code, not a reference. `written` is the paths the session wrote, whatever
 * their write time. */
export function messageDocuments(markdown: string, cwd: string, written: ReadonlySet<string>): string[] {
	const paths: string[] = [];
	for (const file of namedFiles(markdown)) {
		const path = documentPath(file, cwd);
		if (path === undefined || paths.includes(path)) continue;
		if (file.includes("/") || written.has(path)) paths.push(path);
	}
	return paths;
}

function argument(argumentsJSON: string | undefined, name: string): string | undefined {
	if (!argumentsJSON) return undefined;
	try {
		const args: unknown = JSON.parse(argumentsJSON);
		const value = typeof args === "object" && args !== null ? (args as Record<string, unknown>)[name] : undefined;
		return typeof value === "string" ? value : undefined;
	} catch {
		return undefined;
	}
}

// The files a v4a patch leaves behind (agent/internal/tool/apply_patch.go):
// each file it adds or updates, under its new name when "*** Move to:"
// follows. A deleted file is gone, so it isn't one.
function patchedFiles(patch: string): string[] {
	const files: string[] = [];
	const lines = patch.split("\n");
	for (const [index, line] of lines.entries()) {
		const added = /^\*\*\* Add File: (.+)$/.exec(line);
		const updated = /^\*\*\* Update File: (.+)$/.exec(line);
		const moved = updated ? /^\*\*\* Move to: (.+)$/.exec(lines[index + 1] ?? "") : null;
		const file = (moved ?? added ?? updated)?.[1]?.trim();
		if (file) files.push(file);
	}
	return files;
}

/** The files one successful tool call wrote, inside the session's folder. */
function writtenFiles(item: TurnModel["items"][number], cwd: string): string[] {
	if (!item.toolName || item.error !== undefined) return [];
	let named: string[] = [];
	if (WRITING_TOOLS.has(item.toolName)) {
		const file = argument(item.argumentsJSON, "file_path");
		named = file === undefined ? [] : [file];
	} else if (item.toolName === "apply_patch") named = patchedFiles(argument(item.argumentsJSON, "patch") ?? "");
	return named.flatMap((file) => documentPath(file, cwd) ?? []);
}

function later(a: string | undefined, b: string | undefined): string | undefined {
	if (a === undefined) return b;
	if (b === undefined) return a;
	return Date.parse(b) > Date.parse(a) ? b : a;
}

/** When the session last wrote each file inside its folder: the newest
 * successful write_file, edit_file or apply_patch, by the call's completion
 * time (or its turn's, for a call that carries none). A write with no time
 * that parses gives no age. */
export function fileWrites(turns: readonly TurnModel[], cwd: string): Map<string, string> {
	const writes = new Map<string, string>();
	for (const turn of turns)
		for (const item of turn.items) {
			// ISO strings: the reducer converts the wire's epoch milliseconds.
			const at = item.completedAt ?? turn.completedAt;
			if (at === undefined || !Number.isFinite(Date.parse(at))) continue;
			for (const path of writtenFiles(item, cwd)) writes.set(path, later(writes.get(path), at) ?? at);
		}
	return writes;
}

/** The paths the session wrote inside its folder, with or without a write time:
 * a bare file name in a message becomes a chip only for one of these. */
export function writtenPaths(turns: readonly TurnModel[], cwd: string): Set<string> {
	const paths = new Set<string>();
	for (const turn of turns) for (const item of turn.items) for (const path of writtenFiles(item, cwd)) paths.add(path);
	return paths;
}

/** Every document the session named in its messages or wrote, each once, in
 * the order it first appeared, with its newest write. */
export function documentReferences(turns: readonly TurnModel[], cwd: string): DocumentReference[] {
	const writes = fileWrites(turns, cwd);
	const written = writtenPaths(turns, cwd);
	const order: string[] = [];
	const add = (path: string) => {
		if (!order.includes(path)) order.push(path);
	};
	for (const turn of turns)
		for (const item of turn.items) {
			if (item.type === "agentMessage") for (const path of messageDocuments(item.text, cwd, written)) add(path);
			else for (const path of writtenFiles(item, cwd)) add(path);
		}
	return order.map((path) => {
		const updatedAt = writes.get(path);
		return updatedAt === undefined ? { path } : { path, updatedAt };
	});
}
