package server

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"

	"primeradiant.com/evener/appwire"
)

// Measure the whole Manager read, including the mandatory projected row and
// status follow-up. IDs cover fresh 24-character turns and client mutations,
// with call_ plus 24-character provider call IDs in a 4,000-composite set.
func TestPendingAskPageWholeResponseMeasurement(t *testing.T) {
	t.Parallel()
	largestCommon := 4000
	for _, turnID := range []string{"012345678901234567890123", "turn_m4000"} {
		t.Run(turnID, func(t *testing.T) {
			s := NewServer(ServerConfig{})
			s.SetAppIdentity("local", "012345678901234567890123")
			s.appBootGeneration = "123"
			state := &appwire.PendingAskState{Version: 1, InstanceID: "012345678901234567890123", BootGeneration: "123", Revision: 4000, CalllessCount: 2, CompositeTotal: 4000}
			for i := 0; i < 4000; i++ {
				state.Calls = append(state.Calls, appwire.PendingAskCall{TurnID: turnID, CallID: fmt.Sprintf("call_%024d", i)})
			}
			labels := make([]string, appwire.MaxQuestionOptions)
			for i := range labels {
				labels[i] = strings.Repeat("<", appwire.MaxQuestionOptionRunes)
			}
			question := appwire.BoundedPendingQuestion(strings.Repeat("<", appwire.MaxQuestionTextRunes), labels, 4002)
			complete, truncated := false, true
			question.TurnID = turnID
			question.CallID = state.Calls[0].CallID
			question.Complete = &complete
			question.Truncated = &truncated
			s.SetThreadEnvelopeSource(&pendingAskEnvelopeSource{stubThreadEnvelopeSource: stubThreadEnvelopeSource{question: &question}, sample: func() *appwire.PendingAskState { return state }})
			s.RefreshThreadEnvelope()
			huge := math.MaxInt
			read, err := s.handleAppThreadRead(context.Background(), appwire.ThreadReadParams{Ref: "local:" + state.InstanceID, ExpectedAskRevision: &state.Revision, ExpectedAskBootGeneration: "123", Limit: &huge})
			if err != nil {
				t.Fatal(err)
			}
			if read.PendingAskPage == nil || len(read.PendingAskPage.Composites) != appwire.MaxPendingAskPageLimit {
				t.Fatalf("huge limit was not normalized: %+v", read.PendingAskPage)
			}
			supported := true
			read.TargetSessionToolsSupported = &supported
			size := func(n int) int {
				pageRead := read
				pageRead.PendingAskPage = &appwire.PendingAskPage{Composites: state.Calls[:n], Total: 4000, CalllessCount: 2}
				if n < 4000 {
					next := n
					pageRead.PendingAskPage.NextOffset = &next
				}
				response := appwire.SessionOperationResponse{Actor: appwire.SessionActor{Kind: "manager", ManagerRef: "local:012345678901234567890123"}, Read: &pageRead, FollowUps: []appwire.SessionStatusFollowUp{{Tool: "job_status", Arguments: appwire.SessionStatusArguments{Hub: appwire.SessionStatusTarget{Ref: read.Thread.Evener.Ref}}}}}
				encoded, err := json.Marshal(response)
				if err != nil {
					t.Fatal(err)
				}
				return len(encoded)
			}
			low, high := 0, 4000
			for low < high {
				mid := (low + high + 1) / 2
				if size(mid) < 262144 {
					low = mid
				} else {
					high = mid - 1
				}
			}
			largestCommon = min(largestCommon, low)
			t.Logf("turnID=%s largest=%d max=%d wholeBytes=%d maxPlusOneBytes=%d", turnID, low, appwire.MaxPendingAskPageLimit, size(appwire.MaxPendingAskPageLimit), size(appwire.MaxPendingAskPageLimit+1))
			if size(appwire.MaxPendingAskPageLimit) >= 262144 {
				t.Fatal("normalized page exceeds whole Manager-result cap")
			}
			if len(read.Thread.Evener.PendingAsk.Calls) != 10 || !read.Thread.Evener.PendingAsk.CallsTruncated || read.Thread.Evener.PendingAsk.CompositeTotal != 4000 {
				t.Fatalf("row not clamped: %+v", read.Thread.Evener.PendingAsk)
			}
		})
	}
	if appwire.MaxPendingAskPageLimit != largestCommon {
		t.Fatalf("maximum=%d, largest measured common page=%d", appwire.MaxPendingAskPageLimit, largestCommon)
	}
}
