import { vi } from "vitest";
import * as encoding from "../composer/attachments/encodePng";

/** Hold the browser decode boundary while the real attachment pipeline runs. */
export function installControlledImageEncoding() {
  const images: ControlledImage[] = [];
  const completions: Promise<encoding.EncodedPng>[] = [];
  const reencode = encoding.reencodeToPng;
  vi.spyOn(encoding, "reencodeToPng").mockImplementation((blob) => {
    const completion = reencode(blob);
    completions.push(completion);
    return completion;
  });
  class ControlledImage {
    onload: (() => void) | null = null;
    onerror: (() => void) | null = null;
    width = 8;
    height = 4;
    src = "";

    constructor() {
      images.push(this);
    }
  }
  vi.stubGlobal("Image", ControlledImage);
  vi.spyOn(URL, "createObjectURL").mockReturnValue("blob:controlled-image");
  vi.spyOn(URL, "revokeObjectURL").mockImplementation(() => {});
  vi.spyOn(HTMLCanvasElement.prototype, "getContext").mockImplementation((() => ({
    drawImage() {},
  })) as unknown as typeof HTMLCanvasElement.prototype.getContext);
  vi.spyOn(HTMLCanvasElement.prototype, "toBlob").mockImplementation((callback) => {
    callback(new Blob([new Uint8Array([1, 2, 3])], { type: "image/png" }));
  });
  return {
    async resolve(index = 0): Promise<void> {
      const image = images[index];
      if (!image) throw new Error(`No controlled image at ${index}`);
      image.onload?.();
      await completions[index];
    },
    async reject(index = 0): Promise<void> {
      const image = images[index];
      if (!image) throw new Error(`No controlled image at ${index}`);
      image.onerror?.();
      await completions[index]?.catch((error: unknown) => {
        if (!(error instanceof Error) || error.message !== "image decode failed") throw error;
      });
    },
  };
}
