import type { ReactElement } from "react";
import { ImageGallery } from "../panes/session/transcript/flow/ImageGallery";
import type { ComicEntry } from "./projection";

/** Accessible alternate presentation: the same source entries, without comic art or Markdown parsing. */
export function ComicPlainTranscript({ entries }: { entries: readonly ComicEntry[] }): ReactElement {
  const itemEntries = entries.filter((entry) => entry.sourceTurnId === undefined);
  const turnErrors = entries.filter((entry) => entry.sourceTurnId !== undefined);
  const renderEntry = (entry: ComicEntry) => (
    <li key={entry.id} style={{ whiteSpace: "pre-wrap", overflowWrap: "anywhere" }}>
      <div>
        <strong>{entry.sourceLabel}</strong>
        {entry.sourceTurnId && <span> · {entry.sourceTurnId}</span>}
        {entry.persona && (
          <span>
            {" "}
            — {entry.persona.name} · {entry.persona.badge}
            {entry.persona.ownerRef && ` · ${entry.persona.ownerRef}`}
          </span>
        )}
        {entry.kind === "thought" && <span> · Thought</span>}
      </div>
      {entry.text && <div data-comic-text="">{entry.text}</div>}
      {entry.images && <ImageGallery images={entry.images} />}
    </li>
  );
  return (
    <>
      <ol aria-label="Conversation" style={{ listStyle: "none", padding: 0 }}>
        {itemEntries.map(renderEntry)}
      </ol>
      {turnErrors.length > 0 && (
        <section aria-label="Turn errors">
          <ol>{turnErrors.map(renderEntry)}</ol>
        </section>
      )}
    </>
  );
}
