// A stand-in for @react-navigation/native's usePreventRemove, for tests of a
// pushed page: it records what the page last asked for, so a test's Back can
// go through the guard (providersPageTestUtils.tsx's back). It imports nothing,
// so a vi.mock factory of @react-navigation/native can load it.

/** What usePreventRemove was last given. */
export const backGuard: {
	current: { prevent: boolean; onPrevent: (event: { data: { action: unknown } }) => void } | null;
} = { current: null };

export function usePreventRemoveMock(
	prevent: boolean,
	onPrevent: (event: { data: { action: unknown } }) => void,
): void {
	backGuard.current = { prevent, onPrevent };
}
