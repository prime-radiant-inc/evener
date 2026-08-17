//go:build serffuzz

package identifier

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// domainCase pairs a domain's minter with its validator and the prefix that
// separates it from the others.
type domainCase struct {
	name     string
	prefix   string
	mint     func() string
	validate func(string) error
}

func domainCases() []domainCase {
	return []domainCase{
		{"session", "", MustNewSessionID, ValidateSessionID},
		{"installation", "", MustNewInstallationID, ValidateInstallationID},
		{"client mutation", "", MustNewClientMutationID, ValidateClientMutationID},
		{"terminal generation", "", MustNewTerminalGeneration, ValidateTerminalGeneration},
		{"delegate", "dlg_", MustNewDelegateID, ValidateDelegateID},
		{"watch", "watch_", MustNewWatchID, ValidateWatchID},
		{"watch generation", "wg_", MustNewWatchGeneration, ValidateWatchGeneration},
		{"watch delivery", "wd_", MustNewWatchDeliveryID, ValidateWatchDeliveryID},
		{"agent call", "ag_", MustNewAgentCallID, ValidateAgentCallID},
		{"API attempt", "att_", MustNewAPIAttemptID, ValidateAPIAttemptID},
		{"synthetic call", "call_", MustNewSyntheticCallID, ValidateSyntheticCallID},
	}
}

// FuzzIdentifierDomainIDs drives every domain validator with strings nobody
// controls. IDs arrive from session files, resumed state, model-authored tool
// arguments and the wire, so each validator is a decode boundary.
//
// The oracles are relationships between the functions, not "no panic":
//
//   - Mint/validate agreement. Every minted ID passes its own validator. A
//     minter and validator that disagree make an ID that cannot be read back.
//   - Domain separation. Domains exist to keep a watch ID out of a place that
//     wants a delegate ID, so an ID minted under one prefix must be rejected by
//     every validator with a DIFFERENT prefix. The four empty-prefix domains are
//     deliberately interchangeable and are checked to accept each other.
//   - Length discipline. validateDomainID pins an exact length, so truncating or
//     extending any accepted value must make it invalid. This is what stops a
//     prefix match alone from carrying a malformed payload through.
//   - Cross-validator consistency. Whatever the fuzzer supplies, a validator
//     accepts it only when the value carries that domain's prefix and a payload
//     ValidateUUIDv7Payload also accepts — no validator may have its own opinion.
func FuzzIdentifierDomainIDs(f *testing.F) {
	f.Add("")
	f.Add("dlg_" + strings.Repeat("0", base62Width))
	f.Add(strings.Repeat("0", base62Width))
	f.Add("watch_")
	f.Add("wd_" + strings.Repeat("z", base62Width))
	f.Add("\x00\x00\x00")
	f.Add(strings.Repeat("a", 512))

	f.Fuzz(func(t *testing.T, value string) {
		cases := domainCases()

		for _, dc := range cases {
			minted := dc.mint()
			if err := dc.validate(minted); err != nil {
				t.Fatalf("%s: minted %q rejected by its own validator: %v", dc.name, minted, err)
			}
			if !strings.HasPrefix(minted, dc.prefix) {
				t.Fatalf("%s: minted %q lacks prefix %q", dc.name, minted, dc.prefix)
			}

			for _, other := range cases {
				err := other.validate(minted)
				if other.prefix == dc.prefix {
					if err != nil {
						t.Fatalf("%s ID %q rejected by %s, which shares prefix %q: %v",
							dc.name, minted, other.name, dc.prefix, err)
					}
					continue
				}
				if err == nil {
					t.Fatalf("%s ID %q accepted by %s (prefix %q); domains must not be interchangeable",
						dc.name, minted, other.name, other.prefix)
				}
			}

			// Length discipline: neither a shorter nor a longer value may pass.
			if err := dc.validate(minted[:len(minted)-1]); err == nil {
				t.Fatalf("%s accepted a truncated ID %q", dc.name, minted[:len(minted)-1])
			}
			if err := dc.validate(minted + "0"); err == nil {
				t.Fatalf("%s accepted an over-long ID %q", dc.name, minted+"0")
			}
		}

		// Whatever the fuzzer supplied, each validator's verdict must equal the
		// shared rule: correct prefix, exact length, payload accepted.
		for _, dc := range cases {
			accepted := dc.validate(value) == nil
			want := strings.HasPrefix(value, dc.prefix) &&
				len(value) == len(dc.prefix)+base62Width &&
				ValidateUUIDv7Payload(value[min(len(value), len(dc.prefix)):]) == nil
			if accepted != want {
				t.Fatalf("%s.validate(%q) = %v, but the shared rule says %v", dc.name, value, accepted, want)
			}
		}
	})
}

// FuzzJobIDBoundary drives the job-ID seam, whose distinguishing feature is that
// a job ID EMBEDS an owner session ID at fixed offsets. That makes validation
// load-bearing in a way the plain domains are not: JobOwnerSessionID slices the
// owner out by arithmetic, so anything ValidateJobID accepts must yield an owner
// that ValidateSessionID also accepts, or a malformed job ID becomes a wrong
// owner rather than an error.
func FuzzJobIDBoundary(f *testing.F) {
	f.Add("", 26)
	f.Add("job_"+strings.Repeat("0", base62Width)+"_000000000001", 26)
	f.Add("job_", 0)
	f.Add(strings.Repeat("job_", 40), -1)

	f.Fuzz(func(t *testing.T, jobID string, maxLength int) {
		if err := ValidateJobID(jobID); err == nil {
			owner, err := JobOwnerSessionID(jobID)
			if err != nil {
				t.Fatalf("ValidateJobID accepted %q but JobOwnerSessionID rejected it: %v", jobID, err)
			}
			if err := ValidateSessionID(owner); err != nil {
				t.Fatalf("job %q carried owner %q that ValidateSessionID rejects: %v", jobID, owner, err)
			}
		} else if owner, err := JobOwnerSessionID(jobID); err == nil {
			t.Fatalf("JobOwnerSessionID returned owner %q for a job ID ValidateJobID rejects: %q", owner, jobID)
		}

		// Abbreviation is bounded by DISPLAY width, not bytes: maxLength is a
		// column budget and the ellipsis is one character but three bytes, so an
		// abbreviation is routinely longer in bytes than the value it replaced.
		// Counting runes is the assertion that matches what the budget means.
		got := AbbreviateJobID(jobID, maxLength)
		if got != jobID {
			if n := utf8.RuneCountInString(got); n > maxLength {
				t.Fatalf("AbbreviateJobID(%q, %d) = %q, %d characters wide", jobID, maxLength, got, n)
			}
			// The random suffix is what keeps two jobs from the same session
			// distinguishable once the middle is elided, so it must survive whole.
			suffix := jobID[len(jobID)-jobIDSuffixSize:]
			if !strings.HasSuffix(got, suffix) {
				t.Fatalf("AbbreviateJobID(%q, %d) = %q, dropping the %q suffix", jobID, maxLength, got, suffix)
			}
		}
	})
}
