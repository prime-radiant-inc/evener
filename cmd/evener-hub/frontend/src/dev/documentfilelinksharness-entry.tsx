import { parseFileReference } from "@evener/appwire-client/docContent";
import { createRoot } from "react-dom/client";
import { enhanceFileReferences } from "../panes/session/transcript/messages/fileReferenceDOM";
import { documentPaneState, workspaceStore } from "../shell/workspace";
import { selectLocation } from "../stores/navigation/selectors";
import { navigationStore } from "../stores/navigation/store";
import { Markdown } from "../widgets/markdown";
import { DocumentFileLinksFixture, startFileLinksFixture } from "./DocumentFileLinksFixture";
import "../styles/tokens.css";
import "../styles/global.css";

const root = document.getElementById("root");
if (!root) throw new Error("document harness missing root");
const fixtureRef = new URLSearchParams(location.search).get("ref") ?? "";
// The dev page is fetched through Vite's filesystem route, but the shell must
// receive a genuine product session URL rather than that development filename.
history.replaceState(null, "", `/s/${encodeURIComponent(fixtureRef)}`);
createRoot(root).render(<DocumentFileLinksFixture />);
async function qualifyMarkdown(markdown: string, cwd: string) {
  const source = workspaceStore.getState().panes.find((pane) => pane.type === "session");
  if (!source) throw new Error("real source pane missing");
  const container = document.createElement("div");
  container.hidden = true;
  document.body.append(container);
  const probe = createRoot(container);
  let cleanup = () => {};
  try {
    const element = await new Promise<HTMLDivElement>((resolve) => {
      probe.render(
        <Markdown
          source={markdown}
          ref={(node) => {
            if (node) resolve(node);
          }}
        />,
      );
    });
    const sanitized = element.innerHTML;
    const existingAnchors = [...element.querySelectorAll("a[href]")].map((link) => link.getAttribute("href"));
    cleanup = enhanceFileReferences(element, {
      sessionRef: (source.params as { ref: string }).ref,
      cwd,
      sourcePaneId: source.id,
    });
    const anchors = [...element.querySelectorAll("a[href]")].map((link) => link.getAttribute("href") ?? "");
    const paths = anchors.flatMap((href) => {
      const url = new URL(href, location.origin);
      if (url.pathname !== "/doc/file" && url.pathname !== "/doc/image") return [];
      const reference = parseFileReference(url.searchParams.get("path") ?? "", "code", cwd);
      return reference ? [reference.path] : [];
    });
    return { markdown, sanitized, existingAnchors, enhanced: element.innerHTML, anchors, paths };
  } finally {
    cleanup();
    probe.unmount();
    container.remove();
  }
}
const target = window as typeof window & {
  fileLinksFixture: {
    ready: Promise<void>;
    requests(): Promise<number>;
    state(): unknown;
    route(): unknown;
    qualifyMarkdown: typeof qualifyMarkdown;
  };
};
target.fileLinksFixture = {
  ready: startFileLinksFixture(fixtureRef),
  requests: async () => (await fetch("/doc/fixture/requests")).json(),
  state: () => ({
    ...workspaceStore.getState(),
    panes: workspaceStore.getState().panes.map((pane) => ({ ...pane, document: documentPaneState(pane) })),
  }),
  route: () => ({ pathname: location.pathname, location: selectLocation(fixtureRef)(navigationStore.getState()) }),
  qualifyMarkdown,
};
