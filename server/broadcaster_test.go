package server

import (
	"testing"
	"time"
)

func TestBroadcaster_SingleSubscriber(t *testing.T) {
	b := NewBroadcaster(100)

	ch, unsub := b.Subscribe(0)
	defer unsub()

	b.Send("hello")

	select {
	case item := <-ch:
		if item.Value != "hello" {
			t.Errorf("got %v, want hello", item.Value)
		}
		if item.ID != 1 {
			t.Errorf("got ID %d, want 1", item.ID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestBroadcaster_MultipleSubscribers(t *testing.T) {
	b := NewBroadcaster(100)

	ch1, unsub1 := b.Subscribe(0)
	defer unsub1()
	ch2, unsub2 := b.Subscribe(0)
	defer unsub2()

	b.Send("msg")

	for i, ch := range []<-chan BufferItem{ch1, ch2} {
		select {
		case item := <-ch:
			if item.Value != "msg" {
				t.Errorf("subscriber %d: got %v, want msg", i, item.Value)
			}
		case <-time.After(time.Second):
			t.Fatalf("subscriber %d: timeout", i)
		}
	}
}

func TestBroadcaster_Unsubscribe(t *testing.T) {
	b := NewBroadcaster(100)

	ch, unsub := b.Subscribe(0)
	unsub()

	b.Send("after-unsub")

	select {
	case _, ok := <-ch:
		if ok {
			t.Error("expected channel to be closed after unsubscribe")
		}
	case <-time.After(100 * time.Millisecond):
		// Channel closed, no more reads — this is fine
	}
}

func TestBroadcaster_CatchupOnSubscribe(t *testing.T) {
	b := NewBroadcaster(100)

	// Pre-load events into the ring buffer
	b.Send("a")
	b.Send("b")
	b.Send("c")

	// Subscribe with lastID=1 — should get b, c as catchup
	ch, unsub := b.Subscribe(1)
	defer unsub()

	var got []string
	for i := 0; i < 2; i++ {
		select {
		case item := <-ch:
			got = append(got, item.Value.(string))
		case <-time.After(time.Second):
			t.Fatal("timeout")
		}
	}
	if len(got) != 2 || got[0] != "b" || got[1] != "c" {
		t.Errorf("catchup: got %v, want [b c]", got)
	}
}

func TestBroadcaster_SubscriberCount(t *testing.T) {
	b := NewBroadcaster(100)
	if b.SubscriberCount() != 0 {
		t.Errorf("expected 0, got %d", b.SubscriberCount())
	}

	_, unsub1 := b.Subscribe(0)
	_, unsub2 := b.Subscribe(0)
	if b.SubscriberCount() != 2 {
		t.Errorf("expected 2, got %d", b.SubscriberCount())
	}

	unsub1()
	// Give a moment for cleanup
	time.Sleep(10 * time.Millisecond)
	if b.SubscriberCount() != 1 {
		t.Errorf("expected 1, got %d", b.SubscriberCount())
	}
	unsub2()
}

func TestBroadcaster_SlowSubscriberDropsEvents(t *testing.T) {
	b := NewBroadcaster(100)

	ch, unsub := b.Subscribe(0)
	defer unsub()

	// Flood events without reading
	for i := 0; i < 200; i++ {
		b.Send(i)
	}

	// Should still be able to read some (channel buffer)
	// and not block the broadcaster
	count := 0
	for {
		select {
		case <-ch:
			count++
		default:
			goto done
		}
	}
done:
	if count == 0 {
		t.Error("expected at least some events delivered")
	}
}
