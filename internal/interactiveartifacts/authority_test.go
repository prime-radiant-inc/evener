package interactiveartifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"primeradiant.com/evener/identifier"
)

func TestHostAuthorityPersistsInstallationAndRootAssociation(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	installation := authority.Installation()
	sessionID := identifier.MustNewSessionID()
	request := RootRequest{
		SessionID:    sessionID,
		ProjectID:    "project-0123456789",
		RealmID:      installation.RealmID,
		HumanOwnerID: installation.HumanOwnerID,
	}
	first, err := authority.PrepareRoot(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first.NamespaceID == "" || first.PrincipalID == "" {
		t.Fatal("root association must mint stable IDs")
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Installation(); got != installation {
		t.Fatalf("installation changed across reopen: got %+v want %+v", got, installation)
	}
	second, err := reopened.PrepareRoot(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if second != first {
		t.Fatalf("root association changed across reopen: got %+v want %+v", second, first)
	}
}

func TestHostAuthorityRejectsChangedRootOwnerOrRealm(t *testing.T) {
	authority, err := OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	installation := authority.Installation()
	request := RootRequest{
		SessionID:    identifier.MustNewSessionID(),
		ProjectID:    "project-0123456789",
		RealmID:      installation.RealmID,
		HumanOwnerID: installation.HumanOwnerID,
	}
	if _, err := authority.PrepareRoot(t.Context(), request); err != nil {
		t.Fatal(err)
	}
	for name, mutate := range map[string]func(*RootRequest){
		"owner": func(request *RootRequest) { request.HumanOwnerID = "different-owner" },
		"realm": func(request *RootRequest) { request.RealmID = "different-realm" },
	} {
		t.Run(name, func(t *testing.T) {
			changed := request
			mutate(&changed)
			if _, err := authority.PrepareRoot(t.Context(), changed); err == nil {
				t.Fatal("changed root association was accepted")
			}
		})
	}
}

func TestHostAuthorityPublishesImmutablePolicyAndDeletionIntents(t *testing.T) {
	authority, err := OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	installation := authority.Installation()
	root := RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID}
	association, err := authority.PrepareRoot(t.Context(), root)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authority.Policy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	policy[0].NamespaceID = "mutated"
	fresh, err := authority.Policy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if fresh[0].NamespaceID != association.NamespaceID {
		t.Fatalf("policy snapshot was mutable: %+v", fresh)
	}
	intent, err := authority.BeginSessionDeletion(t.Context(), root.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if !intent.Root || intent.NamespaceID != association.NamespaceID {
		t.Fatalf("root deletion intent lost namespace scope: %+v", intent)
	}
	fresh, err = authority.Policy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(fresh) != 1 || !fresh[0].Tombstone {
		t.Fatalf("root tombstone missing from policy: %+v", fresh)
	}
	if err := authority.ReconcileDeletion(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.PrepareRoot(t.Context(), root); !errors.Is(err, ErrDeleted) {
		t.Fatalf("deleted root revived: %v", err)
	}
}

func TestHostAuthoritySeparatesRootAndChildDeletion(t *testing.T) {
	authority, err := OpenHostAuthority(filepath.Join(t.TempDir(), "artifacts"))
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	installation := authority.Installation()
	rootRequest := RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID}
	root, err := authority.PrepareRoot(t.Context(), rootRequest)
	if err != nil {
		t.Fatal(err)
	}
	childRequest := SessionRequest{SessionID: identifier.MustNewSessionID(), RootSessionID: root.SessionID, ProjectID: root.ProjectID, RealmID: root.RealmID, HumanOwnerID: root.HumanOwnerID}
	child, err := authority.EnsureSession(t.Context(), childRequest)
	if err != nil {
		t.Fatal(err)
	}
	if child.NamespaceID != root.NamespaceID || child.PrincipalID == root.PrincipalID {
		t.Fatalf("child identity did not preserve namespace/isolate principal: root=%+v child=%+v", root, child)
	}
	childIntent, err := authority.BeginSessionDeletion(t.Context(), child.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if childIntent.Root {
		t.Fatal("child deletion created root intent")
	}
	policy, err := authority.Policy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(policy) != 1 || policy[0].Tombstone {
		t.Fatalf("child deletion tombstoned shared namespace: %+v", policy)
	}
	if _, err := authority.EnsureSession(t.Context(), childRequest); !errors.Is(err, ErrDeleted) {
		t.Fatalf("deleted child was revived: %v", err)
	}

	forkRequest := rootRequest
	forkRequest.SessionID = identifier.MustNewSessionID()
	fork, err := authority.PrepareRoot(t.Context(), forkRequest)
	if err != nil {
		t.Fatal(err)
	}
	if fork.NamespaceID == root.NamespaceID || fork.PrincipalID == root.PrincipalID {
		t.Fatalf("fork root reused historical identity: root=%+v fork=%+v", root, fork)
	}
}

func TestHostAuthorityFailsClosedForMalformedPrivateSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, authorityFilename)
	if err := os.Chmod(path, 0640); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenHostAuthority(root); err == nil {
		t.Fatal("wrong-mode authority snapshot opened")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"installation":`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenHostAuthority(root); err == nil {
		t.Fatal("corrupt authority snapshot opened")
	}
	if err := os.WriteFile(path, []byte(`{"version":1,"installation":{"realmId":"realm","humanOwnerId":"owner"},"sessions":[],"deletions":[]}`), 0600); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "authority-target.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, path); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenHostAuthority(root); err == nil {
		t.Fatal("symlink authority snapshot opened")
	}
}

func TestHostAuthorityRejectsUnknownVersionFieldsAndOversizedInput(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, authorityFilename)
	valid := `"installation":{"realmId":"realm","humanOwnerId":"owner"},"sessions":[],"deletions":[]}`
	for name, data := range map[string][]byte{
		"unknown-version": []byte(`{"version":99,` + valid),
		"unknown-field":   []byte(`{"version":1,"unexpected":true,` + valid),
		"oversized":       bytes.Repeat([]byte("x"), authorityMaxBytes+1),
	} {
		t.Run(name, func(t *testing.T) {
			if err := os.WriteFile(path, data, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := OpenHostAuthority(root); err == nil {
				t.Fatal("invalid authority snapshot opened")
			}
		})
	}
}

func TestHostAuthorityRejectsDuplicateDecodedIdentities(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	installation := authorityInstallation{RealmID: "realm-identity", HumanOwnerID: "owner-identity"}
	first := authoritySession{SessionID: identifier.MustNewSessionID(), RootSessionID: "", PrincipalID: "principal-one", NamespaceID: "namespace-one", ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID}
	first.RootSessionID = first.SessionID
	second := first
	second.SessionID = identifier.MustNewSessionID()
	second.RootSessionID = second.SessionID
	second.PrincipalID = "principal-two"
	second.NamespaceID = "namespace-two"
	write := func(t *testing.T, sessions []authoritySession) {
		t.Helper()
		data, err := json.Marshal(authoritySnapshot{Version: authoritySchemaVersion, Installation: installation, Sessions: sessions, Deletions: []authorityDeletion{}})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, authorityFilename), data, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := OpenHostAuthority(root); err == nil {
			t.Fatal("malformed duplicate identity snapshot opened")
		}
	}

	duplicatePrincipal := second
	duplicatePrincipal.PrincipalID = first.PrincipalID
	write(t, []authoritySession{first, duplicatePrincipal})
	duplicateNamespace := second
	duplicateNamespace.NamespaceID = first.NamespaceID
	write(t, []authoritySession{first, duplicateNamespace})
}

func TestHostAuthorityRejectsOversizedReplacementBeforeWriting(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	installation := authority.Installation()
	first, err := authority.PrepareRoot(t.Context(), RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID})
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(root, authorityFilename))
	if err != nil {
		t.Fatal(err)
	}
	next := authority.snapshot
	next.Sessions = slices.Clone(next.Sessions)
	for {
		sessionID := identifier.MustNewSessionID()
		next.Sessions = append(next.Sessions, authoritySession{SessionID: sessionID, RootSessionID: sessionID, PrincipalID: randomID(), NamespaceID: randomID(), ProjectID: first.ProjectID, RealmID: first.RealmID, HumanOwnerID: first.HumanOwnerID})
		data, err := json.Marshal(next)
		if err != nil {
			t.Fatal(err)
		}
		if len(data) > authorityMaxBytes {
			break
		}
	}
	authority.mu.Lock()
	err = authority.commitLocked(next)
	authority.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("oversized replacement was not refused: %v", err)
	}
	after, err := os.ReadFile(filepath.Join(root, authorityFilename))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(after, before) {
		t.Fatal("oversized replacement changed the durable snapshot")
	}
	policy, err := authority.Policy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(policy) != 1 || policy[0].NamespaceID != first.NamespaceID {
		t.Fatalf("oversized replacement changed published policy: %+v", policy)
	}
}

func TestHostAuthorityAdoptsRenamedIdentityAndLatchesDebt(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	installation := authority.Installation()
	first, err := authority.PrepareRoot(t.Context(), RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID})
	if err != nil {
		t.Fatal(err)
	}
	fault := true
	authority.hooks.SyncDir = func(directory *os.File) error {
		if fault {
			fault = false
			return errors.New("directory sync barrier")
		}
		return directory.Sync()
	}
	secondRequest := RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID}
	if _, err := authority.PrepareRoot(t.Context(), secondRequest); err == nil {
		t.Fatal("post-rename directory failure was hidden")
	}
	if _, err := authority.Policy(t.Context()); !errors.Is(err, ErrDurabilityDebt) {
		t.Fatalf("policy did not fail closed on durability debt: %v", err)
	}
	_ = authority.Close()

	reopened, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, err := reopened.PrepareRoot(t.Context(), RootRequest{SessionID: first.SessionID, ProjectID: first.ProjectID, RealmID: first.RealmID, HumanOwnerID: first.HumanOwnerID})
	if err != nil {
		t.Fatal(err)
	}
	if got != first {
		t.Fatalf("renamed snapshot lost prior identity: got=%+v want=%+v", got, first)
	}
	newAssociation, err := reopened.PrepareRoot(t.Context(), secondRequest)
	if err != nil {
		t.Fatal(err)
	}
	if newAssociation.NamespaceID == first.NamespaceID {
		t.Fatal("reopened snapshot lost adopted candidate")
	}
}

func TestHostAuthorityFirstCreateSyncDebtRetainsCandidate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	syncs := 0
	authority, err := openHostAuthority(root, authorityHooks{
		SyncDir: func(directory *os.File) error {
			syncs++
			if syncs == 3 {
				return errors.New("initial directory sync barrier")
			}
			return directory.Sync()
		},
	})
	if err == nil {
		t.Fatal("initial post-rename directory failure was hidden")
	}
	if authority == nil {
		t.Fatal("initial sync failure did not retain the opened authority")
	}
	if _, policyErr := authority.Policy(t.Context()); !errors.Is(policyErr, ErrDurabilityDebt) {
		t.Fatalf("initial post-rename failure did not latch debt: %v", policyErr)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	installation := reopened.Installation()
	if installation.RealmID == "" || installation.HumanOwnerID == "" {
		t.Fatal("reopened initial candidate lost installation identity")
	}
	request := RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID}
	if _, err := reopened.PrepareRoot(t.Context(), request); err != nil {
		t.Fatal(err)
	}
}

func TestHostAuthorityFirstCreateSyncsCreatedParentBeforeLeaf(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	parent := filepath.Dir(root)
	parent, err := filepath.EvalSymlinks(parent)
	if err != nil {
		t.Fatal(err)
	}
	parentFault := true
	if _, err := openHostAuthority(root, authorityHooks{
		SyncDir: func(directory *os.File) error {
			if filepath.Clean(directory.Name()) == filepath.Clean(parent) && parentFault {
				parentFault = false
				return errors.New("created parent directory barrier")
			}
			return directory.Sync()
		},
	}); err == nil {
		t.Fatal("created parent directory sync failure was hidden")
	}
	if _, err := os.Stat(filepath.Join(root, authorityFilename)); !os.IsNotExist(err) {
		t.Fatalf("authority file appeared after parent durability failure: %v", err)
	}
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if authority.Installation().RealmID == "" {
		t.Fatal("authority did not recover after parent durability barrier")
	}
}

func TestHostAuthorityRetriesNestedParentDurabilityObligation(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "missing", "inner", "artifacts")
	resolvedBase, err := filepath.EvalSymlinks(base)
	if err != nil {
		t.Fatal(err)
	}
	ancestorParent := filepath.Join(resolvedBase, "missing")
	parentFault := true
	syncDir := func(directory *os.File) error {
		if parentFault && filepath.Clean(directory.Name()) == filepath.Clean(ancestorParent) {
			return errors.New("nested ancestor directory barrier")
		}
		return directory.Sync()
	}
	if _, err := openHostAuthority(root, authorityHooks{SyncDir: syncDir}); err == nil {
		t.Fatal("initial nested parent directory sync failure was hidden")
	}
	if _, err := openHostAuthority(root, authorityHooks{SyncDir: syncDir}); err == nil {
		t.Fatal("retry acknowledged authority before nested parent durability barrier was released")
	}
	if _, err := os.Stat(filepath.Join(root, authorityFilename)); !os.IsNotExist(err) {
		t.Fatalf("authority file appeared before nested parent durability barrier was released: %v", err)
	}
	parentFault = false
	first, err := openHostAuthority(root, authorityHooks{SyncDir: syncDir})
	if err != nil {
		t.Fatal(err)
	}
	installation := first.Installation()
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	if got := reopened.Installation(); got != installation {
		t.Fatalf("installation changed after nested parent durability recovery: got=%+v want=%+v", got, installation)
	}
}

func TestHostAuthorityFirstCreateFileSyncFailureLeavesNoCandidate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	if _, err := openHostAuthority(root, authorityHooks{SyncFile: func(*os.File) error {
		return errors.New("initial file sync barrier")
	}}); err == nil {
		t.Fatal("initial file sync failure was hidden")
	}
	if _, err := os.Stat(filepath.Join(root, authorityFilename)); !os.IsNotExist(err) {
		t.Fatalf("authority file appeared after initial file sync failure: %v", err)
	}
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if authority.Installation().RealmID == "" {
		t.Fatal("authority did not recover after initial file sync barrier")
	}
}

func TestHostAuthorityRejectsMutationsAcrossDebtAndClose(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	installation := authority.Installation()
	rootRequest := RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID}
	association, err := authority.PrepareRoot(t.Context(), rootRequest)
	if err != nil {
		t.Fatal(err)
	}
	fault := true
	authority.hooks.SyncDir = func(directory *os.File) error {
		if fault {
			fault = false
			return errors.New("debt barrier")
		}
		return directory.Sync()
	}
	if _, err := authority.PrepareRoot(t.Context(), RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: rootRequest.ProjectID, RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID}); err == nil {
		t.Fatal("debt mutation was accepted")
	}
	if _, err := authority.BeginSessionDeletion(t.Context(), association.SessionID); !errors.Is(err, ErrDurabilityDebt) {
		t.Fatalf("debt deletion returned %v, want durability debt", err)
	}
	if err := authority.ReconcileDurability(t.Context()); err != nil {
		t.Fatal(err)
	}
	intent, err := authority.BeginSessionDeletion(t.Context(), association.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	if err := authority.ReconcileDeletion(t.Context(), intent); err == nil {
		t.Fatal("closed authority accepted deletion reconciliation")
	}
	if err := authority.ReconcileDurability(t.Context()); err == nil {
		t.Fatal("closed authority accepted durability reconciliation")
	}
}

func TestHostAuthorityReplacementFailuresKeepPreviousSnapshot(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	installation := authority.Installation()
	firstRequest := RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID}
	first, err := authority.PrepareRoot(t.Context(), firstRequest)
	if err != nil {
		t.Fatal(err)
	}
	secondRequest := RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: first.ProjectID, RealmID: first.RealmID, HumanOwnerID: first.HumanOwnerID}
	for name, configure := range map[string]func(*HostAuthority){
		"file-sync": func(authority *HostAuthority) {
			authority.hooks.SyncFile = func(*os.File) error { return errors.New("file sync barrier") }
		},
		"before-rename": func(authority *HostAuthority) {
			authority.hooks.BeforeRename = func() error { return errors.New("before rename barrier") }
		},
	} {
		t.Run(name, func(t *testing.T) {
			configure(authority)
			if _, err := authority.PrepareRoot(t.Context(), secondRequest); err == nil {
				t.Fatal("replacement failure was hidden")
			}
			policies, err := authority.Policy(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(policies) != 1 || policies[0].NamespaceID != first.NamespaceID {
				t.Fatalf("replacement failure changed published policy: %+v", policies)
			}
			authority.hooks.SyncFile = nil
			authority.hooks.BeforeRename = nil
		})
	}
	if _, err := authority.PrepareRoot(t.Context(), secondRequest); err != nil {
		t.Fatalf("replacement did not remain available after clearing fault: %v", err)
	}
}

func TestHostAuthorityReplaysProjectDeletionWithoutPastRows(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	installation := authority.Installation()
	rootRequest := RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID}
	rootAssociation, err := authority.PrepareRoot(t.Context(), rootRequest)
	if err != nil {
		t.Fatal(err)
	}
	child, err := authority.EnsureSession(t.Context(), SessionRequest{SessionID: identifier.MustNewSessionID(), RootSessionID: rootAssociation.SessionID, ProjectID: rootAssociation.ProjectID, RealmID: rootAssociation.RealmID, HumanOwnerID: rootAssociation.HumanOwnerID})
	if err != nil {
		t.Fatal(err)
	}
	// A crash after the child intent commit is represented by reopening before
	// the remaining project targets are imported. The missing Past target is
	// accepted while the retained authority association remains authoritative.
	if err := authority.ImportProjectDeletion(t.Context(), rootAssociation.ProjectID, []string{child.SessionID, identifier.MustNewSessionID()}); err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	authority, err = OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer authority.Close()
	if err := authority.ImportProjectDeletion(t.Context(), rootAssociation.ProjectID, []string{rootAssociation.SessionID}); err != nil {
		t.Fatal(err)
	}
	policy, err := authority.Policy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(policy) != 1 || !policy[0].Tombstone {
		t.Fatalf("replayed root project deletion did not tombstone namespace: %+v", policy)
	}
	if _, err := authority.EnsureSession(t.Context(), SessionRequest{SessionID: child.SessionID, RootSessionID: child.RootSessionID, ProjectID: child.ProjectID, RealmID: child.RealmID, HumanOwnerID: child.HumanOwnerID}); !errors.Is(err, ErrDeleted) {
		t.Fatalf("child association revived after project replay: %v", err)
	}
}

func TestHostAuthorityPolicyDrivesRealStoreTombstone(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	installation := authority.Installation()
	association, err := authority.PrepareRoot(t.Context(), RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID})
	if err != nil {
		t.Fatal(err)
	}
	supervisor := processSupervisor(t, root, authority.Policy)
	scope := testScope()
	scope.RealmID = association.RealmID
	scope.PrincipalID = association.PrincipalID
	scope.NamespaceID = association.NamespaceID
	if _, err := supervisor.Grant(t.Context(), scope); err != nil {
		t.Fatal(err)
	}
	intent, err := authority.BeginSessionDeletion(t.Context(), association.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authority.Policy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if err := supervisor.ApplyNamespace(t.Context(), policy[0]); err != nil {
		t.Fatal(err)
	}
	_, err = supervisor.Grant(t.Context(), scope)
	requireCode(t, err, NotFoundOrForbidden)
	if err := authority.ReconcileDeletion(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := processSupervisor(t, root, reopened.Policy)
	_, err = restarted.Grant(t.Context(), scope)
	requireCode(t, err, NotFoundOrForbidden)
}

func TestHostAuthorityTombstoneLostAcknowledgmentReplaysAfterOwnedRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	installation := authority.Installation()
	association, err := authority.PrepareRoot(t.Context(), RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID})
	if err != nil {
		t.Fatal(err)
	}
	eventRead, eventWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	resumeRead, resumeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = eventRead.Close()
		_ = eventWrite.Close()
		_ = resumeRead.Close()
		_ = resumeWrite.Close()
	})
	waitStarted := make(chan struct{})
	s := NewSupervisor(root, SupervisorOptions{
		Policy:     authority.Policy,
		extraFiles: []*os.File{eventWrite, resumeRead},
		command:    []string{os.Args[0], "-test.run=^TestArtifactServiceProcess$", "--", "artifact-process-namespace-barrier"},
		Wait: func(ctx context.Context, _ time.Duration) error {
			close(waitStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	if _, err := s.Grant(t.Context(), testScopeForAssociation(association)); err != nil {
		t.Fatal(err)
	}
	intent, err := authority.BeginSessionDeletion(t.Context(), association.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := authority.Policy(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	applyDone := make(chan error, 1)
	go func() { applyDone <- s.ApplyNamespace(t.Context(), policy[0]) }()
	var reached [1]byte
	if _, err := io.ReadFull(eventRead, reached[:]); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	child := s.child
	s.mu.Unlock()
	if child == nil {
		t.Fatal("tombstone barrier reached without an owned child")
	}
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-applyDone; err == nil {
		t.Fatal("lost tombstone acknowledgment was reported as success")
	}
	<-waitStarted
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := processSupervisor(t, root, reopened.Policy)
	scope := testScopeForAssociation(association)
	requireCode(t, func() error { _, err := restarted.Grant(t.Context(), scope); return err }(), NotFoundOrForbidden)
	if err := reopened.ReconcileDeletion(t.Context(), intent); err != nil {
		t.Fatal(err)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(root, "artifacts.sqlite"), StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var namespaces, tombstones int
	if err := store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM artifact_namespaces").Scan(&namespaces); err != nil {
		t.Fatal(err)
	}
	if err := store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM artifact_namespaces WHERE tombstoned_at IS NOT NULL").Scan(&tombstones); err != nil {
		t.Fatal(err)
	}
	if namespaces != 1 || tombstones != 1 {
		t.Fatalf("lost tombstone acknowledgment created unexpected namespaces: total=%d tombstoned=%d", namespaces, tombstones)
	}
}

func TestHostAuthorityEnsureLostAcknowledgmentReplaysStableNamespaceAfterOwnedRestart(t *testing.T) {
	root := filepath.Join(t.TempDir(), "artifacts")
	authority, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	installation := authority.Installation()
	association, err := authority.PrepareRoot(t.Context(), RootRequest{SessionID: identifier.MustNewSessionID(), ProjectID: "project-0123456789", RealmID: installation.RealmID, HumanOwnerID: installation.HumanOwnerID})
	if err != nil {
		t.Fatal(err)
	}
	eventRead, eventWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	resumeRead, resumeWrite, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = eventRead.Close()
		_ = eventWrite.Close()
		_ = resumeRead.Close()
		_ = resumeWrite.Close()
	})
	waitStarted := make(chan struct{})
	s := NewSupervisor(root, SupervisorOptions{
		Policy:     func(context.Context) ([]NamespacePolicy, error) { return nil, nil },
		extraFiles: []*os.File{eventWrite, resumeRead},
		command:    []string{os.Args[0], "-test.run=^TestArtifactServiceProcess$", "--", "artifact-process-ensure-barrier"},
		Wait: func(ctx context.Context, _ time.Duration) error {
			close(waitStarted)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	readiness, err := s.Ensure(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	applyDone := make(chan error, 1)
	go func() {
		applyDone <- s.ApplyNamespace(t.Context(), NamespacePolicy{NamespaceID: association.NamespaceID, RealmID: association.RealmID, OwnerThreadID: association.SessionID})
	}()
	var reached [1]byte
	if _, err := io.ReadFull(eventRead, reached[:]); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	child := s.child
	s.mu.Unlock()
	if child == nil {
		t.Fatal("ensure barrier reached without an owned child")
	}
	if err := child.cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := <-applyDone; err == nil {
		t.Fatal("lost ensure acknowledgment was reported as success")
	}
	<-waitStarted
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if err := authority.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenHostAuthority(root)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	restarted := processSupervisor(t, root, reopened.Policy)
	grant, err := restarted.Grant(t.Context(), testScopeForAssociation(association))
	if err != nil {
		t.Fatal(err)
	}
	if grant.Readiness.ServiceID != readiness.ServiceID {
		t.Fatalf("restart changed durable service identity: first=%s restarted=%s", readiness.ServiceID, grant.Readiness.ServiceID)
	}
	if err := restarted.Close(); err != nil {
		t.Fatal(err)
	}
	store, err := OpenStore(filepath.Join(root, "artifacts.sqlite"), StoreOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	var namespaces int
	if err := store.db.QueryRowContext(t.Context(), "SELECT count(*) FROM artifact_namespaces").Scan(&namespaces); err != nil {
		t.Fatal(err)
	}
	if namespaces != 1 {
		t.Fatalf("lost ensure acknowledgment created %d namespaces", namespaces)
	}
}

func testScopeForAssociation(association RootAssociation) Scope {
	scope := testScope()
	scope.RealmID = association.RealmID
	scope.PrincipalID = association.PrincipalID
	scope.NamespaceID = association.NamespaceID
	return scope
}
