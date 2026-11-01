import { ImageGallery } from "../panes/session/transcript/flow/ImageGallery";
import { ComicMarkdown } from "./ComicMarkdown";
import styles from "./comic.module.css";
import type { ComicBalloon } from "./layout";
import type { ComicEntry } from "./projection";

export type ComicScene = "room" | "field" | "white";
export type ComicExpression = "neutral" | "happy-wave" | "angry" | "scared" | "point" | "celebrate";

/** A balloon plus the entry its source range points into. */
export type ComicPanelBalloon = ComicBalloon & { entry: ComicEntry };

/** The per-balloon slice attributes the reader, reading anchors and browser guards all query. */
export function comicSourceSliceProps(
  slice: { entryId: string; start: number; end: number },
  sessionRef: string,
  path: string,
) {
  return {
    "data-comic-entry": slice.entryId,
    "data-comic-session": sessionRef,
    "data-comic-path": path,
    "data-comic-start": slice.start,
    "data-comic-end": slice.end,
  };
}

const ART = "/webassets/comic/sprites/";

function stableXenoPose(id: string): "neutral" | "point" {
  let hash = 0x811c9dc5;
  for (let i = 0; i < id.length; i += 1) hash = Math.imul(hash ^ id.charCodeAt(i), 0x01000193) >>> 0;
  return hash % 2 ? "point" : "neutral";
}

/** Bespoke transcript geometry: source text, not stage imagery, is the accessible reading surface. */
export function ComicPanel({
  balloons,
  scene,
  expression,
  sessionRef,
  path,
}: {
  balloons: readonly ComicPanelBalloon[];
  scene: ComicScene;
  expression: ComicExpression;
  sessionRef: string;
  path: string;
}): React.ReactElement {
  const first = balloons[0];
  if (!first) return <article className={styles.panel} />;
  const lead = first.entry;
  const composition = first.composition;
  const character = lead.persona?.character;
  const pose = character === "anna" ? expression : character === "xeno" ? stableXenoPose(lead.id) : "neutral";
  const continuation = first.slice.start > 0;
  const insert = composition === "insert";
  const author = lead.persona
    ? `${lead.persona.name} · ${lead.persona.badge}${lead.persona.ownerRef ? ` · ${lead.persona.ownerRef}` : ""}`
    : lead.sourceLabel;
  const identity = `${author}${lead.inherited ? " · inherited" : ""}`;
  const label = lead.kind === "thought" ? `${identity} thought` : identity;
  return (
    <article
      className={[
        styles.panel,
        insert ? styles.insert : "",
        composition === "spotlight" ? styles.spotlight : "",
        continuation ? styles.continuation : "",
      ]
        .filter(Boolean)
        .join(" ")}
      aria-label={continuation ? `${label}, continued` : label}
      data-comic-speaker={lead.speaker}
      data-comic-turn-id={"sourceTurnId" in lead && lead.sourceTurnId ? lead.sourceTurnId : undefined}
    >
      <div className={styles.label} data-comic-identity="">
        {identity}
        {continuation ? " · continued" : ""}
      </div>
      {balloons.map(({ entry, slice, document, part, composition: balloonComposition }) => {
        const source = entry.kind === "caption" || entry.kind === "images";
        const spoken = entry.kind === "dialogue" || entry.kind === "thought";
        const balloonInsert = balloonComposition === "insert";
        return (
          <div
            className={styles.sourcePanel}
            key={`${slice.entryId}:${slice.start}`}
            {...comicSourceSliceProps(slice, sessionRef, path)}
          >
            {source && (
              <div className={styles.source} data-comic-source="">
                {entry.sourceLabel}
                {entry.delegateResult && (
                  <>
                    {" "}
                    · {entry.delegateResult.kind}
                    {entry.delegateResult.status && <> · {entry.delegateResult.status}</>}
                  </>
                )}
                {"sourceTurnId" in entry && entry.sourceTurnId && <> · {entry.sourceTurnId}</>}
              </div>
            )}
            {balloonInsert ? (
              <ComicMarkdown document={document} part={part} />
            ) : (
              entry.kind !== "images" && (
                <div
                  className={[
                    styles.balloon,
                    entry.kind === "thought" ? styles.thought : "",
                    source ? styles.captionBalloon : "",
                  ]
                    .filter(Boolean)
                    .join(" ")}
                  data-comic-balloon={entry.kind === "thought" ? "thought" : spoken ? "speech" : "caption"}
                >
                  <ComicMarkdown document={document} part={part} />
                  {entry.kind === "thought" && (
                    <span className={styles.thoughtTrail} data-comic-thought-trail="" aria-hidden="true">
                      <span />
                      <span />
                    </span>
                  )}
                </div>
              )
            )}
            {entry.images && entry.images.length > 0 && slice.end === entry.text.length && (
              <div className={styles.images}>
                <ImageGallery images={entry.images} />
              </div>
            )}
          </div>
        );
      })}
      {!insert && (
        <div className={styles.stage} data-comic-scene={scene} aria-hidden="true">
          {character && <img className={styles.figure} src={`${ART}${character}-${pose}.png`} alt="" />}
        </div>
      )}
    </article>
  );
}
