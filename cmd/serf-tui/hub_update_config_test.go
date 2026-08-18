package main

import (
	"errors"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"primeradiant.com/evener/cmd/serf-tui/internal/launchconfig"
)

// TestConfigResultHandlersSurfaceErrorsAndClearThemOnSuccess pins the contract
// every config result handler shares: a result carrying Err puts that error on
// the model and returns WITHOUT refreshing any panel, and a result without Err
// clears a previously displayed error.
//
// The no-refresh half is the one that matters. Each success path feeds the
// message's List into a panel; taking that path on a failed mutation would
// repaint the panel from a list the mutation never produced — typically the
// zero value — so a failed remove would render as a successful one. Handling
// them as a table also keeps the shared shape from drifting apart one handler
// at a time.
func TestConfigResultHandlersSurfaceErrorsAndClearThemOnSuccess(t *testing.T) {
	boom := errors.New("mutation refused by the hub")

	for _, tc := range []struct {
		name string
		call func(m hubModel, err error) (tea.Model, tea.Cmd)
	}{
		{"instance mutate", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handleInstanceMutateResult(launchconfig.InstanceMutateResultMsg{Err: err})
		}},
		{"marketplace mutate", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handleMarketplaceMutateResult(launchconfig.MarketplaceMutateResultMsg{Err: err})
		}},
		{"plugin mutate", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handlePluginMutateResult(launchconfig.PluginMutateResultMsg{Err: err})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := tc.call(hubModel{}, boom)
			failed, ok := got.(hubModel)
			if !ok {
				t.Fatalf("handler returned %T, want hubModel", got)
			}
			if !errors.Is(failed.err, boom) {
				t.Fatalf("after a failed result, model err = %v, want %v", failed.err, boom)
			}

			// A prior error must not outlive the success that resolves it, or the
			// panel shows fresh data under a stale failure message.
			got, _ = tc.call(hubModel{err: boom}, nil)
			succeeded, ok := got.(hubModel)
			if !ok {
				t.Fatalf("handler returned %T, want hubModel", got)
			}
			if succeeded.err != nil {
				t.Fatalf("after a successful result, model err = %v, want nil", succeeded.err)
			}
		})
	}
}

// TestConfigListHandlersLeaveTheErrorForThePanelToRender pins the other half of
// the split, which is deliberate and easy to "fix" into inconsistency.
//
// A list handler forwards the message ITSELF to the panel, Err and all, so the
// panel renders the failure in place. A mutate handler cannot do that: on
// success it synthesizes a different message to refresh from, so it has to
// intercept Err on the model instead. Copying the mutate handlers' `m.err =
// msg.Err` into a list handler would surface the same failure twice, once on
// the panel and once as a model-level error.
func TestConfigListHandlersLeaveTheErrorForThePanelToRender(t *testing.T) {
	boom := errors.New("list refused by the hub")

	for _, tc := range []struct {
		name string
		call func(m hubModel, err error) (tea.Model, tea.Cmd)
	}{
		{"instance list", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handleInstanceList(launchconfig.InstanceListResultMsg{Err: err})
		}},
		{"marketplace list", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handleMarketplaceListResult(launchconfig.MarketplaceListResultMsg{Err: err})
		}},
		{"marketplace browse", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handleMarketplaceBrowseResult(launchconfig.MarketplaceBrowseResultMsg{Err: err})
		}},
		{"plugin list", func(m hubModel, err error) (tea.Model, tea.Cmd) {
			return m.handlePluginListResult(launchconfig.PluginListResultMsg{Err: err})
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, _ := tc.call(hubModel{}, boom)
			after, ok := got.(hubModel)
			if !ok {
				t.Fatalf("handler returned %T, want hubModel", got)
			}
			if after.err != nil {
				t.Fatalf("list handler set model err = %v; the panel owns rendering this failure", after.err)
			}
		})
	}
}
