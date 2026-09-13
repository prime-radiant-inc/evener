import {
  type DocPort,
  docImageURL,
} from "../../appwire-client/typescript/docContent";

// The native doc seam. Two things the web gets for free have to be supplied
// here: the hub's origin, because nothing on the device is served by the hub,
// and the bearer token, because there is no cookie jar for credentials to ride
// in. Both come from the stored hub the app is connected to, the same pair
// createHubClient takes (connection.ts).
function docAuthHeaders(token: string): Record<string, string> {
  return token ? { Authorization: `Bearer ${token}` } : {};
}

export function nativeDocPort(origin: string, token: string): DocPort {
  // React Native's fetch Response satisfies DocResponseLike structurally, so
  // the platform response goes back to readDocFile untouched.
  return {
    origin,
    fetch: (url) => fetch(url, { headers: docAuthHeaders(token) }),
  };
}

// An <Image> never goes through readDocFile, so the doc image's authentication
// lives on the source: docImageURL's string plus the port's own header set.
export function nativeDocImageSource(
  origin: string,
  token: string,
  session: string,
  path: string,
): { uri: string; headers: Record<string, string> } {
  return {
    uri: docImageURL(origin, session, path),
    headers: docAuthHeaders(token),
  };
}
