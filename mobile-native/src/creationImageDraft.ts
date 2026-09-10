import type { DraftDocument } from "./draftDocument";
import { type DraftImage, imageInput } from "./draftImages";
import type { createNewSessionStore } from "./newSession";

/** Adapts a creation form to the same picker and previews as conversation drafts. */
export function creationImageDraft(
  store: ReturnType<typeof createNewSessionStore>,
) {
  let state = store.getState();
  function snapshot(): ReturnType<DraftDocument["getSnapshot"]> {
    return {
      record: { draft: state.prompt, images: state.images, unconfirmed: null },
      loaded: state.storageLoaded,
      submitting: state.submitting,
      error: state.storageError,
    };
  }
  let current = snapshot();
  return {
    subscribe: (listener: () => void) => store.subscribe(listener),
    getSnapshot: () => {
      if (state !== store.getState()) {
        state = store.getState();
        current = snapshot();
      }
      return current;
    },
    addImage: store.getState().addImage,
    removeImage: store.getState().removeImage,
    imagePreviews: (references: DraftImage[] = store.getState().images) =>
      references.map((reference) => {
        const image = store
          .getState()
          .images.find((item) => item.id === reference.id);
        if (!image) throw new Error("Image is unavailable.");
        return imageInput(image, image.data);
      }),
  };
}
