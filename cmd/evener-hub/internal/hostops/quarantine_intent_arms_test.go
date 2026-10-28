package hostops

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The quarantine artifact arms: every malformed intent, epoch sidecar and
// custody file a boot can find beside its store is refused with its own
// sentinel, never served past. Each row seeds one artifact next to its own
// store path and asserts which sentinel Open answers with; the happy recovery
// paths live in quarantine_test.go.

// validCustodyBody is a complete custody file for the store at path: one fence
// import at the high-water mark, exactly the seven-key schema the writing boot
// emits. The mutate hook lets a row bend one field without re-restating the
// whole document.
func validCustodyBody(t *testing.T, path string, mutate func(map[string]any)) []byte {
	t.Helper()
	boundary := []any{map[string]any{"kind": "local-markerless", "platform": "linux", "nonce": "n"}}
	body := map[string]any{
		"quarantineEpoch":        2,
		"quarantinedFile":        path,
		"custodiedAt":            "2026-09-26T00:00:00Z",
		"recordIds":              []any{map[string]any{"recordId": "00000000000000000009", "host": "hB"}},
		"allocatorHighWaterMark": 9,
		"fences":                 []any{map[string]any{"recordId": "00000000000000000009", "host": "hB", "kind": "restart", "clientOperationId": "op-hB", "generation": 3, "incarnationId": "inc-hB", "quarantine": false, "boundary": boundary}},
		"ownership":              []any{map[string]any{"quarantineRecordId": "00000000000000000009", "host": "hB", "kind": "restart", "clientOperationId": "op-hB", "generation": 3, "highWaterMark": 3, "incarnationId": "inc-hB"}},
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal custody body: %v", err)
	}
	return raw
}

// validIntentBody is the pending-work record the writing boot leaves for the
// store at path, naming the stamp's custody and aside artifacts.
func validIntentBody(t *testing.T, path, stamp string, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"corruptFile":     path,
		"custodyFile":     quarantineCustodyPath(path, stamp),
		"asideFile":       quarantineAsidePath(path, stamp),
		"quarantinedAt":   "2026-09-26T00:00:00Z",
		"quarantineEpoch": 2,
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal intent body: %v", err)
	}
	return raw
}

// artifactSeed prepares one store directory for a refusal row. path is the row's
// own store path, so parallel rows never share state.
type artifactSeed func(t *testing.T, path string)

// refusalRow names one store-side artifact to seed and the sentinel Open
// must refuse the boot with when the artifact is malformed.
type refusalRow struct {
	name    string
	seed    artifactSeed
	wantErr error
}

// runRefusalRows seeds each row's store directory and asserts Open refuses
// it; served names the artifact family in the refusal message.
func runRefusalRows(t *testing.T, cases []refusalRow, served string) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := StorePath(t.TempDir())
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("mkdir store dir: %v", err)
			}
			tc.seed(t, path)
			_, err := Open(path)
			if err == nil {
				t.Fatalf("Open served a store %s", served)
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("Open error = %v, want errors.Is(%v)", err, tc.wantErr)
			}
		})
	}
}

// TestQuarantineIntentArmsRefuseTheBoot covers readQuarantineIntent's refusal
// arms: an intent that is not the record the writing boot emits is incomplete
// custody, and the boot refuses over it rather than guessing what it was doing.
func TestQuarantineIntentArmsRefuseTheBoot(t *testing.T) {
	t.Parallel()
	const stamp = "20260926T000000.000000000Z"

	cases := []refusalRow{
		{
			name: "a directory where the intent belongs",
			seed: func(t *testing.T, path string) {
				if err := os.Mkdir(quarantineIntentPath(path), 0o700); err != nil {
					t.Fatalf("mkdir intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent readable beyond its owner",
			seed: func(t *testing.T, path string) {
				if err := os.WriteFile(quarantineIntentPath(path), validIntentBody(t, path, stamp, nil), 0o644); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreReadableBeyondOwner,
		},
		{
			name:    "intent bytes that are not valid UTF-8",
			seed:    seedArtifactDeferred(quarantineIntentPath, []byte("{\"corruptFile\": \"\xff\xfe\"}")),
			wantErr: ErrStoreCorrupt,
		},
		{
			name:    "an intent naming a key twice",
			seed:    seedArtifactDeferred(quarantineIntentPath, []byte(`{"corruptFile":"x","corruptFile":"x"}`)),
			wantErr: ErrStoreCorrupt,
		},
		{
			name:    "an intent that does not decode",
			seed:    seedArtifactDeferred(quarantineIntentPath, []byte("{")),
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent carrying a key the writer never emits",
			seed: func(t *testing.T, path string) {
				body := validIntentBody(t, path, stamp, func(m map[string]any) { m["extra"] = 1 })
				if err := os.WriteFile(quarantineIntentPath(path), body, 0o600); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent carrying trailing data",
			seed: func(t *testing.T, path string) {
				body := append(validIntentBody(t, path, stamp, nil), []byte(" {}")...)
				if err := os.WriteFile(quarantineIntentPath(path), body, 0o600); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent naming another store's corrupt file",
			seed: func(t *testing.T, path string) {
				body := validIntentBody(t, path, stamp, func(m map[string]any) { m["corruptFile"] = path + ".elsewhere" })
				if err := os.WriteFile(quarantineIntentPath(path), body, 0o600); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent naming no custody file",
			seed: func(t *testing.T, path string) {
				body := validIntentBody(t, path, stamp, func(m map[string]any) { m["custodyFile"] = "" })
				if err := os.WriteFile(quarantineIntentPath(path), body, 0o600); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent naming no aside file",
			seed: func(t *testing.T, path string) {
				body := validIntentBody(t, path, stamp, func(m map[string]any) { m["asideFile"] = "" })
				if err := os.WriteFile(quarantineIntentPath(path), body, 0o600); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent carrying no boot timestamp",
			seed: func(t *testing.T, path string) {
				body := validIntentBody(t, path, stamp, func(m map[string]any) { m["quarantinedAt"] = "0001-01-01T00:00:00Z" })
				if err := os.WriteFile(quarantineIntentPath(path), body, 0o600); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent carrying no quarantine epoch",
			seed: func(t *testing.T, path string) {
				body := validIntentBody(t, path, stamp, func(m map[string]any) { m["quarantineEpoch"] = 0 })
				if err := os.WriteFile(quarantineIntentPath(path), body, 0o600); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent whose aside is not this store's",
			seed: func(t *testing.T, path string) {
				body := validIntentBody(t, path, stamp, func(m map[string]any) {
					m["asideFile"] = filepath.Join(filepath.Dir(path), "elsewhere-quarantined-x")
				})
				if err := os.WriteFile(quarantineIntentPath(path), body, 0o600); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent whose aside stamp climbs out of the directory",
			seed: func(t *testing.T, path string) {
				body := validIntentBody(t, path, stamp, func(m map[string]any) {
					m["asideFile"] = quarantineAsidePath(path, "climb/out")
				})
				if err := os.WriteFile(quarantineIntentPath(path), body, 0o600); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an intent whose custody does not match its aside stamp",
			seed: func(t *testing.T, path string) {
				body := validIntentBody(t, path, stamp, func(m map[string]any) {
					m["custodyFile"] = quarantineCustodyPath(path, "a-different-stamp")
				})
				if err := os.WriteFile(quarantineIntentPath(path), body, 0o600); err != nil {
					t.Fatalf("write intent: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
	}
	runRefusalRows(t, cases, "beside a malformed quarantine intent")
}

// seedArtifactDeferred defers a fixed body to a path derived from the row's
// store path.
func seedArtifactDeferred(derive func(path string) string, body []byte) artifactSeed {
	return func(t *testing.T, path string) {
		t.Helper()
		if err := os.WriteFile(derive(path), body, 0o600); err != nil {
			t.Fatalf("write %s: %v", derive(path), err)
		}
	}
}

// TestQuarantineEpochSidecarArmsRefuseTheBoot covers readQuarantineEpochFile's
// refusal arms: a sidecar that is not the counter's own schema is refused, so
// a hand-edited or swapped file cannot move the counter the cursors pin.
func TestQuarantineEpochSidecarArmsRefuseTheBoot(t *testing.T) {
	t.Parallel()

	cases := []refusalRow{
		{
			name: "a directory where the sidecar belongs",
			seed: func(t *testing.T, path string) {
				if err := os.Mkdir(quarantineEpochPath(path), 0o700); err != nil {
					t.Fatalf("mkdir sidecar: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "a sidecar readable beyond its owner",
			seed: func(t *testing.T, path string) {
				if err := os.WriteFile(quarantineEpochPath(path), []byte(`{"quarantineEpoch":1}`), 0o644); err != nil {
					t.Fatalf("write sidecar: %v", err)
				}
			},
			wantErr: ErrStoreReadableBeyondOwner,
		},
		{
			name:    "sidecar bytes that are not valid UTF-8",
			seed:    seedArtifactDeferred(quarantineEpochPath, []byte("{\"quarantineEpoch\":\xff}")),
			wantErr: ErrStoreCorrupt,
		},
		{
			name:    "a sidecar that does not decode",
			seed:    seedArtifactDeferred(quarantineEpochPath, []byte("{")),
			wantErr: ErrStoreCorrupt,
		},
		{
			name:    "a sidecar carrying a key the writer never emits",
			seed:    seedArtifactDeferred(quarantineEpochPath, []byte(`{"quarantineEpoch":1,"extra":2}`)),
			wantErr: ErrStoreCorrupt,
		},
		{
			name:    "a sidecar carrying trailing data",
			seed:    seedArtifactDeferred(quarantineEpochPath, []byte(`{"quarantineEpoch":1} {}`)),
			wantErr: ErrStoreCorrupt,
		},
		{
			name:    "a sidecar carrying no quarantine epoch",
			seed:    seedArtifactDeferred(quarantineEpochPath, []byte(`{"quarantineEpoch":0}`)),
			wantErr: ErrStoreCorrupt,
		},
	}
	runRefusalRows(t, cases, "beside a malformed quarantine epoch sidecar")
}

// TestQuarantineCustodyArmsRefuseTheBoot covers readCustodyFile and
// assembleCustody's refusal arms over a custody file the scan finds beside the
// store: the boot never leans on a custody snapshot it cannot account for.
func TestQuarantineCustodyArmsRefuseTheBoot(t *testing.T) {
	t.Parallel()
	const stamp = "20260926T000000.000000000Z"

	cases := []refusalRow{
		{
			name: "a custody file with no timestamp in its name",
			seed: func(t *testing.T, path string) {
				if err := os.WriteFile(path+".custody-.json", []byte("{}"), 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "a directory where the custody file belongs",
			seed: func(t *testing.T, path string) {
				if err := os.Mkdir(quarantineCustodyPath(path, stamp), 0o700); err != nil {
					t.Fatalf("mkdir custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a custody file readable beyond its owner",
			seed: func(t *testing.T, path string) {
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), validCustodyBody(t, path, nil), 0o644); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrStoreReadableBeyondOwner,
		},
		{
			name: "a custody file that does not decode",
			seed: seedArtifactDeferred(func(path string) string {
				return quarantineCustodyPath(path, stamp)
			}, []byte("{")),
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a custody file naming the quarantined file of another store",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) { m["quarantinedFile"] = path + ".elsewhere" })
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a custody file carrying only six of the seven keys",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) { delete(m, "ownership") })
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a custody file carrying a key the writer never emits",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) { m["extra"] = 1 })
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a custody file carrying no quarantine epoch",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) { m["quarantineEpoch"] = 0 })
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a custody file naming no quarantined file",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) { m["quarantinedFile"] = "" })
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a custody file carrying no timestamp",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) { m["custodiedAt"] = "0001-01-01T00:00:00Z" })
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "an ownership entry carrying no quarantine record id",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					m["ownership"].([]any)[0].(map[string]any)["quarantineRecordId"] = ""
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "two ownership entries sharing one quarantine record id",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					ownership := m["ownership"].([]any)
					second := map[string]any{}
					for k, v := range ownership[0].(map[string]any) {
						second[k] = v
					}
					second["host"] = "hC"
					m["ownership"] = append(ownership, second)
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "one host carrying two ownership entries",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					ownership := m["ownership"].([]any)
					second := map[string]any{}
					for k, v := range ownership[0].(map[string]any) {
						second[k] = v
					}
					second["quarantineRecordId"] = "00000000000000000008"
					m["ownership"] = append(ownership, second)
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "an ownership entry carrying generation zero",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					m["ownership"].([]any)[0].(map[string]any)["generation"] = 0
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "an ownership entry whose high-water mark sits below its generation",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					m["ownership"].([]any)[0].(map[string]any)["highWaterMark"] = 2
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a recordIds row with no record and no host",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					m["recordIds"] = append(m["recordIds"].([]any), map[string]any{"recordId": "", "host": ""})
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a recordIds row naming an id twice",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					m["recordIds"] = append(m["recordIds"].([]any), map[string]any{"recordId": "00000000000000000009", "host": "hB"})
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a recordIds row that is not an imported record",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					m["recordIds"] = append(m["recordIds"].([]any), map[string]any{"recordId": "00000000000000000005", "host": "hC"})
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a fence carrying a kind outside the record set",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					m["fences"].([]any)[0].(map[string]any)["kind"] = "nonsense"
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a fence carrying no record id",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					m["fences"].([]any)[0].(map[string]any)["recordId"] = ""
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "two fences sharing one record id",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					fences := m["fences"].([]any)
					second := map[string]any{}
					for k, v := range fences[0].(map[string]any) {
						second[k] = v
					}
					second["host"] = "hC"
					m["fences"] = append(fences, second)
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a fence naming a host with no ownership entry",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					m["fences"].([]any)[0].(map[string]any)["host"] = "hZ"
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "a fence import sitting above the pre-quarantine high-water mark",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					const above = "00000000000000000011"
					m["fences"].([]any)[0].(map[string]any)["recordId"] = above
					m["ownership"].([]any)[0].(map[string]any)["quarantineRecordId"] = above
					m["recordIds"].([]any)[0].(map[string]any)["recordId"] = above
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
		{
			name: "an ownership-only import sitting at the pre-quarantine high-water mark",
			seed: func(t *testing.T, path string) {
				body := validCustodyBody(t, path, func(m map[string]any) {
					m["ownership"] = append(m["ownership"].([]any), map[string]any{
						"quarantineRecordId": "00000000000000000005", "host": "hC", "kind": "restart",
						"clientOperationId": "op-hC", "generation": 2, "highWaterMark": 2, "incarnationId": "inc-hC",
					})
					m["recordIds"] = append(m["recordIds"].([]any), map[string]any{"recordId": "00000000000000000005", "host": "hC"})
				})
				if err := os.WriteFile(quarantineCustodyPath(path, stamp), body, 0o600); err != nil {
					t.Fatalf("write custody: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
	}
	runRefusalRows(t, cases, "beside a malformed custody file")
}

// TestQuarantineAsideArmsRefuseTheBoot covers the aside-file arms of the scan:
// §4's rule that an aside file with no complete custody fails startup, never
// serves, plus the kind and mode rules every artifact beside the store carries.
func TestQuarantineAsideArmsRefuseTheBoot(t *testing.T) {
	t.Parallel()
	const stamp = "20260926T000000.000000000Z"

	cases := []refusalRow{
		{
			name: "an aside file with no timestamp in its name",
			seed: func(t *testing.T, path string) {
				if err := os.WriteFile(path+quarantineAsideInfix, []byte("bytes"), 0o600); err != nil {
					t.Fatalf("write aside: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "a directory where the aside file belongs",
			seed: func(t *testing.T, path string) {
				if err := os.Mkdir(quarantineAsidePath(path, stamp), 0o700); err != nil {
					t.Fatalf("mkdir aside: %v", err)
				}
			},
			wantErr: ErrStoreCorrupt,
		},
		{
			name: "an aside file readable beyond its owner",
			seed: func(t *testing.T, path string) {
				if err := os.WriteFile(quarantineAsidePath(path, stamp), []byte("bytes"), 0o644); err != nil {
					t.Fatalf("write aside: %v", err)
				}
			},
			wantErr: ErrStoreReadableBeyondOwner,
		},
		{
			name: "an aside file with no custody to account for it",
			seed: func(t *testing.T, path string) {
				if err := os.WriteFile(quarantineAsidePath(path, stamp), []byte("bytes"), 0o600); err != nil {
					t.Fatalf("write aside: %v", err)
				}
			},
			wantErr: ErrQuarantineIncomplete,
		},
	}
	runRefusalRows(t, cases, "beside a malformed quarantine aside")
}

// TestQuarantineSignalOfNilStore pins the nil-receiver arm: a nil handle has no
// quarantine to report, and the accessor says so instead of panicking.
func TestQuarantineSignalOfNilStore(t *testing.T) {
	t.Parallel()
	var store *Store
	if signal := store.Quarantine(); signal != nil {
		t.Fatalf("(*Store)(nil).Quarantine() = %+v, want nil", signal)
	}
}
