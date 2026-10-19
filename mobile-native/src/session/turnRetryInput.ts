// The input that opened the exchange a failed turn ended, for the error row's
// Retry (spec 8.2, ruling 26): re-issuing the person's own words and images,
// never a sentence put in their mouth. The lookback, its image-byte recovery
// and the RetryInput/OriginatingInput shapes live in the shared appwire
// package's turnRetryInput module, which the web's TurnFailureEndCap shares;
// this module re-exports them under the path the native screens import.
export {
	type OriginatingInput,
	originatingInput,
	type RetryInput,
} from "@evener/appwire-client";
