package hub

import (
	"context"
	"errors"
	"strings"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/cmd/evener-hub/internal/hubcore"
	"primeradiant.com/evener/hubapi"
	"primeradiant.com/evener/internal/appserver"
)

// pinSession is one pinnable session's source-qualified identity: the owning
// source (empty for the controller's own sessions) and the session's bare ID.
// The pin store keys on exactly this pair.
type pinSession struct {
	source    string
	sessionID string
}

// pinSessionRef is the canonical ref a pin's identity is spelled with: the
// name a client can address the session by ("local:<id>" for the controller's
// own, "<host>:<id>" for a remote source).
func (p pinSession) ref() hubapi.Ref {
	if p.source == "" {
		return hubapi.LocalRef(p.sessionID)
	}
	return hubapi.Ref{HostID: p.source, SessionID: p.sessionID}
}

type topLevelSessionResolver func(context.Context, string) (pinSession, error)

func registerPinSectionHandlers(server *appserver.Server, cfg hubcore.WebConfig, navigation *NavigationService, resolve topLevelSessionResolver) {
	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPinSectionRename, func(ctx context.Context, params appwire.PinSectionRenameParams) (appwire.PinSectionRenameResponse, error) {
		if cfg.PinSections == nil {
			return appwire.PinSectionRenameResponse{}, appwire.InternalError("pin section store not configured")
		}
		section, changed, err := cfg.PinSections.Rename(params.SectionID, params.Name, time.Now())
		if err != nil {
			return appwire.PinSectionRenameResponse{}, pinSectionAppWireError(err)
		}
		mutation, err := commitPinNavigation(ctx, cfg, navigation, changed)
		if err != nil {
			return appwire.PinSectionRenameResponse{}, err
		}
		return appwire.PinSectionRenameResponse{OK: true, Changed: changed, Section: pinSectionForAppWire(section), Navigation: mutation}, nil
	})

	appserver.HandleTyped(server.Router(), appwire.MethodEvenerPinSectionDelete, func(ctx context.Context, params appwire.PinSectionDeleteParams) (appwire.PinSectionDeleteResponse, error) {
		if cfg.PinSections == nil {
			return appwire.PinSectionDeleteResponse{}, appwire.InternalError("pin section store not configured")
		}
		memberCount, changed, err := cfg.PinSections.DeleteSection(params.SectionID)
		if err != nil {
			return appwire.PinSectionDeleteResponse{}, pinSectionAppWireError(err)
		}
		mutation, err := commitPinNavigation(ctx, cfg, navigation, changed)
		if err != nil {
			return appwire.PinSectionDeleteResponse{}, err
		}
		return appwire.PinSectionDeleteResponse{OK: true, Changed: changed, MemberCount: memberCount, Navigation: mutation}, nil
	})

	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSessionPinAssign, func(ctx context.Context, params appwire.SessionPinAssignParams) (appwire.SessionPinAssignResponse, error) {
		if (params.SectionID == nil) == (params.SectionName == nil) {
			return appwire.SessionPinAssignResponse{}, appwire.InvalidParams("exactly one of sectionId or sectionName is required")
		}
		if cfg.PinSections == nil {
			return appwire.SessionPinAssignResponse{}, appwire.InternalError("pin section store not configured")
		}
		session, err := resolvePinSession(ctx, resolve, params.SessionRef, "sessionRef")
		if err != nil {
			return appwire.SessionPinAssignResponse{}, err
		}

		var section hubcore.PinSection
		var changed bool
		if params.SectionID != nil {
			section, changed, err = cfg.PinSections.Assign(*params.SectionID, session.source, session.sessionID, time.Now())
		} else {
			section, changed, err = cfg.PinSections.CreateOrReuseAndAssign(*params.SectionName, session.source, session.sessionID, time.Now())
		}
		if err != nil {
			return appwire.SessionPinAssignResponse{}, pinSectionAppWireError(err)
		}
		mutation, err := commitPinNavigation(ctx, cfg, navigation, changed)
		if err != nil {
			return appwire.SessionPinAssignResponse{}, err
		}
		return appwire.SessionPinAssignResponse{
			OK: true, Changed: changed, Navigation: mutation,
			Assignment: appwire.SessionPinAssignment{SessionRef: session.ref().String(), Section: pinSectionForAppWire(section)},
		}, nil
	})

	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSessionPinUnpin, func(ctx context.Context, params appwire.SessionPinUnpinParams) (appwire.SessionPinUnpinResponse, error) {
		if cfg.PinSections == nil {
			return appwire.SessionPinUnpinResponse{}, appwire.InternalError("pin section store not configured")
		}
		session, err := resolvePinSession(ctx, resolve, params.SessionRef, "sessionRef")
		if err != nil {
			return appwire.SessionPinUnpinResponse{}, err
		}
		changed, err := cfg.PinSections.Unpin(session.source, session.sessionID)
		if err != nil {
			return appwire.SessionPinUnpinResponse{}, pinSectionAppWireError(err)
		}
		mutation, err := commitPinNavigation(ctx, cfg, navigation, changed)
		if err != nil {
			return appwire.SessionPinUnpinResponse{}, err
		}
		return appwire.SessionPinUnpinResponse{
			OK: true, Changed: changed, Navigation: mutation,
			Assignment: appwire.SessionPinUnpinAssignment{SessionRef: session.ref().String()},
		}, nil
	})
}

func resolvePinSession(ctx context.Context, resolve topLevelSessionResolver, requested, field string) (pinSession, error) {
	if resolve == nil {
		return pinSession{}, appwire.InternalError("top-level session resolver not configured")
	}
	session, err := resolve(ctx, requested)
	if err != nil {
		return pinSession{}, err
	}
	if session.sessionID == "" {
		return pinSession{}, appwire.InvalidParams(field + " must name a real top-level session")
	}
	return session, nil
}

func commitPinNavigation(ctx context.Context, cfg hubcore.WebConfig, navigation *NavigationService, changed bool) (appwire.NavigationMutation, error) {
	if navigation == nil {
		return appwire.NavigationMutation{}, appwire.Unavailable("navigation service not configured")
	}
	if !changed {
		return navigation.EmptyMutation(), nil
	}
	mutation, err := navigation.Refresh(ctx, navigationChangeHint{})
	if err != nil {
		return appwire.NavigationMutation{}, appwire.Unavailable(err.Error())
	}
	pokeMutationAttention(cfg)
	return mutation, nil
}

func pinSectionForAppWire(section hubcore.PinSection) appwire.PinSection {
	return appwire.PinSection{ID: section.ID, Name: section.Name, MemberCount: section.MemberCount}
}

func pinSectionAppWireError(err error) error {
	switch {
	case errors.Is(err, hubcore.ErrPinSectionName):
		return appwire.InvalidParams(err.Error())
	case errors.Is(err, hubcore.ErrPinSectionNotFound):
		return appwire.ResourceNotFound(err.Error())
	case errors.Is(err, hubcore.ErrPinSectionConflict):
		return appwire.Conflict(err.Error())
	default:
		return appwire.InternalError("pin section store error: " + err.Error())
	}
}

// resolveTopLevelSessionRef resolves a requested session ref to the
// source-qualified identity a pin is keyed by. A "host:<id>" ref names that
// host's session and a bare/"local:" ref the controller's own, so one host's
// ref can never address another source's row that shares its bare ID.
func (s *WebServer) resolveTopLevelSessionRef(ctx context.Context, requested string) (pinSession, error) {
	if strings.HasPrefix(requested, "cluster:") {
		return pinSession{}, appwire.InvalidParams("sessionRef must name a real top-level session")
	}
	metas, live, _ := s.navigationTreeInputs(ctx)
	ids := hubcore.TopLevelSessionIDs(metas)
	metaIDs := make(map[string]struct{}, len(metas))
	for _, meta := range metas {
		metaIDs[meta.ID] = struct{}{}
	}
	// A live session can be visible in the tree before its metadata reaches
	// PastIndex. Such a session is a top-level root by construction; sessions
	// with metadata are classified by the same helper as tree construction.
	for _, entry := range live {
		if entry.SessionID == "" {
			continue
		}
		if _, known := metaIDs[entry.SessionID]; !known {
			ids[entry.SessionID] = struct{}{}
		}
	}
	for id := range ids {
		if sessionRefMatchesID(requested, id) {
			return pinSessionFromTreeNodeID(id), nil
		}
	}
	// Nothing matched. A host-qualified ref that names a source the tree
	// carries rows for is a missing session on that source; a ref that names
	// no source at all is refused as an unknown source instead of being
	// resolved against the controller's own rows.
	if ref, err := hubapi.ParseRef(strings.TrimSpace(requested)); err == nil && hubcore.NormalizeDecisionSource(ref.HostID) != "" {
		for id := range ids {
			if hubRefFromTreeNodeID(id).HostID == ref.HostID {
				return pinSession{}, appwire.InvalidParams("sessionRef must name a real top-level session")
			}
		}
		return pinSession{}, appwire.InvalidParams("unknown source: " + ref.HostID)
	}
	return pinSession{}, appwire.InvalidParams("sessionRef must name a real top-level session")
}

// pinSessionFromTreeNodeID maps a tree node's identity (the bare ID of a
// controller session, or a remote row's host-qualified ref string) to the
// source-qualified pin identity.
func pinSessionFromTreeNodeID(id string) pinSession {
	ref := hubRefFromTreeNodeID(id)
	return pinSession{source: hubcore.NormalizeDecisionSource(ref.HostID), sessionID: ref.SessionID}
}

func sessionRefMatchesID(requested, actual string) bool {
	if requested == actual {
		return true
	}
	actualRef := hubRefFromTreeNodeID(actual)
	if requestedRef, err := hubapi.ParseRef(requested); err == nil && requestedRef == actualRef {
		return true
	}
	return actualRef.HostID == "local" && requested == actualRef.SessionID
}
