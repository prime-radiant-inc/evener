package hub

import (
	"context"
	"testing"
	"time"

	"primeradiant.com/evener/appwire"
	"primeradiant.com/evener/internal/e2ecap"
	"primeradiant.com/evener/test/e2e/fakellm"
)

// Only the external provider is scripted. Both watches pass through the real
// Manager runtime, reverse transport, shared subscription and durable journal.
func TestE2E_ManagerWatchReplacement(t *testing.T) {
	e2ecap.RequireLoopbackBind(t)
	e2ecap.RequireProcessInspect(t)
	if testing.Short() {
		t.Skip("live-stack e2e: builds binaries and runs hub + daemons")
	}
	provider, err := fakellm.New()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(provider.Close)
	stack := startHubStack(t, provider)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	human := stack.dialRPC(ctx, t)
	worker := startLiveThread(ctx, t, human, stack, "REPLACE-WORKER")
	workerCall, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	manager := startManagerThread(ctx, t, human, stack, "REPLACE-MANAGER")
	call, err := provider.Next(ctx.Done())
	if err != nil {
		t.Fatal(err)
	}
	var firstID, lastID string
	for i, hub := range []map[string]any{
		{"ref": worker, "events": []string{"question"}},
		{"ref": worker, "events": []string{"question"}},
		{"ref": worker, "completion": true},
	} {
		call.RespondToolCall("job_watch", map[string]any{"operation": "create", "hub": hub})
		call, err = provider.Next(ctx.Done())
		if err != nil {
			t.Fatal(err)
		}
		watches, err := clientRequest[appwire.SessionWatchesResponse](ctx, human, appwire.MethodEvenerThreadWatchesList, appwire.SessionActivityListParams{Ref: manager})
		if err != nil {
			t.Fatal(err)
		}
		active := 0
		for _, row := range watches.Watches {
			if row.State == appwire.SessionWatchStateArmed {
				active++
				lastID = row.Watch.ID
			}
		}
		if active != 1 {
			t.Fatalf("step %d active=%d watches=%+v", i, active, watches.Watches)
		}
		if i == 0 {
			firstID = lastID
		}
		if (lastID != firstID) != (i == 2) {
			t.Fatalf("step %d active=%s first=%s", i, lastID, firstID)
		}
	}
	call.RespondToolCall("communicate", communicateArgs("REPLACE-MANAGER-WAIT"))
	workerCall.RespondToolCall("communicate", communicateArgs("REPLACE-WORKER-DONE"))
	call, err = provider.Next(ctx.Done())
	if err != nil || !call.Contains("REPLACE-MANAGER") {
		t.Fatalf("completion wake: %v %v", call, err)
	}
	watches, err := clientRequest[appwire.SessionWatchesResponse](ctx, human, appwire.MethodEvenerThreadWatchesList, appwire.SessionActivityListParams{Ref: manager})
	if err != nil {
		t.Fatal(err)
	}
	if !acceptanceWatchCompleted(watches, lastID) {
		t.Fatalf("replacement did not complete: %+v", watches.Watches)
	}
	for _, row := range watches.Watches {
		if row.Watch.ID == firstID && (row.State != appwire.SessionWatchStateEnded || row.Watch.EndReason != "replaced") {
			t.Fatalf("old watch not durably replaced: %+v", row)
		}
	}
	call.RespondToolCall("communicate", communicateArgs("REPLACE-OBSERVED"))
}
