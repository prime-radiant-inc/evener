package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/appserver"
)

const navigationInvalidParamsMessage = "invalid navigation params"

func registerNavigationReadHandler(server *appserver.Server, navigation *NavigationService) {
	server.Router().Handle(appwire.MethodEvenerNavigationRead, func(ctx context.Context, raw json.RawMessage) (any, error) {
		params, fields, err := decodeNavigationReadParams(raw)
		if err != nil {
			return nil, appwire.InvalidParams(navigationInvalidParamsMessage)
		}
		return navigationReadWithFields(ctx, server, navigation, params, fields)
	})
}

func navigationReadWithFields(ctx context.Context, server *appserver.Server, navigation *NavigationService, params appwire.NavigationReadParams, fields map[string]json.RawMessage) (appwire.NavigationReadResponse, error) {
	key, err := navigationReadKeyWithFields(params, fields)
	if err != nil {
		return appwire.NavigationReadResponse{}, appwire.InvalidParams(navigationInvalidParamsMessage)
	}
	if navigation == nil {
		return appwire.NavigationReadResponse{}, appwire.Unavailable("navigation unavailable")
	}
	if err := ctx.Err(); err != nil {
		return appwire.NavigationReadResponse{}, navigationReadError(server, err)
	}
	if params.RepresentationVersion != 2 {
		return appwire.NavigationReadResponse{}, appwire.InvalidParams(navigationInvalidParamsMessage)
	}
	result, err := navigation.readV2(ctx, key, params.Base)
	if err != nil {
		return appwire.NavigationReadResponse{}, navigationReadError(server, err)
	}
	return result.Response, nil
}

func navigationReadKey(params appwire.NavigationReadParams) (navigationResourceKey, error) {
	return navigationReadKeyWithFields(params, nil)
}

func navigationReadKeyWithFields(params appwire.NavigationReadParams, fields map[string]json.RawMessage) (navigationResourceKey, error) {
	switch params.Resource {
	case "manifest":
		if err := rejectNavigationReadFields(params, fields); err != nil {
			return navigationResourceKey{}, err
		}
		return navigationResourceKey{Kind: navigationResourceManifest}, nil
	case "section":
		if err := rejectNavigationReadFields(params, fields, "section"); err != nil {
			return navigationResourceKey{}, err
		}
		var kind navigationResourceKind
		switch params.Section {
		case "live":
			kind = navigationResourceLive
		case "needs_you":
			kind = navigationResourceNeedsYou
		default:
			return navigationResourceKey{}, fmt.Errorf("invalid section %q", params.Section)
		}
		offset, limit, err := navigationReadPage(params, maxNavigationSectionRows)
		if err != nil {
			return navigationResourceKey{}, err
		}
		return navigationResourceKey{Kind: kind, Offset: offset, Limit: limit}, nil
	case "pin_catalog":
		if err := rejectNavigationReadFields(params, fields); err != nil {
			return navigationResourceKey{}, err
		}
		offset, limit, err := navigationReadPage(params, maxNavigationCatalogRows)
		if err != nil {
			return navigationResourceKey{}, err
		}
		return navigationResourceKey{Kind: navigationResourcePinCatalog, Offset: offset, Limit: limit}, nil
	case "pin_section":
		if err := rejectNavigationReadFields(params, fields, "sectionId"); err != nil {
			return navigationResourceKey{}, err
		}
		if err := validateNavigationIdentity("pin section ID", params.SectionID, false); err != nil {
			return navigationResourceKey{}, err
		}
		offset, limit, err := navigationReadPage(params, maxNavigationSectionRows)
		if err != nil {
			return navigationResourceKey{}, err
		}
		return navigationResourceKey{Kind: navigationResourcePinSection, SectionID: params.SectionID, Offset: offset, Limit: limit}, nil
	case "catalog":
		if err := rejectNavigationReadFields(params, fields, "catalog"); err != nil {
			return navigationResourceKey{}, err
		}
		var kind navigationResourceKind
		switch params.Catalog {
		case "projects":
			kind = navigationResourceProjects
		case "archived_projects":
			kind = navigationResourceArchivedProjects
		case "test_runs":
			kind = navigationResourceTestRuns
		default:
			return navigationResourceKey{}, fmt.Errorf("invalid catalog %q", params.Catalog)
		}
		offset, limit, err := navigationReadPage(params, maxNavigationCatalogRows)
		if err != nil {
			return navigationResourceKey{}, err
		}
		return navigationResourceKey{Kind: kind, Offset: offset, Limit: limit}, nil
	case "project":
		if err := rejectNavigationReadFields(params, fields, "projectKey"); err != nil {
			return navigationResourceKey{}, err
		}
		if err := validateNavigationIdentity("project key", params.ProjectKey, false); err != nil {
			return navigationResourceKey{}, err
		}
		return navigationResourceKey{Kind: navigationResourceProject, ProjectKey: params.ProjectKey}, nil
	case "project_page":
		if err := rejectNavigationReadFields(params, fields, "projectKey", "tier"); err != nil {
			return navigationResourceKey{}, err
		}
		if err := validateNavigationIdentity("project key", params.ProjectKey, false); err != nil {
			return navigationResourceKey{}, err
		}
		if params.Tier != "current" && params.Tier != "recent" && params.Tier != "archived" {
			return navigationResourceKey{}, fmt.Errorf("invalid project tier %q", params.Tier)
		}
		offset, limit, err := navigationReadPage(params, maxNavigationSectionRows)
		if err != nil {
			return navigationResourceKey{}, err
		}
		return navigationResourceKey{Kind: navigationResourceProjectPage, ProjectKey: params.ProjectKey, Tier: params.Tier, Offset: offset, Limit: limit}, nil
	case "location":
		if err := rejectNavigationReadFields(params, fields, "ref"); err != nil {
			return navigationResourceKey{}, err
		}
		ref, err := navigationRef(params.Ref)
		if err != nil {
			return navigationResourceKey{}, err
		}
		return navigationResourceKey{Kind: navigationResourceLocation, ID: ref.String()}, nil
	default:
		return navigationResourceKey{}, fmt.Errorf("unknown navigation resource %q", params.Resource)
	}
}

var navigationReadParamNames = map[string]struct{}{
	"resource":              {},
	"section":               {},
	"sectionId":             {},
	"catalog":               {},
	"projectKey":            {},
	"tier":                  {},
	"ref":                   {},
	"offset":                {},
	"limit":                 {},
	"etag":                  {},
	"representationVersion": {},
	"base":                  {},
}

func decodeNavigationReadParams(raw json.RawMessage) (appwire.NavigationReadParams, map[string]json.RawMessage, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return appwire.NavigationReadParams{}, nil, errors.New("params object required")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return appwire.NavigationReadParams{}, nil, err
	}
	if fields == nil {
		return appwire.NavigationReadParams{}, nil, errors.New("params object required")
	}
	for name, value := range fields {
		if _, ok := navigationReadParamNames[name]; !ok {
			return appwire.NavigationReadParams{}, nil, fmt.Errorf("unknown field %q", name)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return appwire.NavigationReadParams{}, nil, fmt.Errorf("field %q must not be null", name)
		}
	}
	var params appwire.NavigationReadParams
	if err := json.Unmarshal(raw, &params); err != nil {
		return appwire.NavigationReadParams{}, nil, err
	}
	return params, fields, nil
}

func rejectNavigationReadFields(params appwire.NavigationReadParams, fields map[string]json.RawMessage, allowed ...string) error {
	values := []struct {
		name  string
		value string
	}{
		{name: "section", value: params.Section},
		{name: "sectionId", value: params.SectionID},
		{name: "catalog", value: params.Catalog},
		{name: "projectKey", value: params.ProjectKey},
		{name: "tier", value: params.Tier},
		{name: "ref", value: params.Ref},
	}
	for _, field := range values {
		fieldPresent := field.value != ""
		if fields != nil {
			_, fieldPresent = fields[field.name]
		}
		if fieldPresent && !navigationReadFieldAllowed(field.name, allowed) {
			return fmt.Errorf("field %q is not valid for resource %q", field.name, params.Resource)
		}
	}
	if !navigationReadResourceIsPaged(params.Resource) && (params.Offset != nil || params.Limit != nil) {
		return fmt.Errorf("page fields are not valid for resource %q", params.Resource)
	}
	return nil
}

func navigationReadFieldAllowed(name string, allowed []string) bool {
	return slices.Contains(allowed, name)
}

func navigationReadResourceIsPaged(resource string) bool {
	switch resource {
	case "section", "pin_catalog", "pin_section", "catalog", "project_page":
		return true
	default:
		return false
	}
}

func navigationReadPage(params appwire.NavigationReadParams, maximum uint32) (uint32, uint32, error) {
	offset := uint32(0)
	if params.Offset != nil {
		offset = *params.Offset
	}
	limit := maximum
	if params.Limit != nil {
		if *params.Limit == 0 {
			return 0, 0, errors.New("limit must be greater than zero")
		}
		if *params.Limit > maximum {
			return 0, 0, fmt.Errorf("limit exceeds maximum of %d", maximum)
		}
		limit = *params.Limit
	}
	return offset, limit, nil
}

func navigationReadError(server *appserver.Server, err error) error {
	var unavailable navigationAvailabilityError
	var notFound navigationNotFoundError
	if errors.As(err, &unavailable) || errors.As(err, &notFound) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return appwire.Unavailable("navigation unavailable")
	}
	server.Logf("navigation read failed: %v", err)
	return appwire.InternalError("navigation read failed")
}
