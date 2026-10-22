package appsource

import (
	"context"
	"errors"
	"fmt"

	"primeradiant.com/evener/appwire"
)

// SetManager forwards the human-only fixed setting to the hosting hub, not to
// a Manager tool or the general host/request allowlist. The host derives the
// enabling authority from its completed identity exchange on this connection;
// neither the ref nor the human parameters supply a HubID.
func (s *RemoteHubSource) SetManager(ctx context.Context, params appwire.ThreadManagerSetParams) (appwire.ThreadManagerSetResponse, error) {
	var out appwire.ThreadManagerSetResponse
	ref, err := s.toRemoteRef(params.Ref, "")
	if err != nil {
		return out, err
	}
	params.Ref = ref.String()
	if err := s.call(ctx, appwire.MethodEvenerThreadManagerSet, params, &out); err != nil {
		return out, err
	}
	out.View.Ref, err = s.fromRemoteRefString(out.View.Ref)
	if err != nil {
		return out, err
	}
	if out.View.Setting.Enabled {
		if err := s.BindManager(ctx, out.View); err != nil {
			return out, err
		}
	} else {
		s.UnbindManager(out.View.Ref)
	}
	return out, nil
}

// RestoreManagerBindings runs once when the existing SSH bridge attaches or
// reconnects. List supplies candidates only: each live preference and instance
// is fetched from the hosting hub on the new connection before a binding is
// installed. The host rechecks that exact preference revision at bind time.
// This is not an observation subscription or a second recovery service.
func (s *RemoteHubSource) RestoreManagerBindings(ctx context.Context) error {
	client, err := s.resolveClient(ctx)
	if err != nil {
		return err
	}
	s.managerMu.Lock()
	getID := s.managerHubID
	old := make(map[string]struct{}, len(s.managerBindings))
	for ref := range s.managerBindings {
		old[ref] = struct{}{}
	}
	s.managerMu.Unlock()
	if getID == nil {
		return appwire.Unavailable("remote Manager transport unavailable")
	}
	hubID, err := getID()
	if err != nil {
		return err
	}
	listed, err := s.ListThreads(ctx, appwire.ThreadListParams{})
	if err != nil {
		return err
	}
	var failures []error
	for _, thread := range listed.Data {
		ref := thread.Evener.Ref
		if ref == "" {
			continue
		}
		delete(old, ref)
		read, readErr := s.ReadThread(ctx, appwire.ThreadReadParams{Ref: ref})
		if readErr != nil {
			failures = append(failures, fmt.Errorf("read %s: %w", ref, readErr))
			continue
		}
		if read.Thread.Evener.Ref != ref || read.Thread.Evener.InstanceID == "" || read.Thread.Evener.ManagerSetting == nil {
			continue
		}
		setting := *read.Thread.Evener.ManagerSetting
		if !setting.Enabled || setting.HubID != hubID {
			s.UnbindManager(ref)
			continue
		}
		current, currentErr := s.resolveClient(ctx)
		if currentErr != nil || current != client {
			return appwire.SessionUnavailable("remote Manager bridge changed during restoration")
		}
		view := appwire.ManagerView{Ref: ref, InstanceID: read.Thread.Evener.InstanceID, Setting: setting}
		if bindErr := s.BindManager(ctx, view); bindErr != nil {
			failures = append(failures, fmt.Errorf("bind %s: %w", ref, bindErr))
		}
	}
	for ref := range old {
		s.UnbindManager(ref)
		failures = append(failures, fmt.Errorf("previous Manager %s missing from new hosting inventory", ref))
	}
	return errors.Join(failures...)
}
