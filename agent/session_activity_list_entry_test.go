package agent

import (
	"context"
	"errors"
	"testing"

	"primeradiant.com/evener/appwire"
)

// The live and retained list entry points of every resource share one
// request contract: refuse a malformed request as InvalidParams before any
// source is touched, and default an unset scope to the session.
func TestSessionActivityListEntryPointsShareRequestContract(t *testing.T) {
	t.Parallel()
	s := newSession(t, withoutGitSnapshot(), withConfig(SessionConfig{StateDir: t.TempDir()}))
	ref := encodeRef("", s.ID())
	stateDir := t.TempDir()

	type listCall func(context.Context, appwire.SessionActivityListParams) (appwire.SessionActivityScope, error)
	entries := map[string]listCall{
		"live jobs": func(ctx context.Context, p appwire.SessionActivityListParams) (appwire.SessionActivityScope, error) {
			r, err := s.ListActivityJobs(ctx, p)
			return r.Scope, err
		},
		"live watches": func(ctx context.Context, p appwire.SessionActivityListParams) (appwire.SessionActivityScope, error) {
			r, err := s.ListActivityWatches(ctx, p)
			return r.Scope, err
		},
		"live delegates": func(ctx context.Context, p appwire.SessionActivityListParams) (appwire.SessionActivityScope, error) {
			r, err := s.ListActivityDelegates(ctx, p)
			return r.Scope, err
		},
		"retained jobs": func(ctx context.Context, p appwire.SessionActivityListParams) (appwire.SessionActivityScope, error) {
			r, err := LoadSessionActivityJobs(ctx, stateDir, s.ID(), p)
			return r.Scope, err
		},
		"retained watches": func(ctx context.Context, p appwire.SessionActivityListParams) (appwire.SessionActivityScope, error) {
			r, err := LoadSessionActivityWatches(ctx, stateDir, s.ID(), p)
			return r.Scope, err
		},
		"retained delegates": func(ctx context.Context, p appwire.SessionActivityListParams) (appwire.SessionActivityScope, error) {
			r, err := LoadSessionActivityDelegates(ctx, stateDir, s.ID(), p)
			return r.Scope, err
		},
	}
	refusals := map[string]appwire.SessionActivityListParams{
		"malformed ref":  {Ref: "not a ref"},
		"unknown scope":  {Ref: ref, Scope: "galaxy"},
		"negative limit": {Ref: ref, Limit: -1},
	}
	for name, call := range entries {
		for refusal, params := range refusals {
			t.Run(name+"/"+refusal, func(t *testing.T) {
				_, err := call(t.Context(), params)
				wire, ok := errors.AsType[appwire.WireError](err)
				if !ok || wire.Code != appwire.CodeInvalidParams {
					t.Fatalf("error = %v, want InvalidParams", err)
				}
			})
		}
	}
	for _, name := range []string{"live jobs", "live watches", "live delegates"} {
		t.Run(name+"/default scope", func(t *testing.T) {
			scope, err := entries[name](t.Context(), appwire.SessionActivityListParams{Ref: ref})
			if err != nil || scope != appwire.SessionActivityScopeSession {
				t.Fatalf("scope=%q err=%v, want the session scope", scope, err)
			}
		})
	}
	for _, name := range []string{"retained jobs", "retained watches", "retained delegates"} {
		t.Run(name+"/other session", func(t *testing.T) {
			_, err := entries[name](t.Context(), appwire.SessionActivityListParams{Ref: encodeRef("", "ses_other")})
			wire, ok := errors.AsType[appwire.WireError](err)
			if !ok || wire.Code != appwire.CodeInvalidParams {
				t.Fatalf("error = %v, want InvalidParams", err)
			}
		})
	}
}
