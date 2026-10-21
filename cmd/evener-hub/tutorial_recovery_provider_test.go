package hub

import (
	"io"
	"net/http"
	"testing"
	"time"
)

func TestTutorialRecoveryDiscoveryIsolation(t *testing.T) {
	providerForGeneration := tutorialRecoveryProviders(t)
	firstURL, firstReady := providerForGeneration()
	client := &http.Client{Timeout: 5 * time.Second}
	discover := func(url string) {
		t.Helper()
		response, err := client.Get(url + "/v1/models")
		if err != nil {
			t.Fatal("discovery request failed")
		}
		defer response.Body.Close()
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			t.Fatal(err)
		}
		if response.StatusCode != http.StatusOK {
			t.Fatalf("discovery status = %d", response.StatusCode)
		}
	}
	discover(firstURL)
	select {
	case <-firstReady:
	default:
		t.Fatal("first generation did not observe its own HTTP request")
	}
	// Extra and late requests from generation one must never release generation two.
	discover(firstURL)
	secondURL, secondReady := providerForGeneration()
	discover(firstURL)
	select {
	case <-secondReady:
		t.Fatal("first generation discovery released second generation")
	default:
	}
	discover(secondURL)
	select {
	case <-secondReady:
	default:
		t.Fatal("second generation did not observe its own HTTP request")
	}
}
