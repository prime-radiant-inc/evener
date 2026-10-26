import { ImageGallery } from "../panes/session/transcript/flow/ImageGallery";
import styles from "./comic.module.css";
import type { ComicComposition, ComicSlice } from "./layout";
import type { ComicEntry } from "./projection";

export type ComicScene = "room" | "field" | "white";
export type ComicExpression = "neutral" | "happy-wave" | "angry" | "scared" | "point" | "celebrate";

const ART = "/webassets/comic/sprites/";

function stableXenoPose(id: string): "neutral" | "point" {
  let hash = 0x811c9dc5;
  for (let i = 0; i < id.length; i += 1) hash = Math.imul(hash ^ id.charCodeAt(i), 0x01000193) >>> 0;
  return hash % 2 ? "point" : "neutral";
}

/** Bespoke transcript geometry: source text, not stage imagery, is the accessible reading surface. */
export function ComicPanel({
  entry,
  slice,
  composition,
  scene,
  expression,
}: {
  entry: ComicEntry;
  slice: ComicSlice;
  composition: ComicComposition;
  scene: ComicScene;
  expression: ComicExpression;
}): React.ReactElement {
  const character = entry.persona?.character ?? (entry.speaker === "human" ? "anna" : "xeno");
  const pose = character === "anna" ? expression : character === "xeno" ? stableXenoPose(entry.id) : "neutral";
  const continuation = slice.start > 0;
  const spoken = entry.kind === "dialogue" || entry.kind === "thought";
  const source = entry.kind === "caption" || entry.kind === "images";
  const text = entry.text.slice(slice.start, slice.end);
  const author = entry.persona ? `${entry.persona.name} · ${entry.persona.badge}` : entry.sourceLabel;
  const identity = `${author}${entry.inherited ? " · inherited" : ""}`;
  const label = entry.kind === "thought" ? `${identity} thought` : identity;
  return (
    <article
      className={[
        styles.panel,
        composition === "spotlight" ? styles.spotlight : "",
        continuation ? styles.continuation : "",
      ]
        .filter(Boolean)
        .join(" ")}
      aria-label={continuation ? `${label}, continued` : label}
      data-comic-speaker={entry.speaker}
      data-comic-turn-id={"sourceTurnId" in entry ? entry.sourceTurnId : undefined}
    >
      <div className={styles.label} data-comic-identity="">
        {identity}
        {continuation ? " · continued" : ""}
      </div>
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
      {entry.kind !== "images" && (
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
          <p className={styles.balloonText} data-comic-text="">
            {text}
          </p>
          {entry.kind === "thought" && (
            <span className={styles.thoughtTrail} data-comic-thought-trail="" aria-hidden="true">
              <span />
              <span />
            </span>
          )}
        </div>
      )}
      {entry.images && entry.images.length > 0 && slice.end === entry.text.length && (
        <div className={styles.images}>
          <ImageGallery images={entry.images} />
        </div>
      )}
      <div className={styles.stage} data-comic-scene={scene} aria-hidden="true">
        <img className={styles.figure} src={`${ART}${character}-${pose}.png`} alt="" />
      </div>
    </article>
  );
}
