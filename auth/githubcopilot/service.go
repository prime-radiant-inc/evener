package githubcopilot

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"primeradiant.com/evener/envvars"
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
			return s.CompleteLogin(ctx, stateDir, instance, tokens)
		case PollExpired:
			return AuthStatus{}, ErrDeviceExpired
		}
		if err := s.sleep(ctx, interval); err != nil {
			return AuthStatus{}, err
		}
	}
}

// CompleteLogin saves the account-bound record for an already-authorized
// GitHub token set: it prepares the record, then persists it under the record
// lock. LoginWithDevice calls it when its poll is authorized; a caller that
// completed authorization itself calls it with the same token set.
func (s *Service) CompleteLogin(ctx context.Context, stateDir, instance string, tokens TokenSet) (AuthStatus, error) {
	record, err := s.PrepareLogin(ctx, instance, tokens)
	if err != nil {
		return AuthStatus{}, err
	}
	return s.SavePreparedLogin(stateDir, instance, record)
}

// PrepareLogin resolves an already-authorized GitHub token set into the record
// the login saves: it fetches the account, exchanges the GitHub token for a
// Copilot session, and builds the record. It neither saves nor takes the record
// lock, so a caller can keep this network I/O outside its own credential
// critical section and save the record once its fences pass.
func (s *Service) PrepareLogin(ctx context.Context, instance string, tokens TokenSet) (AuthRecord, error) {
	if err := validateTokenSet(tokens); err != nil {
		return AuthRecord{}, err
	}
	account, err := FetchAccount(ctx, s.client, s.cfg, tokens.AccessToken)
	if err != nil {
		return AuthRecord{}, err
	}
	session, err := ExchangeCopilotToken(ctx, s.client, s.cfg, tokens.AccessToken)
	if err != nil {
		return AuthRecord{}, fmt.Errorf("exchange GitHub token for a Copilot session: %w", err)
	}
	return AuthRecord{Instance: instance, Scheme: AuthScheme, Version: 1,
		AccessToken: tokens.AccessToken, RefreshToken: tokens.RefreshToken,
		TokenType: envvars.FirstNonEmpty(tokens.TokenType, "Bearer"), Scope: envvars.FirstNonEmpty(tokens.Scope, s.cfg.scope()),
		ObtainedAt: nowFunc(), AccessExpiry: tokens.AccessExpiry, RefreshExpiry: tokens.RefreshExpiry,
		CopilotToken: session.Token, CopilotExpiry: session.Expiry,
		AccountID: account.ID, Email: account.Email}, nil
}

// SavePreparedLogin persists a record PrepareLogin built, under the record
// lock, and reports the saved authentication.
func (s *Service) SavePreparedLogin(stateDir, instance string, record AuthRecord) (AuthStatus, error) {
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

// ResolveRuntimeCredentials returns the current Copilot session token,
// refreshing an expiring GitHub token and then renewing the Copilot session
// five minutes early under the record lock. Missing, rejected, or
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
	if !needsRefresh(nowFunc(), record.CopilotExpiry) && !githubNeedsRefresh(nowFunc(), record) {
		return runtimeFromRecord(record), nil
	}
	return s.renewUnderLock(ctx, stateDir, instance)
}

// githubNeedsRefresh reports whether a refreshable GitHub access token is
// inside the renewal window. A non-expiring token never needs refreshing.
func githubNeedsRefresh(now time.Time, record AuthRecord) bool {
	return !record.AccessExpiry.IsZero() && needsRefresh(now, record.AccessExpiry)
}

func (s *Service) renewUnderLock(ctx context.Context, stateDir, instance string) (credentials RuntimeCredentials, err error) {
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
		renewed := current
		if githubNeedsRefresh(nowFunc(), current) {
			if current.RefreshToken == "" {
				return loginRequired(errors.New("GitHub access token expired and cannot be refreshed"))
			}
			tokens, err := RefreshToken(ctx, s.client, s.cfg, current.RefreshToken)
			if errors.Is(err, ErrBadRefreshToken) {
				return s.saveRejected(stateDir, instance, current, current, ErrBadRefreshToken)
			}
			if err != nil {
				return fmt.Errorf("refresh GitHub Copilot auth: %w", err)
			}
			renewed, err = refreshedRecord(current, tokens)
			if err != nil {
				return err
			}
		}
		if needsRefresh(nowFunc(), renewed.CopilotExpiry) {
			session, err := ExchangeCopilotToken(ctx, s.client, s.cfg, renewed.AccessToken)
			if errors.Is(err, ErrCopilotTokenRejected) {
				return s.saveRejected(stateDir, instance, current, renewed, ErrCopilotTokenRejected)
			}
			if err != nil {
				if renewed != current {
					if saveErr := s.saveIfCurrent(stateDir, instance, current, renewed); saveErr != nil &&
						!errors.Is(saveErr, ErrAuthChanged) && !errors.Is(saveErr, ErrAuthNotFound) {
						return fmt.Errorf("renew Copilot session: %w (persist refreshed GitHub credentials: %w)", err, saveErr)
					}
				}
				return fmt.Errorf("renew Copilot session: %w", err)
			}
			renewed.CopilotToken = session.Token
			renewed.CopilotExpiry = session.Expiry
			renewed.ObtainedAt = nowFunc()
		}
		if renewed == current {
			credentials = runtimeFromRecord(current)
			return nil
		}
		if err := s.saveIfCurrent(stateDir, instance, current, renewed); err != nil {
			return err
		}
		credentials = runtimeFromRecord(renewed)
		return nil
	})
	return credentials, err
}

// saveRejected marks the current generation rejected and saves rejected, which
// may carry a GitHub token rotation from this renewal attempt. A newer login,
// refresh, or logout wins instead of being overwritten.
func (s *Service) saveRejected(stateDir, instance string, read, rejected AuthRecord, cause error) error {
	// Key the flag to the exact refused token and generation: a newer record
	// must not inherit this rejection.
	rejected.RefreshRejected = true
	if err := s.saveIfCurrent(stateDir, instance, read, rejected); err != nil {
		if errors.Is(err, ErrAuthNotFound) || errors.Is(err, ErrAuthChanged) {
			return loginRequired(cause)
		}
		return err
	}
	return loginRequired(cause)
}

// saveIfCurrent saves next only while read is still the stored generation.
// The lock makes the record immutable between the re-read and this save,
// stating the stale-writer contract.
func (s *Service) saveIfCurrent(stateDir, instance string, read, next AuthRecord) error {
	if _, err := LoadAuthIfCurrent(stateDir, instance, read); err != nil {
		return err
	}
	return SaveAuth(stateDir, instance, next)
}

func statusFromRecord(record AuthRecord) AuthStatus {
	now := nowFunc()
	return AuthStatus{SignedIn: true, Source: AuthSourceOAuth, AccountID: record.AccountID,
		Email: record.Email, Expiry: record.CopilotExpiry,
		NeedsRefresh: needsRefresh(now, record.CopilotExpiry) || githubNeedsRefresh(now, record),
		NeedsLogin:   record.NeedsLogin(now)}
}

func runtimeFromRecord(record AuthRecord) RuntimeCredentials {
	return RuntimeCredentials{BearerToken: record.CopilotToken, AccountID: record.AccountID, Expiry: record.CopilotExpiry}
}

func refreshedRecord(current AuthRecord, tokens TokenSet) (AuthRecord, error) {
	if err := validateTokenSet(tokens); err != nil {
		return AuthRecord{}, err
	}
	record := current
	record.AccessToken = tokens.AccessToken
	record.TokenType = envvars.FirstNonEmpty(tokens.TokenType, record.TokenType)
	record.Scope = envvars.FirstNonEmpty(tokens.Scope, record.Scope)
	record.AccessExpiry = tokens.AccessExpiry
	if tokens.RefreshToken != "" {
		record.RefreshToken = tokens.RefreshToken
		record.RefreshExpiry = tokens.RefreshExpiry
	}
	record.ObtainedAt = nowFunc()
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
