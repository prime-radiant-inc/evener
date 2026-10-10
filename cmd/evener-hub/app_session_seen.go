package hub

import (
	"context"
	"fmt"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/appserver"
)

// maxSessionSeenMarks bounds one evener/session/seen/set call: opening a row
// sends one mark, and Mark as read on a selection sends one per row.
const maxSessionSeenMarks = 500

// maxSeenThroughLead bounds how far past the hub's clock a seenThrough may be.
// A correct mark echoes a row's turn_ended_at or a session's lastMovedAt,
// both daemon-stamped, so it is never far ahead even from a host whose clock
// drifts. Seen-through only moves forward, so an unbounded future mark would
// hide the session's turns for good.
const maxSeenThroughLead = 24 * time.Hour

func registerSessionSeenHandler(server *appserver.Server, cfg hubcore.WebConfig, navigation *NavigationService) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSessionSeenSet, func(ctx context.Context, params appwire.SessionSeenSetParams) (appwire.SessionSeenSetResponse, error) {
		return sessionSeenSet(ctx, cfg, navigation, params, time.Now())
	})
}

// sessionSeenSet answers evener/session/seen/set (S4). Every mark is checked
// before any is written, so a malformed call changes nothing. A mark names its
// session by the row's ref as sent; it is not resolved to a current session,
// because the projection reads markers by that same ref. The validated marks
// are then written in MarkBatch's one transaction, so a write failure partway
// through a multi-session call cannot leave some sessions marked and others
// not - the same all-or-nothing guarantee the validation above gives the
// checks themselves.
func sessionSeenSet(ctx context.Context, cfg hubcore.WebConfig, navigation *NavigationService, params appwire.SessionSeenSetParams, now time.Time) (appwire.SessionSeenSetResponse, error) {
	if len(params.Sessions) == 0 || len(params.Sessions) > maxSessionSeenMarks {
		return appwire.SessionSeenSetResponse{}, appwire.InvalidParams(fmt.Sprintf("sessions must name 1 to %d sessions", maxSessionSeenMarks))
	}
	if cfg.SessionSeen == nil {
		return appwire.SessionSeenSetResponse{}, appwire.InternalError("seen marker store not configured")
	}
	marks := make([]hubcore.SessionSeenMark, 0, len(params.Sessions))
	for _, mark := range params.Sessions {
		ref, err := hubapi.ParseRef(mark.Ref)
		if err != nil {
			return appwire.SessionSeenSetResponse{}, appwire.InvalidParams("sessions[].ref must be a session ref: " + err.Error())
		}
		if mark.SeenThrough < 0 || (mark.SeenThrough > 0) == mark.Unread {
			return appwire.SessionSeenSetResponse{}, appwire.InvalidParams("each session sets exactly one of seenThrough or unread")
		}
		if mark.SeenThrough > now.Add(maxSeenThroughLead).UnixMilli() {
			return appwire.SessionSeenSetResponse{}, appwire.InvalidParams("sessions[].seenThrough is a hub or daemon timestamp and cannot be a day past the hub's clock")
		}
		source := hubcore.NormalizeDecisionSource(ref.HostID)
		if err := validateDecisionSource(cfg, source); err != nil {
			return appwire.SessionSeenSetResponse{}, err
		}
		marks = append(marks, hubcore.SessionSeenMark{Source: source, SessionID: ref.SessionID, SeenThrough: hubcore.UnixMilliTime(mark.SeenThrough), Unread: mark.Unread})
	}
	changed, err := cfg.SessionSeen.MarkBatch(marks)
	if err != nil {
		return appwire.SessionSeenSetResponse{}, appwire.InternalError("seen marker store error: " + err.Error())
	}
	mutation, err := commitNavigationChange(ctx, cfg, navigation, changed)
	if err != nil {
		return appwire.SessionSeenSetResponse{}, err
	}
	return appwire.SessionSeenSetResponse{OK: true, Changed: changed, Navigation: mutation}, nil
}
