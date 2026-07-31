// The composer anchors each staged image to a positional "[image N]" marker
// in the textarea (textareaMarkers.ts) - that marker is an EDITING affordance:
// it is what a chip removes, what a decode failure strips, and what a draft
// round-trips. It is not something to say to a model. A raw marker on the wire
// misleads small models: measured 2026-07-31, haiku received
// "[image 1]Describe the attached image" alongside a real vision block, read
// the marker as a file path, called read_file("[image 1]"), failed, and
// answered "I cannot access the image."
//
// The marker also carries no placement information the model could use even in
// principle - buildInput emits one text item followed by the images as
// separate parts, so the wire has already lost the marker's position in the
// text. So each marker is TRANSLATED to prose at send (Jesse, 2026-07-31),
// not stripped and not left raw: the model gets a sentence it can read, and
// the composer's own text keeps its chips' anchors.
export interface MarkerAttachment {
  name?: string;
}

const MARKER = /\[image (\d+)\]/g;

// Marker numbers are stable ids handed out by an increment-only counter, NOT
// array positions: removing a staged image leaves a permanent gap in the
// numbering while the attachment array closes up (useAttachments' own
// "removeItem... by its stable marker, not array index, which shifts"
// contract), so "[image 3]" routinely arrives here alongside just two
// attachments. What survives that is ORDER: markers are handed out in staging
// order and items are appended, so the k-th smallest marker still present in
// the text names the k-th attachment. Ranking by number rather than by
// position in the text also keeps a marker's meaning independent of where the
// user moved it (an image attached with the cursor moved back to the start
// puts marker 2 ahead of marker 1) and of how many times they copied it.
function markerRanks(text: string): Map<number, number> {
  const distinct = new Set<number>();
  for (const match of text.matchAll(MARKER)) distinct.add(Number(match[1]));
  const ascending = [...distinct].sort((left, right) => left - right);
  return new Map(ascending.map((marker, rank) => [marker, rank]));
}

// translateAttachmentMarkers is the ONE transformation applied to the
// composer's text on its way to the wire (floor §1.12: otherwise sent
// untrimmed, byte for byte). Text with no markers comes back identical.
//
// A marker with no attachment to rank onto is left VERBATIM rather than
// translated: prose naming an image that isn't in this submission would
// fabricate an attachment reference, which is worse than the literal the
// model can at least see is a leftover.
export function translateAttachmentMarkers(text: string, attachments?: readonly MarkerAttachment[]): string {
  const staged = attachments ?? [];
  const ranks = markerRanks(text);
  return text.replace(MARKER, (literal, digits: string) => {
    const marker = Number(digits);
    const rank = ranks.get(marker);
    const attachment = rank === undefined ? undefined : staged[rank];
    if (!attachment) return literal;
    return attachment.name ? `(attached image ${marker}: ${attachment.name})` : `(attached image ${marker})`;
  });
}
