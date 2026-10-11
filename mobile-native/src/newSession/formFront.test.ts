import { expect, it } from "vitest";
import { creationStore, forgetCreationForHub } from "./creations";
import { type FormFront, formFront, showForm } from "./formFront";

const noDrafts = () => ({ read: () => null, write: () => {}, clear: () => {} });

it("puts back the form underneath when the newer one on a store goes (#3104)", () => {
	const store = creationStore("hub-a", noDrafts);
	// Only which form is in front matters here, so the navigation is a stub.
	const front = () =>
		({
			navigation: { isFocused: () => true, getParent: () => undefined },
			latest: { current: { ready: true, client: null } },
		}) as FormFront;
	const older = front();
	const newer = front();
	const releaseOlder = showForm(store, older);
	const releaseNewer = showForm(store, newer);
	expect(formFront(store)).toBe(newer);
	releaseNewer();
	expect(formFront(store)).toBe(older);
	// Released out of order, the newest still in place stays in front.
	const releaseAgain = showForm(store, newer);
	releaseOlder();
	expect(formFront(store)).toBe(newer);
	releaseAgain();
	expect(formFront(store)).toBeUndefined();
	forgetCreationForHub("hub-a");
});
