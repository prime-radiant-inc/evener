import { MAX_ATTACHMENT_BYTES } from "@evener/appwire-client";
import { randomUUID } from "expo-crypto";
import { File } from "expo-file-system";
import { ImageManipulator, SaveFormat } from "expo-image-manipulator";
import * as Picker from "expo-image-picker";
import { encodeToFit, type FitAttempt } from "./imageFit";
import { base64ByteLength, CameraAccessDenied, type ImagePicker, type PickedImage } from "./imageSelection";

function pickedImages(result: Picker.ImagePickerResult): PickedImage[] {
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
}

export const nativeImagePicker: ImagePicker = {
	id: randomUUID,
	async pick(limit) {
		const result = await Picker.launchImageLibraryAsync({
			mediaTypes: ["images"],
			allowsMultipleSelection: true,
			selectionLimit: limit,
			allowsEditing: false,
			quality: 1,
			preferredAssetRepresentationMode: Picker.UIImagePickerPreferredAssetRepresentationMode.Current,
		});
		return pickedImages(result);
	},
	async capture() {
		const permission = await Picker.requestCameraPermissionsAsync();
		if (!permission.granted) throw new CameraAccessDenied();
		return pickedImages(
			await Picker.launchCameraAsync({
				mediaTypes: ["images"],
				quality: 1,
				allowsEditing: false,
			}),
		);
	},
	async encode(image) {
		// The photo's own size, read once; each attempt renders afresh.
		const { width, height } = await render(image.uri, null, (rendered) => ({
			width: rendered.width,
			height: rendered.height,
		}));
		return encodeToFit(width, height, MAX_ATTACHMENT_BYTES, base64ByteLength, (attempt) =>
			render(image.uri, attempt.resize, (rendered) => saveJpeg(rendered, attempt)),
		);
	},
};

type Rendered = Awaited<ReturnType<ReturnType<typeof ImageManipulator.manipulate>["renderAsync"]>>;

/** Renders the image, scaled when asked, and hands it to `use` before letting
 * the native objects go; `use` returns plain values, never the rendered image. */
async function render<T>(
	uri: string,
	resize: FitAttempt["resize"],
	use: (rendered: Rendered) => T | Promise<T>,
): Promise<T> {
	const context = ImageManipulator.manipulate(uri);
	try {
		if (resize) context.resize(resize);
		const rendered = await context.renderAsync();
		try {
			return await use(rendered);
		} finally {
			rendered.release();
		}
	} finally {
		context.release();
	}
}

async function saveJpeg(rendered: Rendered, attempt: FitAttempt): Promise<string> {
	const result = await rendered.saveAsync({ format: SaveFormat.JPEG, compress: attempt.compress, base64: true });
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
}
