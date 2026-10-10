package agent

import (
	"errors"
	"reflect"
	"testing"

	"primeradiant.com/evener/agent/events"
	"primeradiant.com/evener/agent/transcript"
	"primeradiant.com/evener/appwire"
)

// These fixtures spell UTF-16 offsets independently of the queue serializers.
// The two /same atoms deliberately select different kinds in command-first order.
func mentionRecoveryInput() []appwire.InputItem {
	return []appwire.InputItem{
		{Type: "text", Text: "/same /same", Mentions: []appwire.InputMention{
			{Kind: "command", Name: "same", Offset: 0},
			{Kind: "skill", Name: "same", Offset: 6},
		}},
		{Type: "image", MediaType: "image/png", Data: []byte{1, 2, 3}, Name: "first.png"},
		{Type: "text", Text: " 😀 /tail  ", Mentions: []appwire.InputMention{{Kind: "skill", Name: "tail", Offset: 4}}},
		{Type: "skill", Name: "same"},
		{Type: "skill", Name: "tail"},
		{Type: "command", Name: "same"},
	}
}

func mentionRecoveryWant() queuedInput {
	return queuedInput{
		Text:       "/same /same 😀 /tail  ",
		Images:     []ImageAttachment{{MediaType: "image/png", Data: []byte{1, 2, 3}, Name: "first.png"}},
		SkillNames: []string{"same", "tail"}, CommandNames: []string{"same"},
		Mentions: []appwire.InputMention{
			{Kind: "command", Name: "same", Offset: 0},
			{Kind: "skill", Name: "same", Offset: 6},
			{Kind: "skill", Name: "tail", Offset: 15},
		},
	}
}

func assertMentionRecoveryQueue(t *testing.T, s *Session, ids, mutationIDs []string, want []queuedInput) {
	t.Helper()
	queue, pending := s.ClientMutationProjection()
	if queue.Depth != len(want) || !reflect.DeepEqual(queue.IDs, ids) || !reflect.DeepEqual(queue.ClientMutationIDs, mutationIDs) {
		t.Fatalf("queue identities = %+v, want IDs %v and mutations %v", queue, ids, mutationIDs)
	}
	if len(queue.Texts) != len(want) || len(queue.SkillNames) != len(want) || len(queue.CommandNames) != len(want) || len(queue.Mentions) != len(want) {
		t.Fatalf("queue arrays not aligned: %+v", queue)
	}
	s.mu.Lock()
	runtimeQueue := append([]queuedInput(nil), s.inputQueue...)
	s.mu.Unlock()
	if len(runtimeQueue) != len(want) {
		t.Fatalf("runtime queue depth = %d, want %d", len(runtimeQueue), len(want))
	}
	for i, expected := range want {
		if queue.Texts[i] != expected.Text || !reflect.DeepEqual(queue.SkillNames[i], expected.SkillNames) || !reflect.DeepEqual(queue.CommandNames[i], expected.CommandNames) {
			t.Errorf("queue content[%d] = %+v, want %+v", i, queue, expected)
		}
		if !reflect.DeepEqual(queue.Mentions[i], expected.Mentions) {
			t.Errorf("queue mentions[%d] = %+v, want %+v", i, queue.Mentions[i], expected.Mentions)
		}
		got := runtimeQueue[i]
		if got.ID != ids[i] || got.Text != expected.Text || !reflect.DeepEqual(got.Images, expected.Images) || !reflect.DeepEqual(got.SkillNames, expected.SkillNames) || !reflect.DeepEqual(got.CommandNames, expected.CommandNames) || !reflect.DeepEqual(got.Mentions, expected.Mentions) {
			t.Errorf("runtime queue[%d] = %+v, want %+v", i, got, expected)
		}
		var found bool
		for _, mutation := range pending {
			if mutation.ClientMutationID == mutationIDs[i] {
				found = true
				if mutation.ExecutionState != "accepted" || mutation.ProjectionState != appwire.MutationProjectionPending || !reflect.DeepEqual(mutation.QueueEntryIDs, []string{ids[i]}) {
					t.Errorf("queue pending mutation = %+v", mutation)
				}
				// Read the authoritative pending payload, not a copy from the runtime queue.
				projected := queuedInputFromClientMutation(clientMutationQueueEntry{Input: mutation.Input})
				if projected.Text != expected.Text || !reflect.DeepEqual(projected.Images, expected.Images) || !reflect.DeepEqual(projected.SkillNames, expected.SkillNames) || !reflect.DeepEqual(projected.CommandNames, expected.CommandNames) || !reflect.DeepEqual(projected.Mentions, expected.Mentions) {
					t.Errorf("pending queue input[%d] = %+v, want %+v", i, projected, expected)
				}
			}
		}
		if !found {
			t.Errorf("queue mutation %q absent from authoritative pending projection", mutationIDs[i])
		}
	}
}

func assertMentionRecoveryQueueEvent(t *testing.T, captured []events.SessionEvent, want []queuedInput) {
	t.Helper()
	var latest *events.QueueChangedData
	for _, event := range captured {
		switch event.Kind {
		case events.EventQueueChanged:
			data, ok := event.Data.(events.QueueChangedData)
			if !ok {
				t.Fatalf("queue event payload = %T", event.Data)
			}
			latest = &data
		case events.EventError, events.EventWarning:
			t.Errorf("unexpected diagnostic: kind=%s data=%+v", event.Kind, event.Data)
		}
	}
	if latest == nil || latest.Depth != len(want) || len(latest.Mentions) != len(want) {
		t.Fatalf("latest queue event = %+v, want depth %d", latest, len(want))
	}
	for i, entry := range want {
		if latest.Texts[i] != entry.Text || !reflect.DeepEqual(latest.SkillNames[i], entry.SkillNames) || !reflect.DeepEqual(latest.CommandNames[i], entry.CommandNames) || !reflect.DeepEqual(latest.Mentions[i], entry.Mentions) {
			t.Errorf("queue event[%d] = %+v, want %+v", i, latest, entry)
		}
	}
}

func TestClientMutationMentionsClaimReturn(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newQueuePersistTestSession(t, dir)
	captured, stop := captureEvents(s)
	defer stop()
	id := s.ID()
	params := appwire.TurnQueueParams{ClientMutationID: "mentions-return", Input: mentionRecoveryInput()}
	accepted, err := s.AcceptClientMutationQueue(params)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "mentions-fifo", Input: []appwire.InputItem{{Type: "text", Text: "FIFO_SENTINEL_318"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ids := []string{accepted.Receipt.QueueEntryIDs[0], second.Receipt.QueueEntryIDs[0]}
	mutations := []string{"mentions-return", "mentions-fifo"}
	want := []queuedInput{mentionRecoveryWant(), {Text: "FIFO_SENTINEL_318"}}
	assertMentionRecoveryQueue(t, s, ids, mutations, want)
	claimed, err := s.popQueueHeadRefusingPoison()
	if err != nil || claimed.ClientMutationID != params.ClientMutationID {
		t.Fatalf("claim = %+v, err = %v", claimed, err)
	}
	if !reflect.DeepEqual(claimed.Mentions, want[0].Mentions) {
		t.Fatalf("claim mentions = %+v, want %+v", claimed.Mentions, want[0].Mentions)
	}
	// Exercise the queue's explicit push-back, not completion's raw-payload
	// return. The latter already preserves mentions and is guarded separately.
	if err := s.pushQueueHead(claimed); err != nil {
		t.Fatal(err)
	}
	assertMentionRecoveryQueue(t, s, ids, mutations, want)
	snapshot := s.clientMutations.snapshot()
	if snapshot.QueueRevision != 4 || snapshot.AcceptedTurns != 0 || snapshot.Journal[params.ClientMutationID].StableTurnID != claimed.StableTurnID {
		t.Fatalf("claim return changed revision, budget or identity: %+v", snapshot)
	}
	stop()
	assertMentionRecoveryQueueEvent(t, *captured, want)
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	assertMentionRecoveryQueue(t, restored, ids, mutations, want)
	replayed, err := restored.AcceptClientMutationQueue(params)
	if err != nil || replayed.Receipt.Disposition != appwire.MutationDispositionReplayed {
		t.Fatalf("queue replay = %+v, err = %v", replayed, err)
	}
	assertMentionRecoveryQueue(t, restored, ids, mutations, want)
}

func TestClientMutationMentionsClaimRefusal(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newQueuePersistTestSession(t, dir)
	captured, stop := captureEvents(s)
	defer stop()
	id := s.ID()
	accepted, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{
		ClientMutationID: "mentions-refusal", Input: mentionRecoveryInput(),
	})
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := s.popQueueHeadRefusingPoison()
	if err != nil || claimed.ClientMutationID != "mentions-refusal" {
		t.Fatalf("claim = %+v, err = %v", claimed, err)
	}
	// Refuse the real user-input executor after a durable claim. Its failure
	// boundary returns the original payload through completeClientMutationTurn.
	if err := s.attachedTranscript().Close(); err != nil {
		t.Fatal(err)
	}
	_, err = s.ProcessInputKind(withQueuedClientMutation(t.Context(), claimed), claimed.Text, claimed.Images, EntryUserInput)
	if !errors.Is(err, transcript.ErrWriterClosed) || errors.Is(err, transcript.ErrWriterPoisoned) {
		t.Fatalf("claimed execution error = %T %v, want closed-writer refusal", err, err)
	}
	want := []queuedInput{mentionRecoveryWant()}
	assertMentionRecoveryQueue(t, s, accepted.Receipt.QueueEntryIDs, []string{"mentions-refusal"}, want)
	stop()
	assertMentionRecoveryQueueEvent(t, *captured, want)
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	assertMentionRecoveryQueue(t, restored, accepted.Receipt.QueueEntryIDs, []string{"mentions-refusal"}, want)
}

func assertMentionRecoverySteering(t *testing.T, s *Session, mutationID, method, turnID string, input []appwire.InputItem, want SteeringEntry) {
	t.Helper()
	queue, pending := s.ClientMutationProjection()
	if queue.Depth != 0 || len(pending) != 1 {
		t.Fatalf("drained/promoted projection: queue=%+v pending=%+v", queue, pending)
	}
	mutation := pending[0]
	if mutation.ClientMutationID != mutationID || mutation.Method != method || mutation.TurnID != turnID || mutation.ExecutionState != "accepted" || mutation.ProjectionState != appwire.MutationProjectionPending {
		t.Errorf("pending steering identity = %+v", mutation)
	}
	if !reflect.DeepEqual(mutation.Input, input) {
		t.Errorf("authoritative steering input = %+v, want %+v", mutation.Input, input)
	}
	if got := s.SteeringQueueSnapshot(); !reflect.DeepEqual(got, []SteeringEntry{want}) {
		t.Errorf("runtime steering = %+v, want %+v", got, want)
	}
}

func TestClientMutationMentionsDrain(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newQueuePersistTestSession(t, dir)
	captured, stop := captureEvents(s)
	defer stop()
	id := s.ID()
	inputs := [][]appwire.InputItem{
		mentionRecoveryInput(),
		// Normalization removes whitespace, but its image still travels FIFO.
		{{Type: "text", Text: " \t\n "}, {Type: "image", MediaType: "image/jpeg", Data: []byte{4}, Name: "blank.jpg"}},
		{
			{Type: "text", Text: "B😀 "},
			{Type: "text", Text: "/same /same ", Mentions: []appwire.InputMention{
				{Kind: "skill", Name: "same", Offset: 0}, {Kind: "command", Name: "same", Offset: 6},
			}},
			{Type: "skill", Name: "same"}, {Type: "command", Name: "same"},
		},
	}
	for i, mutationID := range []string{"mentions-drain-first", "mentions-drain-image", "mentions-drain-second"} {
		if _, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: mutationID, Input: inputs[i]}); err != nil {
			t.Fatal(err)
		}
	}
	params := appwire.TurnDrainAsSteerParams{
		ClientMutationID: "mentions-drain", ExpectedQueueRevision: 3,
		Input: []appwire.InputItem{
			{Type: "text", Text: " 🐈 "},
			{Type: "text", Text: "/extra ", Mentions: []appwire.InputMention{{Kind: "command", Name: "extra", Offset: 0}}},
			{Type: "image", MediaType: "image/png", Data: []byte{5, 6}, Name: "extra.png"},
			{Type: "command", Name: "extra"},
		},
	}
	accepted, err := s.AcceptClientMutationDrainAsSteer(params)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(accepted.Receipt.ConsumedClientMutationIDs, []string{"mentions-drain-first", "mentions-drain-image", "mentions-drain-second"}) {
		t.Fatalf("drain consumed = %+v", accepted.Receipt)
	}
	const text = "/same /same 😀 /tail  \n\nB😀 /same /same \n\n 🐈 /extra "
	// The first entry is 22 UTF-16 units, the second text entry is 16.
	// An image-only entry adds no separator. Extra input starts at 42.
	wantInput := []appwire.InputItem{
		{Type: "text", Text: text, Mentions: []appwire.InputMention{
			{Kind: "command", Name: "same", Offset: 0}, {Kind: "skill", Name: "same", Offset: 6},
			{Kind: "skill", Name: "tail", Offset: 15}, {Kind: "skill", Name: "same", Offset: 28},
			{Kind: "command", Name: "same", Offset: 34}, {Kind: "command", Name: "extra", Offset: 46},
		}},
		{Type: "image", MediaType: "image/png", Data: []byte{1, 2, 3}, Name: "first.png"},
		{Type: "image", MediaType: "image/jpeg", Data: []byte{4}, Name: "blank.jpg"},
		{Type: "image", MediaType: "image/png", Data: []byte{5, 6}, Name: "extra.png"},
		{Type: "skill", Name: "same"}, {Type: "skill", Name: "tail"}, {Type: "skill", Name: "same"},
		{Type: "command", Name: "same"}, {Type: "command", Name: "same"}, {Type: "command", Name: "extra"},
	}
	wantSteering := SteeringEntry{
		Text: text,
		Images: []ImageAttachment{
			{MediaType: "image/png", Data: []byte{1, 2, 3}, Name: "first.png"},
			{MediaType: "image/jpeg", Data: []byte{4}, Name: "blank.jpg"},
			{MediaType: "image/png", Data: []byte{5, 6}, Name: "extra.png"},
		},
		SkillNames: []string{"same", "tail", "same"}, CommandNames: []string{"same", "same", "extra"},
	}
	assertMentionRecoverySteering(t, s, params.ClientMutationID, clientMutationMethodDrain, accepted.Receipt.TurnID, wantInput, wantSteering)
	stop()
	assertMentionRecoveryQueueEvent(t, *captured, nil)
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	assertMentionRecoverySteering(t, restored, params.ClientMutationID, clientMutationMethodDrain, accepted.Receipt.TurnID, wantInput, wantSteering)
	replayed, err := restored.AcceptClientMutationDrainAsSteer(params)
	if err != nil || replayed.Receipt.Disposition != appwire.MutationDispositionReplayed || replayed.Receipt.TurnID != accepted.Receipt.TurnID {
		t.Fatalf("drain replay = %+v, err = %v", replayed, err)
	}
	assertMentionRecoverySteering(t, restored, params.ClientMutationID, clientMutationMethodDrain, accepted.Receipt.TurnID, wantInput, wantSteering)
}

func TestClientMutationMentionsDirectPromote(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newQueuePersistTestSession(t, dir)
	captured, stop := captureEvents(s)
	defer stop()
	id := s.ID()
	input := mentionRecoveryInput()
	queued, err := s.AcceptClientMutationQueue(appwire.TurnQueueParams{ClientMutationID: "mentions-promote-source", Input: input})
	if err != nil {
		t.Fatal(err)
	}
	assertMentionRecoveryQueue(t, s, queued.Receipt.QueueEntryIDs, []string{"mentions-promote-source"}, []queuedInput{mentionRecoveryWant()})
	params := appwire.TurnPromoteQueuedAsSteerParams{
		ClientMutationID: "mentions-promote", Index: 0, ExpectedEntryID: queued.Receipt.QueueEntryIDs[0],
	}
	accepted, err := s.AcceptClientMutationPromoteQueuedAsSteer(params)
	if err != nil {
		t.Fatal(err)
	}
	want := SteeringEntry{
		Text:       "/same /same 😀 /tail  ",
		Images:     []ImageAttachment{{MediaType: "image/png", Data: []byte{1, 2, 3}, Name: "first.png"}},
		SkillNames: []string{"same", "tail"}, CommandNames: []string{"same"},
	}
	// Direct promotion owns the raw, multi-text-item input, not a reserialization.
	assertMentionRecoverySteering(t, s, params.ClientMutationID, clientMutationMethodPromote, accepted.Receipt.TurnID, input, want)
	stop()
	assertMentionRecoveryQueueEvent(t, *captured, nil)
	restored := restoreQueuePersistTestSession(t, dir, id)
	defer restored.Close()
	assertMentionRecoverySteering(t, restored, params.ClientMutationID, clientMutationMethodPromote, accepted.Receipt.TurnID, input, want)
	replayed, err := restored.AcceptClientMutationPromoteQueuedAsSteer(params)
	if err != nil || replayed.Receipt.Disposition != appwire.MutationDispositionReplayed || replayed.Receipt.TurnID != accepted.Receipt.TurnID {
		t.Fatalf("promote replay = %+v, err = %v", replayed, err)
	}
	assertMentionRecoverySteering(t, restored, params.ClientMutationID, clientMutationMethodPromote, accepted.Receipt.TurnID, input, want)
}
