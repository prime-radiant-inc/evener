package hub

import (
	"encoding/json"
	"reflect"
	"sync"
	"testing"

	"primeradiant.com/evener/appwire"
)

func startupTestThread() appwire.Thread {
	return appwire.Thread{
		ID: "new-thread", SessionID: "new-thread", Source: "local",
		Evener: appwire.EvenerThread{Ref: "local:new-thread", InstanceID: "first-instance"},
	}
}

func startupTestNotification(thread appwire.Thread) appwire.Notification {
	return *appwire.NotificationMessage(appwire.NotifyThreadStarted, appwire.ThreadStartedParams{
		ThreadID: thread.ID, Ref: threadRef(thread), Thread: thread,
	}).Notification
}

func TestThreadStartupAdmissionChoosesOneAnnouncement(t *testing.T) {
	for _, originalBeforeAdmission := range []bool{false, true} {
		t.Run(map[bool]string{false: "missing original", true: "held original"}[originalBeforeAdmission], func(t *testing.T) {
			thread := startupTestThread()
			startup := &hubThreadStartup{thread: thread}
			original := startupTestNotification(thread)
			if originalBeforeAdmission && !startup.hold(original) {
				t.Fatal("original escaped before admission")
			}
			announcement, broadcast := startup.finish(true)
			if announcement == nil || broadcast != originalBeforeAdmission {
				t.Fatalf("finish = %v, broadcast=%v", announcement, broadcast)
			}
			var params appwire.ThreadStartedParams
			if err := json.Unmarshal(announcement.Params, &params); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(params.Thread, thread) || params.Ref != threadRef(thread) || params.ThreadID != thread.ID {
				t.Fatalf("startup snapshot = %+v, want %+v", params, thread)
			}
			if !startup.hold(original) {
				t.Fatal("late original escaped after the creation announcement")
			}
			if duplicate, _ := startup.finish(true); duplicate != nil {
				t.Fatal("admission published twice")
			}
		})
	}
}

func TestThreadStartupDoesNotConsumeAnotherInstanceOrTarget(t *testing.T) {
	thread := startupTestThread()
	startup := &hubThreadStartup{thread: thread}
	startup.finish(true)
	for _, alter := range []func(*appwire.Thread){
		func(thread *appwire.Thread) { thread.Evener.InstanceID = "second-instance" },
		func(thread *appwire.Thread) { thread.Evener.Ref = "remote:new-thread" },
		func(thread *appwire.Thread) { thread.ID = "another-thread" },
	} {
		other := thread
		alter(&other)
		if startup.hold(startupTestNotification(other)) {
			t.Fatalf("consumed another startup: %+v", other)
		}
	}
	if startup.hold(appwire.Notification{Method: appwire.NotifyThreadStarted, Params: json.RawMessage("{")}) {
		t.Fatal("consumed malformed startup")
	}
	if startup.hold(appwire.Notification{Method: appwire.NotifyThreadStatusChanged}) {
		t.Fatal("consumed a different notification method")
	}
}

func TestThreadStartupFailedAdmissionReleasesSuppression(t *testing.T) {
	thread := startupTestThread()
	startup := &hubThreadStartup{thread: thread}
	original := startupTestNotification(thread)
	startup.hold(original)
	if notification, _ := startup.finish(false); notification != nil {
		t.Fatal("failed admission published a startup")
	}
	if startup.hold(original) {
		t.Fatal("failed admission suppressed a later source event")
	}
	if notification, _ := startup.finish(true); notification != nil {
		t.Fatal("retired admission was revived")
	}
}

func TestThreadStartupOriginalRacingAdmissionPublishesOnce(t *testing.T) {
	thread := startupTestThread()
	original := startupTestNotification(thread)
	for range 100 {
		startup := &hubThreadStartup{thread: thread}
		begin := make(chan struct{})
		var held bool
		var notification *appwire.Notification
		var done sync.WaitGroup
		done.Add(2)
		go func() {
			defer done.Done()
			<-begin
			held = startup.hold(original)
		}()
		go func() {
			defer done.Done()
			<-begin
			notification, _ = startup.finish(true)
		}()
		close(begin)
		done.Wait()
		if !held || notification == nil {
			t.Fatalf("race lost or duplicated announcement: held=%v, announcement=%v", held, notification)
		}
	}
}
