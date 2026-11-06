package server

import (
	"fmt"
	"sync/atomic"

	"primeradiant.com/evener/agent"
	"primeradiant.com/evener/appwire"
)

var queuedTestMutationSeq atomic.Uint64

// enqueueText queues one text message through the same durable entry a client's
// turn/queue request takes.
func enqueueText(sess *agent.Session, text string) error {
	_, err := sess.AcceptClientMutationQueue(appwire.TurnQueueParams{
		Ref:              sess.ID(),
		ClientMutationID: fmt.Sprintf("test-queue-%d", queuedTestMutationSeq.Add(1)),
		Input:            []appwire.InputItem{{Type: "text", Text: text}},
	})
	return err
}

// queuePreview, queueIDs and queueTexts read the durable queue the way a client
// sees it.
func queuePreview(sess *agent.Session) []string {
	queue, _ := sess.ClientMutationProjection()
	return queue.Preview
}

func queueIDs(sess *agent.Session) []string {
	queue, _ := sess.ClientMutationProjection()
	return queue.IDs
}

// steeringInputs reads the steering a client's steer, drain or promote (or a
// human note) has durably accepted and the active turn has not yet taken: one
// input list per accepted mutation.
func steeringInputs(sess *agent.Session) [][]appwire.InputItem {
	_, pending := sess.ClientMutationProjection()
	var out [][]appwire.InputItem
	for _, mutation := range pending {
		switch mutation.Method {
		case appwire.MethodTurnSteer, appwire.MethodTurnDrainAsSteer, appwire.MethodTurnPromoteQueuedAsSteer, appwire.MethodNotesHumanSet:
			if mutation.ExecutionState == "accepted" {
				out = append(out, mutation.Input)
			}
		}
	}
	return out
}

// durablyAcceptedMutations counts the mutations the session's durable store
// holds as pending: queued inputs, steering and active starts alike.
func durablyAcceptedMutations(sess *agent.Session) int {
	_, pending := sess.ClientMutationProjection()
	return len(pending)
}

func queueTexts(sess *agent.Session) []string {
	queue, _ := sess.ClientMutationProjection()
	return queue.Texts
}
