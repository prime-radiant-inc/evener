import type { LaunchConfigLayer } from "../../appwire-client/typescript/types.gen";
import {
  type DraftImage,
  type DraftImageData,
  parseImages,
} from "./draftImages";
import type { DraftDatabase } from "./draftRepository";

export interface CreationDraft {
  cwd: string;
  prompt: string;
  harness: string;
  model: { provider: string; model: string } | null;
  reasoning: string;
  launchOverrides: LaunchConfigLayer;
  images: DraftImageData[];
  unconfirmed: boolean;
}
type SavedDraft = Omit<CreationDraft, "images"> & { images: DraftImage[] };
function decode(raw: string): SavedDraft {
  const value = JSON.parse(raw) as SavedDraft;
  if (
    !value ||
    typeof value !== "object" ||
    typeof value.cwd !== "string" ||
    typeof value.prompt !== "string" ||
    typeof value.harness !== "string" ||
    typeof value.reasoning !== "string" ||
    typeof value.unconfirmed !== "boolean" ||
    !value.launchOverrides ||
    typeof value.launchOverrides !== "object" ||
    Array.isArray(value.launchOverrides) ||
    (value.model !== null &&
      (!value.model ||
        typeof value.model.provider !== "string" ||
        typeof value.model.model !== "string"))
  ) {
    throw new Error("Invalid saved creation draft.");
  }
  return { ...value, images: parseImages(JSON.stringify(value.images)) };
}
export function creationDraftMetadata(draft: CreationDraft): string {
  return JSON.stringify({
    ...draft,
    model: draft.model
      ? { provider: draft.model.provider, model: draft.model.model }
      : null,
    images: draft.images.map(({ data: _data, ...image }) => image),
  });
}

/** One atomic form checkpoint per hub; image bytes are not rewritten on typing. */
export class CreationDraftRepository {
  private savedImages = new Map<string, DraftImageData[]>();
  constructor(private db: DraftDatabase) {
    db.execSync(
      "CREATE TABLE IF NOT EXISTS creation_drafts (hub_id TEXT PRIMARY KEY, draft TEXT NOT NULL)",
    );
    db.execSync(
      "CREATE TABLE IF NOT EXISTS creation_draft_images (hub_id TEXT NOT NULL, id TEXT NOT NULL, media_type TEXT NOT NULL, data TEXT NOT NULL, PRIMARY KEY (hub_id, id))",
    );
  }
  read(hubId: string): CreationDraft | null {
    const row = this.db.getFirstSync<{ draft: string }>(
      "SELECT draft FROM creation_drafts WHERE hub_id = ?",
      hubId,
    );
    if (!row) return null;
    const saved = decode(row.draft);
    const images = saved.images.map((image) => {
      const bytes = this.db.getFirstSync<{ data: string; media_type: string }>(
        "SELECT data, media_type FROM creation_draft_images WHERE hub_id = ? AND id = ?",
        hubId,
        image.id,
      );
      if (!bytes?.data || bytes.media_type !== image.mediaType)
        throw new Error("Saved creation image is unavailable.");
      return { ...image, data: bytes.data };
    });
    this.savedImages.set(hubId, images);
    return { ...saved, images };
  }
  write(hubId: string, draft: CreationDraft): void {
    const raw = creationDraftMetadata(draft);
    const saved = decode(raw);
    this.db.execSync("SAVEPOINT creation_draft_write");
    try {
      for (const image of draft.images) {
        const known = this.savedImages
          .get(hubId)
          ?.find((existing) => existing.id === image.id);
        if (known?.data === image.data && known.mediaType === image.mediaType)
          continue;
        const existing = this.db.getFirstSync<{
          data: string;
          media_type: string;
        }>(
          "SELECT data, media_type FROM creation_draft_images WHERE hub_id = ? AND id = ?",
          hubId,
          image.id,
        );
        if (
          !image.data ||
          (existing &&
            (existing.data !== image.data ||
              existing.media_type !== image.mediaType))
        )
          throw new Error("Saved image identity cannot be replaced.");
        if (!existing)
          this.db.runSync(
            "INSERT INTO creation_draft_images (hub_id, id, media_type, data) VALUES (?, ?, ?, ?)",
            hubId,
            image.id,
            image.mediaType,
            image.data,
          );
      }
      this.db.runSync(
        "INSERT INTO creation_drafts (hub_id, draft) VALUES (?, ?) ON CONFLICT (hub_id) DO UPDATE SET draft = excluded.draft",
        hubId,
        raw,
      );
      const ids = saved.images.map((image) => image.id);
      this.db.runSync(
        `DELETE FROM creation_draft_images WHERE hub_id = ?${ids.length ? ` AND id NOT IN (${ids.map(() => "?").join(",")})` : ""}`,
        hubId,
        ...ids,
      );
      this.db.execSync("RELEASE creation_draft_write");
      this.savedImages.set(hubId, draft.images);
    } catch (error) {
      this.db.execSync(
        "ROLLBACK TO creation_draft_write; RELEASE creation_draft_write",
      );
      throw error;
    }
  }
  clear(hubId: string): void {
    this.db.execSync("SAVEPOINT creation_draft_clear");
    try {
      this.db.runSync("DELETE FROM creation_drafts WHERE hub_id = ?", hubId);
      this.db.runSync(
        "DELETE FROM creation_draft_images WHERE hub_id = ?",
        hubId,
      );
      this.db.execSync("RELEASE creation_draft_clear");
      this.savedImages.delete(hubId);
    } catch (error) {
      this.db.execSync(
        "ROLLBACK TO creation_draft_clear; RELEASE creation_draft_clear",
      );
      throw error;
    }
  }
}
