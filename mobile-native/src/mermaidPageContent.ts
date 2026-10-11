// The bootstrap the native mermaid WebView page runs. The generator
// (scripts/build-mermaid-page.mts) bundles this module together with mermaid
// and DOMPurify into one self-contained HTML page emitted as
// src/generated/mermaidPage.ts. This file is never imported by the app itself;
// it exists so the bootstrap is real, typechecked, lintable TypeScript.
import DOMPurify from "dompurify";
import mermaid from "mermaid";

declare global {
	interface Window {
		ReactNativeWebView?: { postMessage: (data: string) => void };
	}
}

// The forbid list is not a literal here: the generator
// (scripts/build-mermaid-page.mts) imports MERMAID_FORBID_TAGS from the web
// security module (cmd/evener-hub/frontend/src/widgets/mermaid/security.ts -
// the one source) and substitutes it into this bundle via esbuild's define, so
// the two frontends cannot drift. Resource-bearing and navigational tags are
// forbidden at BOTH layers here too — mermaid's own sanitize (dompurifyConfig
// below) and this post-render pass. The render-time network fetch (mermaid's
// temporary render DOM) can only be stopped at the mermaid layer; the
// post-render pass covers markup passed through labels.
declare const MERMAID_FORBID_TAGS: readonly string[];

// Same profile config as the web sanitizer (MERMAID_SANITIZE_CONFIG in the
// module above): html + svg + svgFilters keeps mermaid's <foreignObject> label
// XHTML, "role" keeps role="graphics-document document" for screen readers.
const MERMAID_SANITIZE_CONFIG = {
	USE_PROFILES: { html: true, svg: true, svgFilters: true },
	ADD_TAGS: ["foreignObject"],
	HTML_INTEGRATION_POINTS: { foreignobject: true },
	ADD_ATTR: ["role"],
	FORBID_TAGS: [...MERMAID_FORBID_TAGS],
};

interface RenderMessage {
	type: "render";
	// The host's monotonically increasing render id, echoed on this render's
	// reply so the host can drop a reply that a newer source has superseded.
	id: number;
	source: string;
	theme: Record<string, string>;
	mode: "fit" | "zoom";
}

function postToHost(message: unknown): void {
	window.ReactNativeWebView?.postMessage(JSON.stringify(message));
}

// "zoom" is the fullscreen viewer: allow pinch-zoom. "fit" is the inline
// diagram: lock the viewport to the WebView's own width.
function setViewportScale(mode: RenderMessage["mode"]): void {
	const meta = document.querySelector('meta[name="viewport"]');
	if (!meta) return;
	meta.setAttribute(
		"content",
		mode === "zoom"
			? "width=device-width, initial-scale=1, maximum-scale=5, user-scalable=yes"
			: "width=device-width, initial-scale=1, maximum-scale=1, user-scalable=no",
	);
}

async function renderDiagram(message: RenderMessage): Promise<void> {
	try {
		setViewportScale(message.mode);
		mermaid.initialize({
			startOnLoad: false,
			securityLevel: "strict",
			theme: "default",
			themeVariables: message.theme ?? {},
			dompurifyConfig: { FORBID_TAGS: [...MERMAID_FORBID_TAGS] },
		});
		// Fixed id: mermaid removes its temporary render element each call.
		const { svg } = await mermaid.render("mermaid-diagram", message.source);
		const container = document.getElementById("mermaid-container");
		if (!container) throw new Error("mermaid container missing");
		container.innerHTML = DOMPurify.sanitize(svg, MERMAID_SANITIZE_CONFIG);
		postToHost({ type: "height", value: container.scrollHeight, id: message.id });
	} catch (error) {
		postToHost({ type: "error", message: error instanceof Error ? error.message : String(error), id: message.id });
	}
}

function onHostMessage(event: MessageEvent): void {
	if (typeof event.data !== "string") return;
	let message: unknown;
	try {
		message = JSON.parse(event.data);
	} catch {
		return;
	}
	if (message !== null && typeof message === "object" && (message as { type?: unknown }).type === "render") {
		void renderDiagram(message as RenderMessage);
	}
}

export function bootstrapMermaidPage(): void {
	// RN delivers postMessage as a "message" event on document (Android) or
	// window (iOS); listen on both.
	document.addEventListener("message", onHostMessage as EventListener);
	window.addEventListener("message", onHostMessage as EventListener);
	postToHost({ type: "ready" });
}
