import { expect, it, vi } from "vitest";
import { fonts, paletteFor, typeRoles } from "./design/tokens";
import { markdownStyle } from "./markdownStyle";

vi.mock("react-native", () => ({ Platform: { OS: "ios" } }));

const palette = paletteFor("light");
const colors = {
	background: palette.page,
	surface: palette.inset,
	text: palette.inkHi,
	secondary: palette.inkMid,
	border: palette.edge,
	accent: palette.accentInk,
	error: palette.dangerInk,
	warning: palette.attentionInk,
	onAccent: palette.onFill,
	palette,
};
const serifHeading = (fontSize: number, lineHeight: number) => ({
	fontFamily: fonts.serifSemibold,
	fontSize,
	lineHeight,
	fontWeight: "600",
});

it("sets a document's body and headings from the roles it's given", () => {
	const style = markdownStyle(colors, {
		body: typeRoles.document,
		headings: [serifHeading(24, 30), serifHeading(20, 26), serifHeading(18, 24)],
	});
	expect(style.paragraph).toMatchObject({ ...typeRoles.document, color: palette.prose });
	expect(style.list).toMatchObject({ fontFamily: fonts.serif, fontSize: 18 });
	expect(style.h1).toMatchObject({ fontFamily: fonts.serifSemibold, fontSize: 24, color: palette.inkHi });
	expect(style.h2).toMatchObject({ fontSize: 20 });
	expect(style.h3).toMatchObject({ fontSize: 18 });
	// Deeper headings read as the third level.
	expect(style.h6).toEqual(style.h3);
});
