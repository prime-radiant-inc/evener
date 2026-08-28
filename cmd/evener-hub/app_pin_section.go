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

type topLevelSessionResolver func(context.Context, string) (string, bool)

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
			return appwire.SessionPinAssignResponse{}, appwire.InvalidParams("exactly one of section_id or section_name is required")
		}
		if cfg.PinSections == nil {
			return appwire.SessionPinAssignResponse{}, appwire.InternalError("pin section store not configured")
		}
		sessionID, err := resolvePinSession(ctx, resolve, params.SessionRef, "session_ref")
		if err != nil {
			return appwire.SessionPinAssignResponse{}, err
		}

		var section hubcore.PinSection
		var changed bool
		if params.SectionID != nil {
			section, changed, err = cfg.PinSections.Assign(*params.SectionID, sessionID, time.Now())
		} else {
			section, changed, err = cfg.PinSections.CreateOrReuseAndAssign(*params.SectionName, sessionID, time.Now())
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
			Assignment: appwire.SessionPinAssignment{SessionRef: hubRefFromTreeNodeID(sessionID).String(), Section: pinSectionForAppWire(section)},
		}, nil
	})

	appserver.HandleTyped(server.Router(), appwire.MethodEvenerSessionPinUnpin, func(ctx context.Context, params appwire.SessionPinUnpinParams) (appwire.SessionPinUnpinResponse, error) {
		if cfg.PinSections == nil {
			return appwire.SessionPinUnpinResponse{}, appwire.InternalError("pin section store not configured")
		}
		sessionID, err := resolvePinSession(ctx, resolve, params.SessionRef, "session_ref")
		if err != nil {
			return appwire.SessionPinUnpinResponse{}, err
		}
		changed, err := cfg.PinSections.Unpin(sessionID)
		if err != nil {
			return appwire.SessionPinUnpinResponse{}, pinSectionAppWireError(err)
		}
		mutation, err := commitPinNavigation(ctx, cfg, navigation, changed)
		if err != nil {
			return appwire.SessionPinUnpinResponse{}, err
		}
		return appwire.SessionPinUnpinResponse{
			OK: true, Changed: changed, Navigation: mutation,
			Assignment: appwire.SessionPinUnpinAssignment{SessionRef: hubRefFromTreeNodeID(sessionID).String()},
		}, nil
	})
}

func resolvePinSession(ctx context.Context, resolve topLevelSessionResolver, requested, field string) (string, error) {
	if resolve == nil {
		return "", appwire.InternalError("top-level session resolver not configured")
	}
	sessionID, ok := resolve(ctx, requested)
	if !ok {
		return "", appwire.InvalidParams(field + " must name a real top-level session")
	}
	return sessionID, nil
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

func (s *WebServer) resolveTopLevelSessionRef(ctx context.Context, requested string) (string, bool) {
	if strings.HasPrefix(requested, "cluster:") {
		return "", false
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
			return id, true
		}
	}
	return "", false
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
