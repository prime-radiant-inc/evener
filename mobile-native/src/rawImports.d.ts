// A `?raw` import is a file's text as a string, the way Vite (and so Vitest)
// loads it. The package's test fixtures read the daemon's recorded wire this
// way (@evener/appwire-client/testing/notificationWireFixtures), and native
// tests import them; the web app gets the same declaration from vite/client.
declare module "*?raw" {
	const text: string;
	export default text;
}
