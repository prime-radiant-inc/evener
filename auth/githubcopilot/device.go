package githubcopilot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"time"
)

// nowFunc is the service clock. Tests replace it with a fake clock; production
// leaves time.Now.
var nowFunc = time.Now

// PollState is one device authorization poll's outcome.
type PollState int

const (
	PollPending PollState = iota
	PollAuthorized
	PollExpired
)

var (
	// ErrAccessDenied reports that the user refused the device authorization.
	ErrAccessDenied = errors.New("github device authorization was denied")
	// ErrDeviceExpired reports that the code expired before authorization.
	ErrDeviceExpired = errors.New("github device code expired")
)

// DeviceCode is the display handle and polling state of one device login.
type DeviceCode struct {
	DeviceCode      string
	UserCode        string
	VerificationURL string
	Interval        time.Duration
	Expiry          time.Time
}

type deviceCodePayload struct {
	DeviceCode      string `json:"device_code"`
	UserCode        string `json:"user_code"`
	VerificationURL string `json:"verification_uri"`
	ExpiresIn       int    `json:"expires_in"`
	Interval        int    `json:"interval"`
}

// RequestDeviceCode starts the device flow. Expiry is absolute so a poll can
// refuse an expired code without another network call.
func RequestDeviceCode(ctx context.Context, client *http.Client, cfg Config) (DeviceCode, error) {
	cfg = mergeConfigDefaults(cfg)
	var payload deviceCodePayload
	err := postFormJSON(ctx, client, cfg, "/login/device/code", url.Values{
		"client_id": {cfg.ClientID}, "scope": {cfg.scope()},
	}, &payload)
	if err != nil {
		return DeviceCode{}, err
	}
	if payload.DeviceCode == "" || payload.UserCode == "" || payload.VerificationURL == "" || payload.ExpiresIn <= 0 {
		return DeviceCode{}, fmt.Errorf("%w: missing device authorization fields", ErrMalformedResponse)
	}
	interval := max(time.Duration(payload.Interval)*time.Second, deviceInterval)
	return DeviceCode{
		DeviceCode: payload.DeviceCode, UserCode: payload.UserCode,
		VerificationURL: payload.VerificationURL, Interval: interval,
		Expiry: nowFunc().Add(time.Duration(payload.ExpiresIn) * time.Second),
	}, nil
}

// PollDeviceOnce makes one poll. slow_down adds GitHub's documented five
// seconds to the effective interval; expired_token and a locally past expiry
// both return PollExpired.
func PollDeviceOnce(ctx context.Context, client *http.Client, cfg Config, dc DeviceCode) (TokenSet, PollState, time.Duration, error) {
	cfg = mergeConfigDefaults(cfg)
	interval := max(dc.Interval, deviceInterval)
	if !dc.Expiry.IsZero() && !nowFunc().Before(dc.Expiry) {
		return TokenSet{}, PollExpired, interval, nil
	}
	var payload tokenResponsePayload
	err := postFormJSON(ctx, client, cfg, "/login/oauth/access_token", url.Values{
		"client_id": {cfg.ClientID}, "device_code": {dc.DeviceCode}, "grant_type": {deviceGrantType},
	}, &payload)
	if err != nil {
		return TokenSet{}, PollPending, interval, err
	}
	switch payload.Error {
	case "":
		tokens := payload.tokenSet(nowFunc())
		if err := validateTokenSet(tokens); err != nil {
			return TokenSet{}, PollPending, interval, err
		}
		return tokens, PollAuthorized, interval, nil
	case "authorization_pending":
		return TokenSet{}, PollPending, interval, nil
	case "slow_down":
		return TokenSet{}, PollPending, interval + 5*time.Second, nil
	case "expired_token":
		return TokenSet{}, PollExpired, interval, nil
	case "access_denied":
		return TokenSet{}, PollPending, interval, ErrAccessDenied
	default:
		return TokenSet{}, PollPending, interval, fmt.Errorf("github device authorization failed: %s", payload.Error)
	}
}
