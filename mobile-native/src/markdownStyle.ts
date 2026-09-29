// The one markdown style builder the transcript and the Reader share. Each
// passes the text roles it reads in (spec 16.2): the transcript its agent
// prose and system-font headings, the Reader its document role and serif
// headings. Everything else (lists, quotes, code, tables, links) is the same.
import { Platform } from "react-native";
import type { MarkdownStyle } from "react-native-enriched-markdown";
import { fonts } from "./design/tokens";
import type { useColors } from "./ui";

// The platform monospace face for code spans and code blocks. iOS resolves the
// app's Mono token ("Menlo" is iOS-only); Android does not, so it falls back to
// the platform monospace face. A function, not a module constant, so the
// current OS is read at each call (the same per-call read this module had when
// the value was a local).
export function codeFontFamily(): string {
	return Platform.OS === "ios" ? fonts.mono : "monospace";
}

export interface TextRole {
	fontFamily?: string;
	fontSize: number;
	lineHeight: number;
	fontWeight?: string;
}

export interface MarkdownRoles {
	body: TextRole;
	/** h1, h2, and h3 and deeper. */
	headings: readonly [TextRole, TextRole, TextRole];
}

export function markdownStyle(colors: ReturnType<typeof useColors>, roles: MarkdownRoles): MarkdownStyle {
	const body = {
		...roles.body,
		color: colors.palette.prose,
		marginTop: 0,
		marginBottom: 12,
	};
	const heading = (role: TextRole) => ({
		color: colors.text,
		marginTop: 20,
		marginBottom: 8,
		...role,
	});
	const h1 = heading(roles.headings[0]);
	const h2 = heading(roles.headings[1]);
	const h3 = heading(roles.headings[2]);
	return {
		paragraph: body,
		h1,
		h2,
		h3,
		h4: h3,
		h5: h3,
		h6: h3,
		list: {
			...body,
			bulletColor: colors.secondary,
			markerColor: colors.secondary,
			markerFontWeight: "normal",
			gapWidth: 8,
			itemSpacing: 4,
		},
		blockquote: {
			...body,
			color: colors.secondary,
			backgroundColor: colors.background,
			borderColor: colors.border,
			borderWidth: 2,
			gapWidth: 12,
		},
		link: { color: colors.accent, underline: true },
		code: {
			color: colors.text,
			backgroundColor: colors.surface,
			borderColor: colors.border,
			fontSize: 14,
			fontFamily: codeFontFamily(),
		},
		codeBlock: {
			fontSize: 14,
			lineHeight: 21,
			color: colors.text,
			backgroundColor: colors.surface,
			borderColor: colors.border,
			borderRadius: 10,
			padding: 12,
			marginTop: 8,
			marginBottom: 12,
			fontFamily: codeFontFamily(),
			syntaxColors: {
				keyword: colors.accent,
				operator: colors.text,
				punctuation: colors.secondary,
				string: colors.palette.scheme === "dark" ? "#b8d8a3" : "#2e6443",
				number: colors.palette.scheme === "dark" ? "#ecc48d" : "#785119",
				constant: colors.accent,
				comment: colors.secondary,
				function: colors.accent,
				type: colors.accent,
				variable: colors.text,
				property: colors.text,
				tag: colors.accent,
				attribute: colors.accent,
				embedded: colors.text,
			},
		},
		table: {
			marginTop: body.marginTop,
			marginBottom: body.marginBottom,
			lineHeight: body.lineHeight,
			fontSize: 14,
			color: colors.text,
			headerTextColor: colors.text,
			headerBackgroundColor: colors.surface,
			rowEvenBackgroundColor: colors.background,
			rowOddBackgroundColor: colors.background,
			borderColor: colors.border,
			borderWidth: 1,
			cellPaddingHorizontal: 12,
			cellPaddingVertical: 10,
		},
		thematicBreak: {
			color: colors.border,
			height: 1,
			marginTop: 16,
			marginBottom: 16,
		},
		taskList: {
			checkedColor: colors.palette.accentFill,
			checkedTextColor: colors.text,
			borderColor: colors.secondary,
		},
		image: { maxHeight: 320, resizeMode: "contain", borderRadius: 10 },
		math: { color: colors.text, backgroundColor: colors.surface },
		inlineMath: { color: colors.text },
	};
}
