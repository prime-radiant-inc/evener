// A stack navigator on React Navigation's real core, for tests that pin how a
// navigation action treats the stack (a leave guard, a nested navigate)
// without native-stack, which vitest cannot render.
import { createNavigatorFactory, StackRouter, useNavigationBuilder } from "@react-navigation/core";
import { Fragment, type ReactNode } from "react";

/** A stack that renders every route it holds, as a native stack keeps the
 * screens under the top one mounted. */
function TestStack({ children, initialRouteName }: { children: ReactNode; initialRouteName?: string }) {
	const { state, descriptors, NavigationContent } = useNavigationBuilder(StackRouter, { children, initialRouteName });
	return (
		<NavigationContent>
			{state.routes.map((route) => (
				<Fragment key={route.key}>{descriptors[route.key]?.render()}</Fragment>
			))}
		</NavigationContent>
	);
}

export const createTestStack = createNavigatorFactory(TestStack);

/** A page whose content a test never reads. Declared once at module scope:
 * React Navigation warns about a component written inline in a Screen,
 * which would be a new type on every render. */
export function BlankPage() {
	return null;
}
