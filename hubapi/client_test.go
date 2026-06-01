package hubapi_test

import (
	"testing"

	"primeradiant.com/serf/hubapi"
)

func TestClientURLPreservesQueryString(t *testing.T) {
	client, err := hubapi.NewClient("http://127.0.0.1:9180", nil)
	if err != nil {
		t.Fatal(err)
	}
	got := client.URL("/api/sessions/local:01ABC?include=details")
	want := "http://127.0.0.1:9180/api/sessions/local:01ABC?include=details"
	if got != want {
		t.Fatalf("URL()=%q, want %q", got, want)
	}
}
