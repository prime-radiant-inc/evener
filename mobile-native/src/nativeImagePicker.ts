import { randomUUID } from "expo-crypto";
import { File } from "expo-file-system";
import { ImageManipulator, SaveFormat } from "expo-image-manipulator";
import * as Picker from "expo-image-picker";
import type { ImagePicker } from "./imageSelection";

export const nativeImagePicker: ImagePicker = {
  id: randomUUID,
  async pick(limit) {
    const result = await Picker.launchImageLibraryAsync({
      mediaTypes: ["images"],
      allowsMultipleSelection: true,
      selectionLimit: limit,
      allowsEditing: false,
      quality: 1,
      preferredAssetRepresentationMode:
        Picker.UIImagePickerPreferredAssetRepresentationMode.Current,
    });
    if (result.canceled) return [];
    return result.assets.map((asset) => {
      const file = new File(asset.uri);
      return {
        uri: asset.uri,
        name: asset.fileName ?? file.name,
        type: asset.mimeType || file.type || "image/unknown",
        size: asset.fileSize ?? file.size,
      };
    });
  },
  async encode(image) {
    const context = ImageManipulator.manipulate(image.uri);
    try {
      const rendered = await context.renderAsync();
      try {
        const result = await rendered.saveAsync({
          format: SaveFormat.PNG,
          base64: true,
        });
        try {
          if (!result.base64) throw new Error("Image data is unavailable.");
          return result.base64;
        } finally {
          // The encoded bytes are persisted in SQLite; the generated cache file is disposable.
          try {
            new File(result.uri).delete();
          } catch {
            /* OS cache eviction can finish cleanup. */
          }
        }
      } finally {
        rendered.release();
      }
    } finally {
      context.release();
    }
  },
};
