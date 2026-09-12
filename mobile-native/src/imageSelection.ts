import {
  MAX_ATTACHMENTS,
  rejectionReason,
} from "../../cmd/evener-hub/frontend/src/panes/session/composer/attachments/limits";
import type { DraftDocument } from "./draftDocument";

export interface PickedImage {
  uri: string;
  name: string;
  type: string;
  size: number;
}
interface PendingImage extends PickedImage {
  id: string;
  marker: number;
}
interface SelectionSnapshot {
  busy: boolean;
  pending: PendingImage[];
  error: string | null;
}
export interface ImagePicker {
  pick(limit: number): Promise<PickedImage[]>;
  encode(image: PickedImage): Promise<string>;
  id(): string;
}

function base64ByteLength(data: string): number {
  const padding = data.endsWith("==") ? 2 : data.endsWith("=") ? 1 : 0;
  return (data.length * 3) / 4 - padding;
}

/** Owns selection and encoding for one draft, including late native results. */
export class ImageSelection {
  private snapshot: SelectionSnapshot = {
    busy: false,
    pending: [],
    error: null,
  };
  private listeners = new Set<() => void>();
  private generation = 0;
  private nextMarker = 0;
  constructor(
    private document: Pick<DraftDocument, "getSnapshot" | "addImage">,
    private picker: ImagePicker,
  ) {}
  getSnapshot = () => this.snapshot;
  subscribe = (listener: () => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };
  private update(update: Partial<SelectionSnapshot>) {
    this.snapshot = { ...this.snapshot, ...update };
    for (const listener of this.listeners) listener();
  }
  cancel() {
    this.generation++;
    this.update({ busy: false, pending: [], error: null });
  }
  remove(id: string) {
    this.update({
      pending: this.snapshot.pending.filter((image) => image.id !== id),
    });
  }
  async choose() {
    const draft = this.document.getSnapshot();
    if (this.snapshot.busy || !draft.loaded || draft.error) return;
    const count = draft.record.images?.length ?? 0;
    if (count >= MAX_ATTACHMENTS) {
      this.update({ error: `You can attach up to ${MAX_ATTACHMENTS} images.` });
      return;
    }
    const generation = ++this.generation;
    this.update({ busy: true, error: null });
    const errors: string[] = [];
    try {
      const picked = await this.picker.pick(MAX_ATTACHMENTS - count);
      if (generation !== this.generation) return;
      const current = this.document.getSnapshot().record;
      this.nextMarker = Math.max(
        this.nextMarker,
        ...[
          ...(current.images ?? []),
          ...(current.unconfirmedImages ?? []),
        ].map((image) => image.marker),
      );
      let reserved = current.images?.length ?? 0;
      const pending: PendingImage[] = [];
      for (const image of picked) {
        const reason =
          !Number.isFinite(image.size) || image.size < 0
            ? `${image.name} (could not read file size)`
            : rejectionReason(image, reserved);
        if (reason) {
          errors.push(reason);
          continue;
        }
        reserved++;
        pending.push({
          ...image,
          id: this.picker.id(),
          marker: ++this.nextMarker,
        });
      }
      this.update({ pending });
      // Decode one full-resolution image at a time to bound native memory use.
      for (const image of pending) {
        if (generation !== this.generation) return;
        if (!this.snapshot.pending.some((item) => item.id === image.id))
          continue;
        try {
          const data = await this.picker.encode(image);
          if (generation !== this.generation) return;
          if (!this.snapshot.pending.some((item) => item.id === image.id))
            continue;
          const reason = rejectionReason(
            {
              type: "image/png",
              size: base64ByteLength(data),
              name: image.name,
            },
            0,
          );
          if (reason) errors.push(reason);
          else
            this.document.addImage({
              id: image.id,
              marker: image.marker,
              mediaType: "image/png",
              name: image.name,
              data,
            });
        } catch {
          if (
            generation === this.generation &&
            this.snapshot.pending.some((item) => item.id === image.id)
          )
            errors.push(`${image.name} (could not process image)`);
        }
        if (generation === this.generation) this.remove(image.id);
      }
    } catch {
      errors.push("Could not open or read the image selection.");
    } finally {
      if (generation === this.generation)
        this.update({
          busy: false,
          pending: [],
          error: errors.length ? errors.join("\n") : null,
        });
    }
  }
}
