// The composer anchors each staged image to a positional "[image N]" marker
// in the textarea (textareaMarkers.ts) - that marker is an EDITING affordance:
// it is what a tile removes, what a decode failure strips, and what a draft
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
// the composer's own text keeps its tiles' anchors.
// Every attachment carries the marker number it was staged under, so this
// pairing is an identity lookup, never an inference from array position or
// from where the marker sits in the text: a marker number is a stable id
// handed out by an increment-only counter (useAttachments' own "removeItem...
// by its stable marker, not array index, which shifts" contract), so the
// attachment array routinely closes up around gaps the numbering keeps. That
// identity also survives edits no positional rule can follow - a marker the
// user hand-deleted while leaving its attachment staged, one they moved ahead
// of its siblings, one they copied.
export interface MarkerAttachment {
  marker: number;
  name?: string;
}

const MARKER = /\[image (\d+)\]/g;

// translateAttachmentMarkers is the ONE transformation applied to the
// composer's text on its way to the wire (floor §1.12: otherwise sent
// untrimmed, byte for byte). Text with no markers comes back identical.
//
// A marker with no attachment to rank onto is left VERBATIM rather than
// translated: prose naming an image that isn't in this submission would
// fabricate an attachment reference, which is worse than the literal the
// model can at least see is a leftover.
export function translateAttachmentMarkers(text: string, attachments?: readonly MarkerAttachment[]): string {
  const staged = new Map((attachments ?? []).map((attachment) => [attachment.marker, attachment]));
  return text.replace(MARKER, (literal, digits: string) => {
    const marker = Number(digits);
    const attachment = staged.get(marker);
    if (!attachment) return literal;
    return attachment.name ? `(attached image ${marker}: ${attachment.name})` : `(attached image ${marker})`;
  });
}
