// What the Reader can show for a document, and the one sentence it says when
// it can't show the text (spec 10.2; Review Focus 5). Text comes from the
// hub's /doc/file route through readDocFile and the native doc port; images
// render from /doc/image, which reaches other hosts' sessions already.
import { lineCount } from "@evener/appwire-client";
import {
	type DocFileContent,
	DocFileError,
	type DocPort,
	filenameOf,
	isImagePath,
	isMarkdownPath,
	readDocFile,
} from "@evener/appwire-client/docContent";
import { type DocumentBlock, documentBlocks, documentTitle } from "./documentBlocks";

export type DocumentKind = "Plan" | "Spec" | "Doc" | "Code" | "Image";

/** The label a document chip, a Files row and the Reader's caption show
 * (ruling 19). */
export function documentKind(path: string): DocumentKind {
	if (isImagePath(path)) return "Image";
	if (!isMarkdownPath(path)) return "Code";
	if (/(^|\/)plans\//i.test(path)) return "Plan";
	if (/(^|\/)specs\//i.test(path)) return "Spec";
	return "Doc";
}

/** The host a session lives on, from its ref: "local" for the hub's own. */
export function refHost(ref: string): string {
	const colon = ref.indexOf(":");
	return colon > 0 ? ref.slice(0, colon) : "";
}

export interface Truncation {
	shownBytes: number;
	totalBytes?: number;
}

export type LoadedDocument =
	| { kind: "markdown"; title: string; text: string; blocks: DocumentBlock[]; lines: number; truncated?: Truncation }
	| { kind: "code"; title: string; text: string; lines: number; truncated?: Truncation }
	| { kind: "image"; title: string }
	| { kind: "binary"; title: string; sizeBytes: number }
	| { kind: "host-unsupported" | "missing" | "forbidden" | "failed"; title: string };

/** Reads the captured target through the owning hub, including remote sessions. */
export async function loadDocument(port: DocPort, sessionRef: string, path: string): Promise<LoadedDocument> {
	const title = filenameOf(path);
	if (documentKind(path) === "Image") return { kind: "image", title };
	let content: DocFileContent;
	try {
		content = await readDocFile(sessionRef, path, port);
	} catch (error) {
		if (error instanceof DocFileError && error.kind === "not-found") return { kind: "missing", title };
		if (error instanceof DocFileError && error.kind === "forbidden") return { kind: "forbidden", title };
		if (error instanceof DocFileError && error.kind === "host-unsupported") return { kind: "host-unsupported", title };
		return { kind: "failed", title };
	}
	if (content.binary) return { kind: "binary", title, sizeBytes: content.totalBytes ?? content.sizeBytes };
	const truncated: { truncated?: Truncation } = content.truncated
		? {
				truncated: {
					shownBytes: content.sizeBytes,
					...(content.totalBytes === undefined ? {} : { totalBytes: content.totalBytes }),
				},
			}
		: {};
	const lines = lineCount(content.text);
	if (!isMarkdownPath(path)) return { kind: "code", title, text: content.text, lines, ...truncated };
	const blocks = documentBlocks(content.text);
	return { kind: "markdown", title: documentTitle(blocks, path), text: content.text, blocks, lines, ...truncated };
}

/** The one sentence the Reader says when it can't show a document's text. */
export function documentNotice(document: LoadedDocument): string | null {
	switch (document.kind) {
		case "host-unsupported":
			return "This host does not support document reads yet.";
		case "missing":
			return `${document.title} isn't in this session's folder any more.`;
		case "forbidden":
			return `${document.title} is outside this session's folder, so it can't be shown.`;
		case "failed":
			return `${document.title} couldn't be loaded right now.`;
		case "binary":
			return `${document.title} isn't text, so it can't be shown here (${byteSize(document.sizeBytes)}).`;
		default:
			return null;
	}
}

/** Sizes the way the spec writes them: "512 KB", "1.3 MB". */
export function byteSize(bytes: number): string {
	if (bytes < 1024) return `${bytes} ${bytes === 1 ? "byte" : "bytes"}`;
	if (bytes < 1024 * 1024) return `${Math.floor(bytes / 1024)} KB`;
	return `${(bytes / (1024 * 1024)).toFixed(1).replace(/\.0$/, "")} MB`;
}

/** "Showing the first 512 KB of 1.3 MB" (spec 10.2). */
export function truncationNote(truncation: Truncation): string {
	const shown = `Showing the first ${byteSize(truncation.shownBytes)}`;
	return truncation.totalBytes === undefined ? shown : `${shown} of ${byteSize(truncation.totalBytes)}`;
}
