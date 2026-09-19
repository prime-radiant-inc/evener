package interactiveartifacts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"sync"
	"sync/atomic"

	"primeradiant.com/evener/identifier"
)

const (
	authoritySchemaVersion = 1
	authorityFilename      = "authority.json"
	authorityMaxBytes      = 1 << 20
)

var (
	ErrDeleted            = errors.New("artifact authority association deleted")
	ErrAssociationMissing = errors.New("artifact authority association missing")
	ErrDurabilityDebt     = errors.New("artifact authority durability debt")
)

// InstallationIdentity is the stable identity of one Hub installation.
// These values are durable authority inputs, never bearer credentials.
type InstallationIdentity struct {
	RealmID      string
	HumanOwnerID string
}

// RootRequest is trusted host lifecycle input. It is intentionally separate
// from Scope: daemon/model callers do not receive this constructor in 4B.1.
type RootRequest struct {
	SessionID    string
	ProjectID    string
	RealmID      string
	HumanOwnerID string
}

// SessionRequest describes a child session attached to an existing root.
type SessionRequest struct {
	SessionID     string
	RootSessionID string
	ProjectID     string
	RealmID       string
	HumanOwnerID  string
}

// RootAssociation is durable identity returned after a root association is
// committed. The NamespaceID remains stable for this root's lifetime.
type RootAssociation struct {
	SessionID     string
	RootSessionID string
	PrincipalID   string
	NamespaceID   string
	ProjectID     string
	RealmID       string
	HumanOwnerID  string
}

// SessionAssociation is durable identity for a child session. Child sessions
// share their root namespace while retaining their own stable principal.
type SessionAssociation struct {
	SessionID     string
	RootSessionID string
	PrincipalID   string
	NamespaceID   string
	ProjectID     string
	RealmID       string
	HumanOwnerID  string
}

// DeletionIntent is a durable, retryable lifecycle embargo. Completed intents
// retain content-free identity so a deleted session cannot be resurrected.
type DeletionIntent struct {
	SessionID     string
	RootSessionID string
	PrincipalID   string
	NamespaceID   string
	ProjectID     string
	Root          bool
	Completed     bool
}

type authorityInstallation struct {
	RealmID      string `json:"realmId"`
	HumanOwnerID string `json:"humanOwnerId"`
}
type authoritySession struct {
	SessionID     string `json:"sessionId"`
	RootSessionID string `json:"rootSessionId"`
	PrincipalID   string `json:"principalId"`
	NamespaceID   string `json:"namespaceId"`
	ProjectID     string `json:"projectId"`
	RealmID       string `json:"realmId"`
	HumanOwnerID  string `json:"humanOwnerId"`
}
type authorityDeletion struct {
	SessionID     string `json:"sessionId"`
	RootSessionID string `json:"rootSessionId"`
	PrincipalID   string `json:"principalId"`
	NamespaceID   string `json:"namespaceId"`
	ProjectID     string `json:"projectId"`
	Root          bool   `json:"root"`
	Completed     bool   `json:"completed"`
}
type authoritySnapshot struct {
	Version      int                   `json:"version"`
	Installation authorityInstallation `json:"installation"`
	Sessions     []authoritySession    `json:"sessions"`
	Deletions    []authorityDeletion   `json:"deletions"`
}

type authorityHooks struct {
	BeforeRename func() error
	AfterRename  func() error
	SyncFile     func(*os.File) error
	SyncDir      func(*os.File) error
}

type authorityPolicy struct {
	policies []NamespacePolicy
}

// HostAuthority owns the private, durable source of artifact namespace
// authority. The caller must hold the installation Hub lock for its lifetime.
type HostAuthority struct {
	mu        sync.Mutex
	directory string
	path      string
	snapshot  authoritySnapshot
	hooks     authorityHooks
	closed    bool
	debt      atomic.Bool
	policy    atomic.Value // authorityPolicy; immutable copies only
}

// OpenHostAuthority opens or creates one strict private authority snapshot.
// The caller must acquire the production Hub single-owner lock first and hold
// it until Close returns.
func OpenHostAuthority(privateDir string) (*HostAuthority, error) {
	return openHostAuthority(privateDir, authorityHooks{})
}

func openHostAuthority(privateDir string, hooks authorityHooks) (*HostAuthority, error) {
	directory, _, err := prepareStoreDirectory(privateDir)
	if err != nil {
		return nil, err
	}
	authorityPath := filepath.Join(directory, authorityFilename)
	if _, err := os.Lstat(authorityPath); errors.Is(err, os.ErrNotExist) {
		if err := syncAuthorityParentChain(directory, hooks); err != nil {
			return nil, fmt.Errorf("sync artifact authority parent directory: %w", err)
		}
	}
	if err := syncDirectory(directory, hooks); err != nil {
		return nil, fmt.Errorf("sync artifact authority directory: %w", err)
	}
	authority := &HostAuthority{directory: directory, path: authorityPath, hooks: hooks}
	authority.policy.Store(authorityPolicy{})
	data, err := readAuthority(authority.path)
	if errors.Is(err, os.ErrNotExist) {
		authority.snapshot = authoritySnapshot{
			Version:      authoritySchemaVersion,
			Installation: authorityInstallation{RealmID: randomID(), HumanOwnerID: randomID()},
			Sessions:     []authoritySession{},
			Deletions:    []authorityDeletion{},
		}
		if err := authority.commitLocked(authority.snapshot); err != nil {
			return authority, err
		}
		return authority, nil
	} else if err != nil {
		return nil, fmt.Errorf("read artifact authority: %w", err)
	}
	snapshot, err := decodeAuthority(data)
	if err != nil {
		return nil, err
	}
	authority.snapshot = snapshot
	authority.publishLocked()
	return authority, nil
}

func readAuthority(path string) ([]byte, error) {
	if err := requirePrivatePath(path, false); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, err
		}
		return nil, fmt.Errorf("validate artifact authority: %w", err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(file, authorityMaxBytes+1))
	return data, errors.Join(readErr, file.Close())
}

func decodeAuthority(data []byte) (authoritySnapshot, error) {
	if len(data) > authorityMaxBytes {
		return authoritySnapshot{}, errors.New("artifact authority snapshot exceeds limit")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var snapshot authoritySnapshot
	if err := decoder.Decode(&snapshot); err != nil {
		return authoritySnapshot{}, fmt.Errorf("decode artifact authority: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return authoritySnapshot{}, errors.New("decode artifact authority: trailing JSON value")
		}
		return authoritySnapshot{}, fmt.Errorf("decode artifact authority trailing data: %w", err)
	}
	if err := validateAuthority(snapshot); err != nil {
		return authoritySnapshot{}, err
	}
	return snapshot, nil
}

func validateAuthority(snapshot authoritySnapshot) error {
	if snapshot.Version != authoritySchemaVersion {
		return fmt.Errorf("unsupported artifact authority schema version %d", snapshot.Version)
	}
	if snapshot.Installation.RealmID == "" || snapshot.Installation.HumanOwnerID == "" {
		return errors.New("artifact authority installation identity is incomplete")
	}
	seen := make(map[string]struct{}, len(snapshot.Sessions))
	principals := make(map[string]string, len(snapshot.Sessions))
	sessions := make(map[string]authoritySession, len(snapshot.Sessions))
	roots := make(map[string]authoritySession, len(snapshot.Sessions))
	rootNamespaces := make(map[string]string, len(snapshot.Sessions))
	for _, session := range snapshot.Sessions {
		if err := validateSessionAssociation(session); err != nil {
			return err
		}
		if _, ok := seen[session.SessionID]; ok {
			return fmt.Errorf("duplicate artifact authority session %q", session.SessionID)
		}
		if prior, ok := principals[session.PrincipalID]; ok {
			return fmt.Errorf("artifact authority sessions %q and %q share principal %q", prior, session.SessionID, session.PrincipalID)
		}
		seen[session.SessionID] = struct{}{}
		principals[session.PrincipalID] = session.SessionID
		sessions[session.SessionID] = session
		if session.RootSessionID == session.SessionID {
			if session.RealmID != snapshot.Installation.RealmID || session.HumanOwnerID != snapshot.Installation.HumanOwnerID {
				return fmt.Errorf("artifact authority root %q does not match installation", session.SessionID)
			}
			if prior, ok := rootNamespaces[session.NamespaceID]; ok {
				return fmt.Errorf("artifact authority roots %q and %q share namespace %q", prior, session.SessionID, session.NamespaceID)
			}
			rootNamespaces[session.NamespaceID] = session.SessionID
			roots[session.SessionID] = session
		}
	}
	for _, session := range snapshot.Sessions {
		root, ok := roots[session.RootSessionID]
		if !ok || root.NamespaceID != session.NamespaceID || root.RealmID != session.RealmID || root.HumanOwnerID != session.HumanOwnerID || root.ProjectID != session.ProjectID {
			return fmt.Errorf("artifact authority session %q has invalid root relationship", session.SessionID)
		}
	}
	deletions := make(map[string]struct{}, len(snapshot.Deletions))
	for _, deletion := range snapshot.Deletions {
		if deletion.SessionID == "" || deletion.RootSessionID == "" || deletion.PrincipalID == "" || deletion.NamespaceID == "" || deletion.ProjectID == "" {
			return errors.New("incomplete artifact authority deletion")
		}
		if _, ok := seen[deletion.SessionID]; !ok {
			return fmt.Errorf("artifact authority deletion has unknown session %q", deletion.SessionID)
		}
		if _, ok := deletions[deletion.SessionID]; ok {
			return fmt.Errorf("duplicate artifact authority deletion %q", deletion.SessionID)
		}
		deletions[deletion.SessionID] = struct{}{}
		session := sessions[deletion.SessionID]
		if deletion.Root != (session.RootSessionID == session.SessionID) || deletion.RootSessionID != session.RootSessionID || deletion.PrincipalID != session.PrincipalID || deletion.NamespaceID != session.NamespaceID || deletion.ProjectID != session.ProjectID {
			return fmt.Errorf("artifact authority deletion %q does not match session", deletion.SessionID)
		}
	}
	return nil
}

func validateSessionAssociation(session authoritySession) error {
	for name, value := range map[string]string{"sessionId": session.SessionID, "rootSessionId": session.RootSessionID, "principalId": session.PrincipalID, "namespaceId": session.NamespaceID, "realmId": session.RealmID, "humanOwnerId": session.HumanOwnerID} {
		if value == "" {
			return fmt.Errorf("artifact authority session %s is empty", name)
		}
	}
	if err := identifier.ValidateSessionID(session.SessionID); err != nil {
		return fmt.Errorf("invalid artifact authority session ID: %w", err)
	}
	if err := identifier.ValidateSessionID(session.RootSessionID); err != nil {
		return fmt.Errorf("invalid artifact authority root session ID: %w", err)
	}
	if err := identifier.ValidateProjectID(session.ProjectID); err != nil {
		return fmt.Errorf("invalid artifact authority project ID: %w", err)
	}
	return nil
}

func (a *HostAuthority) Installation() InstallationIdentity {
	a.mu.Lock()
	defer a.mu.Unlock()
	return InstallationIdentity{RealmID: a.snapshot.Installation.RealmID, HumanOwnerID: a.snapshot.Installation.HumanOwnerID}
}

// Policy returns a copy of the last durably published namespace policy. It
// takes no authority lock so Store Ensure can call back into Policy safely.
func (a *HostAuthority) Policy(context.Context) ([]NamespacePolicy, error) {
	if a.debt.Load() {
		return nil, ErrDurabilityDebt
	}
	value := a.policy.Load().(authorityPolicy)
	return slices.Clone(value.policies), nil
}

// PrepareRoot idempotently persists the association for one root session.
func (a *HostAuthority) PrepareRoot(ctx context.Context, request RootRequest) (RootAssociation, error) {
	if err := ctx.Err(); err != nil {
		return RootAssociation{}, err
	}
	if err := validateRootRequest(request); err != nil {
		return RootAssociation{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return RootAssociation{}, err
	}
	if request.RealmID != a.snapshot.Installation.RealmID || request.HumanOwnerID != a.snapshot.Installation.HumanOwnerID {
		return RootAssociation{}, errors.New("artifact authority installation identity changed")
	}
	for _, session := range a.snapshot.Sessions {
		if session.SessionID != request.SessionID {
			continue
		}
		if session.RootSessionID != session.SessionID || session.RealmID != request.RealmID || session.HumanOwnerID != request.HumanOwnerID || session.ProjectID != request.ProjectID {
			return RootAssociation{}, errors.New("artifact authority root association changed")
		}
		if a.isDeletedLocked(session.SessionID) {
			return RootAssociation{}, fmt.Errorf("%w: root %s", ErrDeleted, session.SessionID)
		}
		return rootAssociation(session), nil
	}
	next := a.snapshot
	next.Sessions = append(slices.Clone(next.Sessions), authoritySession{
		SessionID: request.SessionID, RootSessionID: request.SessionID, PrincipalID: randomID(), NamespaceID: randomID(), ProjectID: request.ProjectID, RealmID: request.RealmID, HumanOwnerID: request.HumanOwnerID,
	})
	if err := a.commitLocked(next); err != nil {
		return RootAssociation{}, err
	}
	return rootAssociation(next.Sessions[len(next.Sessions)-1]), nil
}

// EnsureSession persists one child association under an existing root.
func (a *HostAuthority) EnsureSession(ctx context.Context, request SessionRequest) (SessionAssociation, error) {
	if err := ctx.Err(); err != nil {
		return SessionAssociation{}, err
	}
	if err := validateSessionRequest(request); err != nil {
		return SessionAssociation{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return SessionAssociation{}, err
	}
	if request.RealmID != a.snapshot.Installation.RealmID || request.HumanOwnerID != a.snapshot.Installation.HumanOwnerID {
		return SessionAssociation{}, errors.New("artifact authority installation identity changed")
	}
	var root *authoritySession
	for i := range a.snapshot.Sessions {
		if a.snapshot.Sessions[i].SessionID == request.RootSessionID && a.snapshot.Sessions[i].RootSessionID == request.RootSessionID {
			root = &a.snapshot.Sessions[i]
		}
	}
	if root == nil {
		return SessionAssociation{}, ErrAssociationMissing
	}
	if root.RealmID != request.RealmID || root.HumanOwnerID != request.HumanOwnerID || root.ProjectID != request.ProjectID || a.isDeletedLocked(root.SessionID) {
		return SessionAssociation{}, fmt.Errorf("%w: root %s", ErrDeleted, root.SessionID)
	}
	for _, session := range a.snapshot.Sessions {
		if session.SessionID == request.SessionID {
			if session.RootSessionID != request.RootSessionID || session.RealmID != request.RealmID || session.HumanOwnerID != request.HumanOwnerID || session.ProjectID != request.ProjectID {
				return SessionAssociation{}, errors.New("artifact authority session association changed")
			}
			if a.isDeletedLocked(session.SessionID) {
				return SessionAssociation{}, fmt.Errorf("%w: session %s", ErrDeleted, session.SessionID)
			}
			return sessionAssociation(session), nil
		}
	}
	session := authoritySession{SessionID: request.SessionID, RootSessionID: request.RootSessionID, PrincipalID: randomID(), NamespaceID: root.NamespaceID, ProjectID: request.ProjectID, RealmID: request.RealmID, HumanOwnerID: request.HumanOwnerID}
	next := a.snapshot
	next.Sessions = append(slices.Clone(next.Sessions), session)
	if err := a.commitLocked(next); err != nil {
		return SessionAssociation{}, err
	}
	return sessionAssociation(session), nil
}

// BeginSessionDeletion durably seals exactly one session. Root deletion emits
// a namespace tombstone; child deletion leaves the shared namespace live.
func (a *HostAuthority) BeginSessionDeletion(ctx context.Context, sessionID string) (DeletionIntent, error) {
	if err := ctx.Err(); err != nil {
		return DeletionIntent{}, err
	}
	if err := identifier.ValidateSessionID(sessionID); err != nil {
		return DeletionIntent{}, err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return DeletionIntent{}, err
	}
	for _, deletion := range a.snapshot.Deletions {
		if deletion.SessionID == sessionID {
			return deletionIntent(deletion), nil
		}
	}
	var session *authoritySession
	for i := range a.snapshot.Sessions {
		if a.snapshot.Sessions[i].SessionID == sessionID {
			session = &a.snapshot.Sessions[i]
			break
		}
	}
	if session == nil {
		return DeletionIntent{}, ErrAssociationMissing
	}
	deletion := authorityDeletion{SessionID: session.SessionID, RootSessionID: session.RootSessionID, PrincipalID: session.PrincipalID, NamespaceID: session.NamespaceID, ProjectID: session.ProjectID, Root: session.RootSessionID == session.SessionID}
	next := a.snapshot
	next.Deletions = append(slices.Clone(next.Deletions), deletion)
	if err := a.commitLocked(next); err != nil {
		return DeletionIntent{}, err
	}
	return deletionIntent(deletion), nil
}

// ImportProjectDeletion replays durable Hub project targets. Missing source
// Past rows are harmless: retained authority associations are still sealed.
func (a *HostAuthority) ImportProjectDeletion(ctx context.Context, projectID string, sessionIDs []string) error {
	if err := identifier.ValidateProjectID(projectID); err != nil {
		return err
	}
	for _, sessionID := range sessionIDs {
		if err := ctx.Err(); err != nil {
			return err
		}
		var session authoritySession
		a.mu.Lock()
		for _, candidate := range a.snapshot.Sessions {
			if candidate.SessionID == sessionID && candidate.ProjectID == projectID {
				session = candidate
				break
			}
		}
		a.mu.Unlock()
		if session.SessionID == "" {
			continue
		}
		if _, err := a.BeginSessionDeletion(ctx, sessionID); err != nil && !errors.Is(err, ErrDeleted) {
			return err
		}
	}
	return nil
}

// ReconcileDeletion marks a previously acknowledged deletion complete while
// retaining its content-free identity and root namespace tombstone.
func (a *HostAuthority) ReconcileDeletion(ctx context.Context, intent DeletionIntent) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.mutationReadyLocked(); err != nil {
		return err
	}
	for _, deletion := range a.snapshot.Deletions {
		if deletion.SessionID != intent.SessionID || deletion.NamespaceID != intent.NamespaceID || deletion.Root != intent.Root {
			continue
		}
		if deletion.Completed {
			return nil
		}
		next := a.snapshot
		next.Deletions = slices.Clone(next.Deletions)
		for i := range next.Deletions {
			if next.Deletions[i].SessionID == intent.SessionID {
				next.Deletions[i].Completed = true
			}
		}
		return a.commitLocked(next)
	}
	return ErrAssociationMissing
}

// ReconcileDurability clears a post-rename durability debt after explicitly
// syncing the adopted snapshot and its parent directory.
func (a *HostAuthority) ReconcileDurability(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if err := a.openLocked(); err != nil {
		return err
	}
	if !a.debt.Load() {
		return nil
	}
	file, err := os.OpenFile(a.path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	if err := syncFile(file, a.hooks); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := syncDirectory(a.directory, a.hooks); err != nil {
		return err
	}
	a.publishLocked()
	a.debt.Store(false)
	return nil
}

func (a *HostAuthority) Close() error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.closed = true
	return nil
}

func (a *HostAuthority) openLocked() error {
	if a.closed {
		return errors.New("artifact authority closed")
	}
	return nil
}

func (a *HostAuthority) mutationReadyLocked() error {
	if err := a.openLocked(); err != nil {
		return err
	}
	if a.debt.Load() {
		return ErrDurabilityDebt
	}
	return nil
}

func (a *HostAuthority) isDeletedLocked(sessionID string) bool {
	for _, deletion := range a.snapshot.Deletions {
		if deletion.SessionID == sessionID {
			return true
		}
	}
	return false
}

func (a *HostAuthority) publishLocked() {
	policies := make([]NamespacePolicy, 0)
	for _, session := range a.snapshot.Sessions {
		if session.RootSessionID != session.SessionID {
			continue
		}
		tombstone := false
		for _, deletion := range a.snapshot.Deletions {
			if deletion.RootSessionID == session.SessionID && deletion.Root {
				tombstone = true
				break
			}
		}
		policies = append(policies, NamespacePolicy{NamespaceID: session.NamespaceID, RealmID: session.RealmID, OwnerThreadID: session.SessionID, Tombstone: tombstone})
	}
	sort.SliceStable(policies, func(i, j int) bool {
		if policies[i].Tombstone != policies[j].Tombstone {
			return policies[i].Tombstone
		}
		return policies[i].NamespaceID < policies[j].NamespaceID
	})
	a.policy.Store(authorityPolicy{policies: policies})
}

func (a *HostAuthority) commitLocked(next authoritySnapshot) error {
	if err := validateAuthority(next); err != nil {
		return err
	}
	data, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if len(data) > authorityMaxBytes {
		return errors.New("artifact authority snapshot exceeds limit")
	}
	temp, err := os.CreateTemp(a.directory, ".authority-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() {
		_ = temp.Close()
		_ = os.Remove(tempPath)
	}()
	if err := temp.Chmod(0600); err != nil {
		return err
	}
	if _, err := temp.Write(data); err != nil {
		return err
	}
	if err := syncFile(temp, a.hooks); err != nil {
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	if a.hooks.BeforeRename != nil {
		if err := a.hooks.BeforeRename(); err != nil {
			return err
		}
	}
	if err := os.Rename(tempPath, a.path); err != nil {
		return err
	}
	a.snapshot = next
	if err := syncDirectory(a.directory, a.hooks); err != nil {
		a.debt.Store(true)
		return err
	}
	if a.hooks.AfterRename != nil {
		if err := a.hooks.AfterRename(); err != nil {
			a.debt.Store(true)
			return err
		}
	}
	a.debt.Store(false)
	a.publishLocked()
	return nil
}

func syncFile(file *os.File, hooks authorityHooks) error {
	if hooks.SyncFile != nil {
		return hooks.SyncFile(file)
	}
	return file.Sync()
}

func syncDirectory(path string, hooks authorityHooks) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	if hooks.SyncDir != nil {
		err = hooks.SyncDir(directory)
	} else {
		err = directory.Sync()
	}
	return errors.Join(err, directory.Close())
}

// syncAuthorityParentChain revalidates every parent entry when the authority
// file is not present. A prior failed open may have left created directories
// visible without proving their parent entries durable, so existence alone is
// not enough to narrow the next attempt to the leaf parent.
func syncAuthorityParentChain(directory string, hooks authorityHooks) error {
	parents := make([]string, 0, 8)
	for current := directory; ; current = filepath.Dir(current) {
		parent := filepath.Dir(current)
		parents = append(parents, parent)
		if parent == current {
			break
		}
	}
	for _, parent := range slices.Backward(parents) {
		if err := syncDirectory(parent, hooks); err != nil {
			return err
		}
	}
	return nil
}

func validateRootRequest(request RootRequest) error {
	if err := identifier.ValidateSessionID(request.SessionID); err != nil {
		return err
	}
	if err := identifier.ValidateProjectID(request.ProjectID); err != nil {
		return err
	}
	if request.RealmID == "" || request.HumanOwnerID == "" {
		return errors.New("artifact authority root identity is incomplete")
	}
	return nil
}

func validateSessionRequest(request SessionRequest) error {
	if err := identifier.ValidateSessionID(request.SessionID); err != nil {
		return err
	}
	if err := identifier.ValidateSessionID(request.RootSessionID); err != nil {
		return err
	}
	if err := identifier.ValidateProjectID(request.ProjectID); err != nil {
		return err
	}
	if request.RealmID == "" || request.HumanOwnerID == "" {
		return errors.New("artifact authority session identity is incomplete")
	}
	return nil
}

func rootAssociation(session authoritySession) RootAssociation {
	return RootAssociation(session)
}

func sessionAssociation(session authoritySession) SessionAssociation {
	return SessionAssociation(session)
}

func deletionIntent(deletion authorityDeletion) DeletionIntent {
	return DeletionIntent(deletion)
}
