import type { ReactTestInstance } from "react-test-renderer";

function channels(hex: string): number[] {
	return [1, 3, 5].map((index) => Number.parseInt(hex.slice(index, index + 2), 16) / 255);
}

function luminance(channels: number[]): number {
	const [r, g, b] = channels.map((channel) =>
		channel <= 0.04045 ? channel / 12.92 : ((channel + 0.055) / 1.055) ** 2.4,
	);
	return 0.2126 * r + 0.7152 * g + 0.0722 * b;
}

/** WCAG contrast after sRGB alpha compositing, independent of production color mixing. */
export function contrast(foreground: string, background: string, opacity = 1): number {
	const base = channels(background);
	const ink = channels(foreground).map((channel, index) => channel * opacity + base[index] * (1 - opacity));
	const values = [luminance(ink), luminance(base)];
	return (Math.max(...values) + 0.05) / (Math.min(...values) + 0.05);
}

function styleOf(node: ReactTestInstance) {
	const style = typeof node.props.style === "function" ? node.props.style({ pressed: false }) : node.props.style;
	return Object.assign({}, ...[style].flat(Number.POSITIVE_INFINITY));
}

/** Include native ancestor opacity, without counting component wrappers twice. */
export function textContrast(node: ReactTestInstance, background: string): number {
	let opacity = 1;
	for (let current: ReactTestInstance | null = node; current; current = current.parent) {
		if (typeof current.type === "string") opacity *= styleOf(current).opacity ?? 1;
	}
	return contrast(styleOf(node).color, background, opacity);
}
