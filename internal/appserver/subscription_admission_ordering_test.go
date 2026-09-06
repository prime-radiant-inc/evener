package appserver

import "testing"

func TestSubscriptionAdmissionExistsBeforeRefResolution(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-ref-only")
	admission := conn.beginSubscriptionAdmission(`1`, "")
	if admission == nil {
		t.Fatal("ref-only thread/read lost its admission before resolver preparation")
	}
}

func TestSubscriptionAdmissionEarlyUnsubscribeSurvivesCanonicalPreparation(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-alias")
	admission := conn.beginSubscriptionAdmission(`1`, "canonical:stable")
	if admission == nil {
		t.Fatal("raw thread/read admission was not created")
	}
	conn.cancelSubscriptionAdmissions("canonical:stable")
	conn.admissionMu.Lock()
	_, allowed := conn.claimSubscriptionAdmission(`1`)
	conn.admissionMu.Unlock()
	if allowed {
		t.Fatal("unsubscribe before canonical preparation did not cancel the read")
	}
}

func TestSubscriptionAdmissionSameThreadPreAndPostUnsubscribe(t *testing.T) {
	server := NewServer(ServerConfig{})
	conn := server.NewConnection("conn-order")
	first := conn.beginSubscriptionAdmission(`1`, "thread")
	if first == nil {
		t.Fatal("first admission missing")
	}
	conn.cancelSubscriptionAdmissions("thread")
	second := conn.beginSubscriptionAdmission(`2`, "thread")
	if second == nil {
		t.Fatal("second admission missing")
	}
	conn.admissionMu.Lock()
	_, firstAllowed := conn.claimSubscriptionAdmission(`1`)
	_, secondAllowed := conn.claimSubscriptionAdmission(`2`)
	conn.admissionMu.Unlock()
	if firstAllowed {
		t.Fatal("pre-unsubscribe admission was allowed")
	}
	if !secondAllowed {
		t.Fatal("post-unsubscribe admission was canceled with the stale read")
	}
}
