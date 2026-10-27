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

func queueTexts(sess *agent.Session) []string {
	queue, _ := sess.ClientMutationProjection()
	return queue.Texts
}
