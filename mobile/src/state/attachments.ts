// AttachmentState (Zustand) — the per-session store of pending image
// attachments. Holds PendingAttachment entries (opaque native handles +
// media type + optional name) and an error slot. The store is the bridge
// between the attachment service (which validates and normalizes) and the
// composer (which previews and sends). The store never auto-retries; errors
// are surfaced and cleared explicitly.

import { create } from "zustand";
import type { PendingAttachment } from "../services/attachments";

export interface AttachmentState {
  readonly attachments: PendingAttachment[];
  readonly error: string | null;
  add(handle: string, mediaType: string, name?: string): void;
  remove(id: string): void;
  clear(): void;
  setError(error: string | null): void;
}

let attachmentStoreIdCounter = 0;
function nextId(): string {
  attachmentStoreIdCounter += 1;
  return `att-${Date.now().toString(36)}-${attachmentStoreIdCounter}`;
}

export function createAttachmentStore() {
  return create<AttachmentState>((set) => ({
    attachments: [],
    error: null,
    add(handle, mediaType, name) {
      set((s) => ({
        attachments: [
          ...s.attachments,
          { id: nextId(), handle, mediaType, name },
        ],
      }));
    },
    remove(id) {
      set((s) => ({
        attachments: s.attachments.filter((a) => a.id !== id),
      }));
    },
    clear() {
      set({ attachments: [] });
    },
    setError(error) {
      set({ error });
    },
  }));
}
