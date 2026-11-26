import { isWorkspacePath } from "../location";

// Only the hub's UI documents speak for the phone. Session files and other
// same-origin responses can contain the user's own content.
export function hubPagePath(origin: string, address: string): string | null {
	let url: URL;
	try {
		url = new URL(address);
	} catch {
		return null;
	}
	if (url.origin !== origin || url.username || url.password || url.searchParams.has("token")) return null;
	return isWorkspacePath(url.pathname) ? url.pathname : null;
}

// Mirrors hubedge.cookieName: FNV-1a of the token, encoded as eight
// big-endian bytes in raw base64url. The hub renews it as an HttpOnly cookie
// on this first document response, before WebKit requests assets or /rpc.
function hubCookie(token: string): string {
	let hash = 0xcbf29ce484222325n;
	for (let i = 0; i < token.length; i++) {
		hash = BigInt.asUintN(64, (hash ^ BigInt(token.charCodeAt(i))) * 0x100000001b3n);
	}
	const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_";
	let suffix = "";
	const padded = hash << 2n;
	for (let shift = 60n; shift >= 0n; shift -= 6n) suffix += alphabet[Number((padded >> shift) & 63n)];
	return `evener_hub_auth_${suffix}=${token}`;
}

export function hubPageSource(origin: string, path: string, token: string) {
	const route = path.startsWith("/") ? hubPagePath(origin, origin + path) : null;
	const uri = origin + (route ?? "/");
	if (!token) return { uri };
	// Only an RFC 6265 cookie-octet may enter a Cookie header.
	if (!/^[\x21\x23-\x2b\x2d-\x3a\x3c-\x5b\x5d-\x7e]+$/.test(token)) return null;
	return { uri, headers: { Cookie: hubCookie(token) } };
}
