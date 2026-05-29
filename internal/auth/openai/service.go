package openai

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

const refreshSkew = 5 * time.Minute

// defaultConcurrentLoginWatchInterval is how often LoginWithDevice polls the
// on-disk auth file to detect that a parallel `serf openai login` succeeded.
// One second is well below the 5-second device-code poll interval and still
// imperceptible to a user waiting on the device flow.
const defaultConcurrentLoginWatchInterval = time.Second

const (
	AuthSourceSignedOut = "signed-out"
	AuthSourceEnv       = "env"
	AuthSourceOAuth     = "oauth"
)

var ErrLoginRequired = errors.New("openai login required")

type AuthStatus struct {
	SignedIn     bool
	Source       string
	Email        string
	AccountID    string
	WorkspaceID  string
	Expiry       time.Time
	NeedsRefresh bool
	NeedsLogin   bool
}

type RuntimeCredentials struct {
	BearerToken string
	Source      string
	Expiry      time.Time
}

type callbackServer interface {
	RedirectURI() string
	Wait(ctx context.Context) (CallbackResult, error)
	Close() error
}

type Service struct {
	cfg                 Config
	client              *http.Client
	now                 func() time.Time
	openBrowser         func(string) error
	readRedirectURL     func(context.Context) (string, error)
	startCallbackServer func(Config, int, string) (callbackServer, error)
	exchangeCode        func(context.Context, *http.Client, Config, TokenExchangeRequest) (TokenSet, error)
	refreshToken        func(context.Context, *http.Client, Config, RefreshTokenRequest) (TokenSet, error)
	requestDeviceCode   func(context.Context, *http.Client, Config) (DeviceCode, error)
	pollDeviceAuth      func(context.Context, *http.Client, Config, DeviceCode) (DeviceCodeSuccess, error)
	exchangeDeviceCode  func(context.Context, *http.Client, Config, string, string) (TokenSet, error)

	// concurrentLoginWatchInterval controls how often LoginWithDevice's
	// watcher goroutine polls the on-disk auth file. Tests override this to
	// sub-second values; production stays at one second.
	concurrentLoginWatchInterval time.Duration

	// notifyConcurrentLogin is invoked once when the watcher detects that a
	// parallel login wrote fresh OAuth state, so callers can surface a
	// message to the user explaining why the command is exiting before the
	// device code was entered. Nil by default.
	notifyConcurrentLogin func()
}

func NewService(cfg Config, client *http.Client) *Service {
	resolvedCfg := mergeConfigDefaults(cfg)
	if client == nil {
		client = &http.Client{Timeout: resolvedCfg.HTTPTimeout}
	}

	return &Service{
		cfg:         resolvedCfg,
		client:      client,
		now:         time.Now,
		openBrowser: defaultBrowserOpener,
		readRedirectURL: func(ctx context.Context) (string, error) {
			<-ctx.Done()
			return "", ctx.Err()
		},
		startCallbackServer: func(cfg Config, port int, expectedState string) (callbackServer, error) {
			return StartCallbackServer(cfg, port, expectedState)
		},
		exchangeCode:                 ExchangeCode,
		refreshToken:                 RefreshToken,
		requestDeviceCode:            RequestDeviceCode,
		pollDeviceAuth:               PollDeviceAuth,
		exchangeDeviceCode:           ExchangeDeviceCode,
		concurrentLoginWatchInterval: defaultConcurrentLoginWatchInterval,
	}
}

// WithConcurrentLoginNotifier registers a callback that LoginWithDevice
// invokes once if it exits early because a parallel `serf openai login`
// wrote fresh OAuth state to disk. Callers typically use this to print a
// "Detected concurrent login; using existing OAuth state." line so the user
// understands why the CLI exited without authorizing the device code.
func (s *Service) WithConcurrentLoginNotifier(notify func()) *Service {
	s.notifyConcurrentLogin = notify
	return s
}

func (s *Service) WithBrowserOpener(openBrowser func(string) error) *Service {
	if openBrowser != nil {
		s.openBrowser = openBrowser
	}
	return s
}

func (s *Service) WithManualRedirectReader(readRedirectURL func(context.Context) (string, error)) *Service {
	if readRedirectURL != nil {
		s.readRedirectURL = readRedirectURL
	}
	return s
}

func (s *Service) Login(ctx context.Context, stateDir, instanceName string) (AuthStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	state, err := GenerateState()
	if err != nil {
		return AuthStatus{}, fmt.Errorf("generate OAuth state: %w", err)
	}
	verifier, challenge, err := GeneratePKCE()
	if err != nil {
		return AuthStatus{}, fmt.Errorf("generate PKCE values: %w", err)
	}

	callback, err := s.startCallbackServer(s.config(), DefaultCallbackPort, state)
	if err != nil {
		return AuthStatus{}, err
	}
	defer callback.Close()

	redirectURI := callback.RedirectURI()
	authorizeURL, err := s.config().AuthorizeURL(AuthorizeURLOptions{
		RedirectURI:   redirectURI,
		State:         state,
		CodeChallenge: challenge,
		OpenBrowser:   true,
	})
	if err != nil {
		return AuthStatus{}, err
	}

	_ = s.openBrowser(authorizeURL)

	loginCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	callbackResults := make(chan callbackResult, 1)
	go func() {
		result, err := callback.Wait(loginCtx)
		callbackResults <- callbackResult{result: result, err: err}
	}()

	manualResults := make(chan callbackResult, 1)
	go func() {
		rawRedirectURL, err := s.readRedirectURL(loginCtx)
		if err != nil {
			manualResults <- callbackResult{err: err}
			return
		}
		code, returnedState, err := ParseRedirectURL(rawRedirectURL)
		if err != nil {
			manualResults <- callbackResult{err: err}
			return
		}
		if err := ValidateState(state, returnedState); err != nil {
			manualResults <- callbackResult{err: err}
			return
		}
		manualResults <- callbackResult{result: CallbackResult{Code: code, State: returnedState}}
	}()

	result, err := waitForLoginCompletion(ctx, callbackResults, manualResults)
	if err != nil {
		return AuthStatus{}, err
	}
	cancel()

	tokens, err := s.exchangeCode(ctx, s.client, s.config(), TokenExchangeRequest{
		Code:         result.Code,
		RedirectURI:  redirectURI,
		CodeVerifier: verifier,
	})
	if err != nil {
		return AuthStatus{}, err
	}

	record := authRecordFromTokens(s.now(), tokens)
	if claims, err := ParseIDTokenClaims(tokens.IDToken); err == nil {
		applyClaims(&record, claims)
	}

	if err := SaveAuth(stateDir, instanceName, record); err != nil {
		return AuthStatus{}, err
	}
	return s.statusFromRecord(record), nil
}

// LoginWithDevice runs the OpenAI device-code flow. It is the headless
// counterpart to Login: the caller's showPrompt is invoked once with the
// verification URL and short user code that the user must enter on a separate
// device. PollDeviceAuth then blocks until the user completes the flow or the
// 15-minute window expires.
func (s *Service) LoginWithDevice(ctx context.Context, stateDir, instanceName string, showPrompt func(DeviceCode)) (AuthStatus, error) {
	if ctx == nil {
		ctx = context.Background()
	}

	// Mark when this login started before any network/prompt work, so the
	// watcher can detect a parallel browser login that completes while the
	// device-code setup is still in progress.
	startedAt := s.now()

	dc, err := s.requestDeviceCode(ctx, s.client, s.config())
	if err != nil {
		return AuthStatus{}, err
	}

	if showPrompt != nil {
		showPrompt(dc)
	}

	// Wrap the parent context so a parallel-login detection can cancel the
	// poll without disturbing the caller's context. A separate flag tells
	// LoginWithDevice why the context ended — ctx.Err() alone cannot
	// distinguish user-cancel from watcher-cancel.
	pollCtx, cancelPoll := context.WithCancel(ctx)
	defer cancelPoll()

	var (
		parallelMu       sync.Mutex
		parallelDetected bool
		parallelRecord   AuthRecord
	)
	markParallel := func(r AuthRecord) {
		parallelMu.Lock()
		defer parallelMu.Unlock()
		if !parallelDetected {
			parallelDetected = true
			parallelRecord = r
		}
	}
	readParallel := func() (bool, AuthRecord) {
		parallelMu.Lock()
		defer parallelMu.Unlock()
		return parallelDetected, parallelRecord
	}

	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		s.watchForConcurrentLogin(pollCtx, stateDir, instanceName, startedAt, func(r AuthRecord) {
			markParallel(r)
			cancelPoll()
		})
	}()

	success, pollErr := s.pollDeviceAuth(pollCtx, s.client, s.config(), dc)
	cancelPoll()
	<-watcherDone

	if detected, record := readParallel(); detected {
		if s.notifyConcurrentLogin != nil {
			s.notifyConcurrentLogin()
		}
		return s.statusFromRecord(record), nil
	}

	if pollErr != nil {
		return AuthStatus{}, pollErr
	}

	tokens, err := s.exchangeDeviceCode(ctx, s.client, s.config(), success.AuthorizationCode, success.CodeVerifier)
	if err != nil {
		return AuthStatus{}, err
	}

	record := authRecordFromTokens(s.now(), tokens)
	if claims, err := ParseIDTokenClaims(tokens.IDToken); err == nil {
		applyClaims(&record, claims)
	}

	if err := SaveAuth(stateDir, instanceName, record); err != nil {
		return AuthStatus{}, err
	}
	return s.statusFromRecord(record), nil
}

// watchForConcurrentLogin polls the auth state file every
// concurrentLoginWatchInterval and invokes onDetected exactly when a parallel
// `serf openai login` has written a fresh OAuth record — that is, the record
// is parseable, was sourced from OAuth (not the env fallback), and was
// obtained at or after the device-code flow began. Pre-existing records are
// intentionally ignored: the user explicitly invoked the command and likely
// wants to overwrite stale state.
//
// LoadAuth is used (rather than os.Stat + ModTime) so a half-written file
// caught mid-rename simply fails validation and the watcher keeps polling.
func (s *Service) watchForConcurrentLogin(ctx context.Context, stateDir, instanceName string, startedAt time.Time, onDetected func(AuthRecord)) {
	interval := s.concurrentLoginWatchInterval
	if interval <= 0 {
		interval = defaultConcurrentLoginWatchInterval
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			record, err := LoadAuth(stateDir, instanceName)
			if err != nil {
				// Missing, corrupt, or mid-write — nothing to do; the next
				// tick will retry.
				continue
			}
			if record.Source != AuthSourceOAuth {
				continue
			}
			if record.ObtainedAt.Before(startedAt) {
				continue
			}
			onDetected(record)
			return
		}
	}
}

func (s *Service) Status(stateDir, instanceName string) (AuthStatus, error) {
	// Prefer a stored OAuth record over OPENAI_API_KEY: once a user explicitly
	// signs in via `serf openai login`, that intent should win over an env
	// fallback that may have been set globally.
	record, err := LoadAuth(stateDir, instanceName)
	switch {
	case err == nil:
		return s.statusFromRecord(record), nil
	case errors.Is(err, ErrAuthNotFound):
		// fall through to env fallback below
	default:
		return AuthStatus{}, err
	}

	if envToken := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); envToken != "" {
		return AuthStatus{
			SignedIn: true,
			Source:   AuthSourceEnv,
		}, nil
	}

	return AuthStatus{Source: AuthSourceSignedOut}, nil
}

func (s *Service) Logout(stateDir, instanceName string) (bool, error) {
	return DeleteAuth(stateDir, instanceName)
}

func (s *Service) ResolveRuntimeCredentials(ctx context.Context, stateDir, instanceName string) (RuntimeCredentials, error) {
	// Prefer a stored OAuth record over OPENAI_API_KEY. We only fall back to env
	// when there is no stored auth at all; if a record exists but cannot be
	// refreshed, surface that error instead of silently routing through env —
	// the user explicitly signed in and would be surprised otherwise.
	record, err := LoadAuth(stateDir, instanceName)
	if err != nil {
		if errors.Is(err, ErrAuthNotFound) {
			if envToken := strings.TrimSpace(os.Getenv("OPENAI_API_KEY")); envToken != "" {
				return RuntimeCredentials{
					BearerToken: envToken,
					Source:      AuthSourceEnv,
				}, nil
			}
			return RuntimeCredentials{}, loginRequiredError(err)
		}
		return RuntimeCredentials{}, err
	}

	if !needsRefresh(s.now(), record.Expiry) {
		return RuntimeCredentials{
			BearerToken: record.AccessToken,
			Source:      AuthSourceOAuth,
			Expiry:      record.Expiry,
		}, nil
	}

	if strings.TrimSpace(record.RefreshToken) == "" {
		return RuntimeCredentials{}, loginRequiredError(errors.New("stored auth cannot be refreshed"))
	}

	tokens, err := s.refreshToken(ctx, s.client, s.config(), RefreshTokenRequest{
		RefreshToken: record.RefreshToken,
	})
	if err != nil {
		if isPermanentRefreshError(err) {
			return RuntimeCredentials{}, loginRequiredError(err)
		}
		return RuntimeCredentials{}, fmt.Errorf("refresh OpenAI auth: %w", err)
	}

	refreshed := refreshedAuthRecord(s.now(), record, tokens)
	if claims, err := ParseIDTokenClaims(refreshed.IDToken); err == nil {
		applyClaims(&refreshed, claims)
	}

	if err := SaveAuth(stateDir, instanceName, refreshed); err != nil {
		return RuntimeCredentials{}, err
	}
	return RuntimeCredentials{
		BearerToken: refreshed.AccessToken,
		Source:      AuthSourceOAuth,
		Expiry:      refreshed.Expiry,
	}, nil
}

func (s *Service) statusFromRecord(record AuthRecord) AuthStatus {
	now := s.now()
	return AuthStatus{
		SignedIn:     true,
		Source:       record.Source,
		Email:        record.Email,
		AccountID:    record.AccountID,
		WorkspaceID:  record.WorkspaceID,
		Expiry:       record.Expiry,
		NeedsRefresh: needsRefresh(now, record.Expiry),
		NeedsLogin:   !record.Expiry.IsZero() && !record.Expiry.After(now),
	}
}

func (s *Service) config() Config {
	return mergeConfigDefaults(s.cfg)
}

func authRecordFromTokens(now time.Time, tokens TokenSet) AuthRecord {
	return AuthRecord{
		Version:      1,
		Provider:     "openai",
		Source:       AuthSourceOAuth,
		ObtainedAt:   now,
		TokenType:    firstNonEmpty(tokens.TokenType, "Bearer"),
		Scope:        tokens.Scope,
		AccessToken:  tokens.AccessToken,
		RefreshToken: tokens.RefreshToken,
		IDToken:      tokens.IDToken,
		Expiry:       tokens.Expiry,
	}
}

func refreshedAuthRecord(now time.Time, current AuthRecord, tokens TokenSet) AuthRecord {
	record := authRecordFromTokens(now, tokens)
	if record.RefreshToken == "" {
		record.RefreshToken = current.RefreshToken
	}
	if record.IDToken == "" {
		record.IDToken = current.IDToken
		record.Email = current.Email
		record.AccountID = current.AccountID
		record.WorkspaceID = current.WorkspaceID
	}
	if record.Scope == "" {
		record.Scope = current.Scope
	}
	if record.Expiry.IsZero() {
		record.Expiry = current.Expiry
	}
	return record
}

func applyClaims(record *AuthRecord, claims TokenClaims) {
	record.Email = firstNonEmpty(claims.Email, record.Email)
	record.AccountID = firstNonEmpty(claims.AccountID, record.AccountID)
	record.WorkspaceID = firstNonEmpty(claims.WorkspaceID, record.WorkspaceID)
}

func needsRefresh(now, expiry time.Time) bool {
	if expiry.IsZero() {
		return true
	}
	return !expiry.After(now.Add(refreshSkew))
}

func loginRequiredError(err error) error {
	return fmt.Errorf("%w: run `serf openai login`: %v", ErrLoginRequired, err)
}

func isPermanentRefreshError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "invalid_grant") ||
		strings.Contains(message, "invalid_request") ||
		strings.Contains(message, "unauthorized_client") ||
		strings.Contains(message, "access_denied")
}

func mergeConfigDefaults(cfg Config) Config {
	defaults := DefaultConfig()
	if cfg.IssuerBaseURL != "" {
		defaults.IssuerBaseURL = cfg.IssuerBaseURL
	}
	if cfg.ClientID != "" {
		defaults.ClientID = cfg.ClientID
	}
	if len(cfg.Scopes) > 0 {
		defaults.Scopes = append([]string(nil), cfg.Scopes...)
	}
	if cfg.RedirectPath != "" {
		defaults.RedirectPath = cfg.RedirectPath
	}
	if cfg.HTTPTimeout != 0 {
		defaults.HTTPTimeout = cfg.HTTPTimeout
	}
	if cfg.CallbackTimeout != 0 {
		defaults.CallbackTimeout = cfg.CallbackTimeout
	}
	return defaults
}

func waitForLoginCompletion(
	ctx context.Context,
	callbackResults <-chan callbackResult,
	manualResults <-chan callbackResult,
) (CallbackResult, error) {
	for callbackResults != nil || manualResults != nil {
		select {
		case <-ctx.Done():
			return CallbackResult{}, ctx.Err()
		case result := <-callbackResults:
			if result.err == nil {
				return result.result, nil
			}
			if errors.Is(result.err, context.Canceled) {
				callbackResults = nil
				continue
			}
			if errors.Is(result.err, context.DeadlineExceeded) {
				callbackResults = nil
				continue
			}
			return CallbackResult{}, result.err
		case result := <-manualResults:
			if result.err == nil {
				return result.result, nil
			}
			if errors.Is(result.err, context.Canceled) {
				manualResults = nil
				continue
			}
			if errors.Is(result.err, context.DeadlineExceeded) {
				manualResults = nil
				continue
			}
			return CallbackResult{}, result.err
		}
	}

	if err := ctx.Err(); err != nil {
		return CallbackResult{}, err
	}
	return CallbackResult{}, context.DeadlineExceeded
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func defaultBrowserOpener(rawURL string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Start()
}
