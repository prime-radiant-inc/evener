// The transcript's speaker tile, rendered at exchange-boundary headers: a
// small square tile carrying a glyph that names WHO is speaking (the user or
// the agent), so a long transcript's speaker turns are scannable as structure
// rather than recoverable only by reading the message text.
//
// Why a tile and not a photo avatar: the app has no images and no identity
// beyond "you" and "the agent" - there is no portrait to show. A tile with a
// line-art glyph is a boundary marker, not a portrait: it answers "whose turn
// is this" the way a chat app's avatar column does, without pretending there
// is a person behind the glyph.
//
// Why --surface-2 for the fill: in the light theme --surface-2 equals
// --surface-1, so the fill alone does not separate the tile from the page -
// the 1px --edge border carries it, the same situation as the app's existing
// hairline boxes (the MANDATE box). The border is therefore load-bearing in
// one theme and decorative in the other, and it is present in both so the
// tile is one drawing.
//
// Both glyphs come from widgets/toolicon's grammar (16-grid, stroke
// currentColor, 1.75 width), so the avatars read as the same family as the
// tool-row icons they sit among: the user wears "person", the agent the
// "skill" sparkle. The glyph is currentColor inside a tile that sets its own
// ink (--ink-mid), so no colour literal appears here.

import { requireClass } from "../internal/requireClass";
import { ToolIcon } from "../toolicon";
import styles from "./speakeravatar.module.css";

const CLASS = {
  tile: requireClass(styles.tile, "speakeravatar.module.css", "tile"),
};

export type SpeakerAvatarSpeaker = "user" | "agent";

export interface SpeakerAvatarProps {
  speaker: SpeakerAvatarSpeaker;
  /** Tile edge in px. Square by construction - see this widget's own test. */
  size?: number;
}

// 24px: the exchange-boundary header's tile edge (see docs/web-ui/specs/
// 2026-07-29-transcript-slack-lean-messages.md, decision 2). This is the
// widget's API default and must work outside the transcript too; the
// transcript's matching --speaker-avatar-size (declared on .turn in
// turnblock.module.css) is pinned equal by this widget's own test - that
// drift pin is what keeps the widget default and the transcript geometry
// from drifting apart.
export const DEFAULT_SIZE = 24;

// The glyph scales with the tile so a resized avatar stays proportioned:
// 24px -> 14px, the app's standard icon box (widgets/chevron's DEFAULT_SIZE).
// At any other size the glyph keeps the same 14/24 ratio, rounded to a whole
// px so the svg box stays square at any tile size.
function glyphSizeFor(tileSize: number): number {
  return Math.round(tileSize * 0.583);
}

export function SpeakerAvatar({ speaker, size = DEFAULT_SIZE }: SpeakerAvatarProps) {
  return (
    // Decorative: the header row beside it already names the speaker in words
    // ("You", "Agent"), so exposing the tile would announce the same fact
    // twice.
    <span className={CLASS.tile} style={{ width: size, height: size }} aria-hidden="true" data-testid="speaker-avatar">
      <ToolIcon kind={speaker === "user" ? "person" : "skill"} size={glyphSizeFor(size)} />
    </span>
  );
}
