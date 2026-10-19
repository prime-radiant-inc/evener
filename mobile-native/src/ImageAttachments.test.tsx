// The composer's attached-image strip: each thumbnail's remove control keeps
// the platform's 44pt touch target (28pt visible + 8pt slop each side) and
// draws its mark as an SF Symbol, never a text glyph.
import { createElement } from "react";
import { act } from "react-test-renderer";
import { expect, it, vi } from "vitest";
import type { DraftDocument } from "./draftDocument";
import type { ImageSelection } from "./imageSelection";
import { ImageAttachments } from "./ImageAttachments";
import { render } from "./renderNative.testkit";

vi.mock("react-native", async () => (await import("./renderNative.testkit")).nativeModuleMock());
vi.mock("expo-symbols", () => ({ SymbolView: "SymbolView" }));

const image = { id: "image-1", marker: 1, mediaType: "image/png", data: "AQID", name: "photo.png" };

function documentWithImage(): DraftDocument {
	const empty = { record: { images: [], unconfirmedImages: [] } };
	const holding = { record: { images: [image], unconfirmedImages: [] } };
	let snapshot = holding;
	return {
		subscribe: () => () => {},
		getSnapshot: () => snapshot,
		imagePreviews: () => [{ data: "AQID" }],
		removeImage: () => {
			snapshot = empty;
		},
	} as unknown as DraftDocument;
}

const pending = { pending: [] };
const selection = {
	subscribe: () => () => {},
	getSnapshot: () => pending,
	remove: () => {},
} as unknown as ImageSelection;

function strip(document = documentWithImage()) {
	return render(createElement(ImageAttachments, { document, selection }));
}

it("removes an image from a control whose touch reaches 44pt, drawn as a symbol", () => {
	const tree = strip();
	const remove = tree.root.findAll(
		(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Remove image 1: photo.png",
	)[0];
	if (!remove) throw new Error("no remove control");
	// The computed target: the visible square plus its slop, per axis.
	const { width, height } = remove.props.style;
	const slop = remove.props.hitSlop;
	const eachSide = typeof slop === "number" ? slop : (slop?.left ?? 0);
	expect(width + 2 * eachSide).toBeGreaterThanOrEqual(44);
	expect(height + 2 * (typeof slop === "number" ? slop : (slop?.top ?? 0))).toBeGreaterThanOrEqual(44);
	// The mark is a symbol; no text glyph remains.
	expect(remove.findByType("SymbolView" as never).props.name).toBe("xmark");
	expect(tree.root.findAll((node) => String(node.type) === "Text" && node.children.includes("×"))).toEqual([]);
	const document = documentWithImage();
	const pressing = strip(document);
	const control = pressing.root.findAll(
		(node) => String(node.type) === "Pressable" && node.props.accessibilityLabel === "Remove image 1: photo.png",
	)[0];
	act(() => control.props.onPress());
	// The control asked the document to drop the image.
	expect(document.getSnapshot().record.images).toEqual([]);
});
