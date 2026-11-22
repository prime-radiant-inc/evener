package githubcopilot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ErrLoginRequired reports that the instance cannot produce a runtime
// credential without a fresh device login.
var ErrLoginRequired = errors.New("github copilot login required")

// AuthStatus is a local snapshot of an instance's stored authentication.
type AuthStatus struct {
	SignedIn     bool
	Source       string
	AccountID    string
	Email        string
	Expiry       time.Time
	NeedsRefresh bool
	NeedsLogin   bool
}

// RuntimeCredentials is the current bearer credential for Copilot requests.
type RuntimeCredentials struct {
	BearerToken string
	AccountID   string
	Expiry      time.Time
}

// Service drives the Copilot record lifecycle; sleep is a test seam.
type Service struct {
	cfg    Config
	client *http.Client
	sleep  func(context.Context, time.Duration) error
}

// beforeRecordLock is a test seam that runs immediately before a record-lock
// acquisition, letting process tests signal the lock wait.
var beforeRecordLock = func() {}

// NewService resolves cfg defaults and creates a client when none is supplied.
func NewService(cfg Config, client *http.Client) *Service {
	cfg = mergeConfigDefaults(cfg)
	return &Service{cfg: cfg, client: httpClient(client, cfg), sleep: ctxSleep}
}

// LoginWithDevice runs the device flow, shows the user code once, polls until
// authorization or expiry, and saves the account-bound record under the record
// lock. A fresh login wins over any existing record.
func (s *Service) LoginWithDevice(ctx context.Context, stateDir, instance string, showPrompt func(DeviceCode)) (AuthStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	dc, err := RequestDeviceCode(ctx, s.client, s.cfg)
	if err != nil {
		return AuthStatus{}, err
	}
	if showPrompt != nil {
		showPrompt(dc)
	}
	for {
		if err := ctx.Err(); err != nil {
			return AuthStatus{}, err
		}
		tokens, state, interval, err := PollDeviceOnce(ctx, s.client, s.cfg, dc)
		if err != nil {
			return AuthStatus{}, err
		}
		// Carry slow_down's effective interval into the next poll.
		dc.Interval = interval
		switch state {
		case PollAuthorized:
			return s.saveLogin(ctx, stateDir, instance, tokens)
		case PollExpired:
			return AuthStatus{}, ErrDeviceExpired
		}
		if err := s.sleep(ctx, interval); err != nil {
			return AuthStatus{}, err
		}
	}
}

func (s *Service) saveLogin(ctx context.Context, stateDir, instance string, tokens TokenSet) (AuthStatus, error) {
	if err := validateTokenSet(tokens, true); err != nil {
		return AuthStatus{}, err
	}
	account, err := FetchAccount(ctx, s.client, s.cfg, tokens.AccessToken)
	if err != nil {
		return AuthStatus{}, err
	}
	record := AuthRecord{Instance: instance, Scheme: AuthScheme, Version: 1,
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		TokenType: firstNonEmpty(tokens.TokenType, "Bearer"), Scope: firstNonEmpty(tokens.Scope, s.cfg.scope()),
		ObtainedAt: nowFunc(), AccessExpiry: tokens.AccessExpiry, RefreshExpiry: tokens.RefreshExpiry,
		AccountID: account.ID, Email: account.Email}
	beforeRecordLock()
	if err := WithRecordLock(stateDir, instance, func() error {
		return SaveAuth(stateDir, instance, record)
	}); err != nil {
		return AuthStatus{}, err
	}
	return statusFromRecord(record), nil
}

// Status is local: it reports stored-record state without contacting GitHub.
func (s *Service) Status(stateDir, instance string) (AuthStatus, error) {
	record, err := LoadAuth(stateDir, instance)
	if errors.Is(err, ErrAuthNotFound) {
		return AuthStatus{Source: AuthSourceSignedOut}, nil
	}
	if err != nil {
		return AuthStatus{}, err
	}
	return statusFromRecord(record), nil
}

// Logout deletes the local record under the record lock and reports whether one
// existed. A corrupt record is still removed; deleting a missing record is not
// an error.
func (s *Service) Logout(stateDir, instance string) (bool, error) {
	existed := false
	beforeRecordLock()
	err := WithRecordLock(stateDir, instance, func() error {
		_, err := LoadAuth(stateDir, instance)
		switch {
		case err == nil, errors.Is(err, ErrAuthCorrupt):
			existed = true
		case errors.Is(err, ErrAuthNotFound):
			return nil
		default:
			return err
		}
		return DeleteAuth(stateDir, instance)
	})
	return existed, err
}

// ResolveRuntimeCredentials returns a usable bearer token, refreshing five
// minutes before access expiry under the record lock. Missing, rejected, or
// unrefreshable records return ErrLoginRequired; a transient failure stays an
// ordinary error so the caller can retry.
func (s *Service) ResolveRuntimeCredentials(ctx context.Context, stateDir, instance string) (RuntimeCredentials, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	record, err := LoadAuth(stateDir, instance)
	if errors.Is(err, ErrAuthNotFound) {
		return RuntimeCredentials{}, loginRequired(err)
	}
	if err != nil {
		return RuntimeCredentials{}, err
	}
	if record.NeedsLogin(nowFunc()) {
		return RuntimeCredentials{}, loginRequired(errors.New("stored refresh token cannot recover this record"))
	}
	if !needsRefresh(nowFunc(), record.AccessExpiry) {
		return runtimeFromRecord(record), nil
	}
	return s.refreshUnderLock(ctx, stateDir, instance)
}

func (s *Service) refreshUnderLock(ctx context.Context, stateDir, instance string) (credentials RuntimeCredentials, err error) {
	beforeRecordLock()
	err = WithRecordLock(stateDir, instance, func() error {
		current, err := LoadAuth(stateDir, instance)
		if errors.Is(err, ErrAuthNotFound) {
			return loginRequired(err)
		}
		if err != nil {
			return err
		}
		if current.NeedsLogin(nowFunc()) {
			return loginRequired(errors.New("stored refresh token cannot recover this record"))
		}
		if !needsRefresh(nowFunc(), current.AccessExpiry) {
			credentials = runtimeFromRecord(current)
			return nil
		}
		// Re-read under the Task 1 lock: a newer login, refresh, or logout wins.
		// This generation is the only one this request may redeem and replace.
		tokens, err := RefreshToken(ctx, s.client, s.cfg, current.RefreshToken)
		if errors.Is(err, ErrBadRefreshToken) {
			return s.recordRejection(stateDir, instance, current)
		}
		if err != nil {
			return fmt.Errorf("refresh GitHub Copilot auth: %w", err)
		}
		refreshed, err := refreshedRecord(current, tokens)
		if err != nil {
			return err
		}
		// The lock makes the record immutable between the re-read and this
		// compare-and-save, stating the stale-writer contract.
		if _, err := LoadAuthIfCurrent(stateDir, instance, current); err != nil {
			return err
		}
		if err := SaveAuth(stateDir, instance, refreshed); err != nil {
			return err
		}
		credentials = runtimeFromRecord(refreshed)
		return nil
	})
	return credentials, err
}

func (s *Service) recordRejection(stateDir, instance string, refused AuthRecord) error {
	// Key the flag to the exact refused token and generation: a newer record
	// must not inherit this rejection.
	current, err := LoadAuthIfCurrent(stateDir, instance, refused)
	if errors.Is(err, ErrAuthNotFound) || errors.Is(err, ErrAuthChanged) {
		return loginRequired(ErrBadRefreshToken)
	}
	if err != nil {
		return err
	}
	current.RefreshRejected = true
	if err := SaveAuth(stateDir, instance, current); err != nil {
		return err
	}
	return loginRequired(ErrBadRefreshToken)
}

func statusFromRecord(record AuthRecord) AuthStatus {
	now := nowFunc()
	return AuthStatus{SignedIn: true, Source: AuthSourceOAuth, AccountID: record.AccountID,
		Email: record.Email, Expiry: record.AccessExpiry,
		NeedsRefresh: needsRefresh(now, record.AccessExpiry), NeedsLogin: record.NeedsLogin(now)}
}

func runtimeFromRecord(record AuthRecord) RuntimeCredentials {
	return RuntimeCredentials{BearerToken: record.AccessToken, AccountID: record.AccountID, Expiry: record.AccessExpiry}
}

func refreshedRecord(current AuthRecord, tokens TokenSet) (AuthRecord, error) {
	if err := validateTokenSet(tokens, false); err != nil {
		return AuthRecord{}, err
	}
	record := current
	record.AccessToken = tokens.AccessToken
	record.TokenType = firstNonEmpty(tokens.TokenType, record.TokenType)
	record.Scope = firstNonEmpty(tokens.Scope, record.Scope)
	if strings.TrimSpace(tokens.RefreshToken) != "" {
		record.RefreshToken = tokens.RefreshToken
	}
	record.ObtainedAt = nowFunc()
	record.AccessExpiry = tokens.AccessExpiry
	if !tokens.RefreshExpiry.IsZero() {
		record.RefreshExpiry = tokens.RefreshExpiry
	}
	record.RefreshRejected = false
	return record, nil
}

func needsRefresh(now, expiry time.Time) bool {
	return expiry.IsZero() || !expiry.After(now.Add(refreshSkew))
}

func loginRequired(err error) error {
	return fmt.Errorf("%w: run `evener github-copilot login`: %w", ErrLoginRequired, err)
}

func ctxSleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(d):
		return nil
	}
}
