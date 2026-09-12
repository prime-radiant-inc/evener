import {
	DEFAULT_BINDINGS,
	registerDefaultBindings,
} from "../../cmd/evener-hub/frontend/src/keybindings/defaults";
import { ACTION_DISPLAY_ROWS } from "../../cmd/evener-hub/frontend/src/keybindings/display";
import { createKeybindingsRegistry } from "../../cmd/evener-hub/frontend/src/keybindings/registry";
import {
	type KeybindingsPlatform,
	validateOverrideRules,
} from "../../cmd/evener-hub/frontend/src/keybindings/validation";
import type { KeybindingsRule } from "../../cmd/evener-hub/frontend/src/protocol/types.gen";

const actionIds = new Set(ACTION_DISPLAY_ROWS.map((row) => row.actionId));

function resolveModifierAlias(chord: string, platform: KeybindingsPlatform) {
	const modifier = platform === "apple" ? "Meta" : "Control";
	// Use tinykeys' press and modifier boundaries; the final token is a key
	// or pattern, whose literal $mod text must remain untouched.
	return chord
		.split(" ")
		.map((press) => {
			const parts = press.split(/(?<=\w|\])\+/);
			return parts
				.map((part, index) => {
					if (index === parts.length - 1) return part;
					if (part === "$mod") return modifier;
					if (part === "[$mod]") return `[${modifier}]`;
					return part;
				})
				.join("+");
		})
		.join(" ");
}

export function replaceKeybindingRule(
	rules: readonly KeybindingsRule[],
	actionId: string,
	chord?: string | null,
): KeybindingsRule[] {
	if (!actionIds.has(actionId))
		throw new Error("This shortcut belongs to a newer version of Evener.");
	return [
		...rules.filter((rule) => rule.action !== actionId),
		...(chord === undefined ? [] : [{ action: actionId, chord }]),
	];
}

export function keybindingPreview(
	rules: readonly KeybindingsRule[],
	platform: KeybindingsPlatform = "apple",
) {
	const registry = createKeybindingsRegistry();
	registerDefaultBindings(registry);
	// Native has no browser navigator. Resolve the portable alias explicitly;
	// default registrations already contain both Command and Control bindings.
	const transformed = rules.map((rule) => ({
		...rule,
		chord:
			rule.chord === null ? null : resolveModifierAlias(rule.chord, platform),
	}));
	const result = validateOverrideRules(transformed, registry, platform);
	const effective = new Set(result.rules.map((rule) => rule.action));
	// Validation skips invalid repeats and removes conflicting candidates.
	// Only surviving effective actions may use their last unreported source;
	// a rejected candidate must show defaults, not promote an earlier repeat.
	const rejected = new Set(result.warnings.map((warning) => warning.rule));
	const authored = new Map<string, string | null>();
	transformed.forEach((rule, index) => {
		const source = rules[index];
		if (source && !rejected.has(rule)) authored.set(rule.action, source.chord);
	});
	return {
		rows: ACTION_DISPLAY_ROWS.map((row) => {
			const chord = authored.get(row.actionId);
			const defaults = DEFAULT_BINDINGS.filter(
				(binding) => binding.actionId === row.actionId,
			)
				.map((binding) => binding.chord)
				.filter((value): value is string => typeof value === "string");
			return {
				...row,
				shortcuts: effective.has(row.actionId)
					? typeof chord === "string"
						? [chord]
						: []
					: defaults,
				customized: effective.has(row.actionId),
			};
		}),
		warnings: result.warnings.map((warning) => ({
			...warning,
			rule: rules[transformed.indexOf(warning.rule)] ?? warning.rule,
		})),
	};
}

export function checkedKeybindingChange(
	rules: readonly KeybindingsRule[],
	actionId: string,
	chord?: string | null,
): KeybindingsRule[] {
	const next = replaceKeybindingRule(rules, actionId, chord);
	const prior = keybindingPreview(rules).warnings;
	const added = keybindingPreview(next).warnings.find(
		(warning) =>
			!prior.some(
				(old) =>
					old.reason === warning.reason &&
					old.rule.action === warning.rule.action &&
					old.rule.chord === warning.rule.chord &&
					old.conflictWith === warning.conflictWith,
			),
	);
	if (added) throw new Error(added.message);
	return next;
}
