package hubcore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/spf13/afero"
	"primeradiant.com/evener/appwire"
)

var (
	ErrVoiceBindingBusy       = errors.New("voice session already has an active binding")
	ErrVoiceConflict          = errors.New("voice record conflicts with immutable identity")
	ErrVoiceStale             = errors.New("voice binding is retired, expired or stale")
	ErrVoiceInvalid           = errors.New("invalid voice record")
	ErrVoiceCorrupt           = errors.New("corrupt voice journal record")
	ErrVoiceReconcileRequired = errors.New("voice attempt requires original-instance reconciliation")
)

type VoiceBinding struct {
	ID         string    `json:"id"`
	DeviceID   string    `json:"deviceId"`
	Ref        string    `json:"ref"`
	InstanceID string    `json:"instanceId"`
	Epoch      string    `json:"epoch"`
	GrantHash  string    `json:"grantHash"`
	Provider   string    `json:"provider"`
	Model      string    `json:"model"`
	Voice      string    `json:"voice"`
	ExpiresAt  time.Time `json:"expiresAt"`
	Active     bool      `json:"active"`
}

type VoiceTurn struct {
	ID                     string `json:"id"`
	BindingID              string `json:"bindingId"`
	Epoch                  string `json:"epoch"`
	Sequence               int64  `json:"sequence"`
	ProviderInputItemID    string `json:"providerInputItemId,omitempty"`
	State                  string `json:"state"`
	PresentationDeliveryID string `json:"presentationDeliveryId"`
}

const (
	VoiceTurnOpen        = "open"
	VoiceTurnCommitted   = "committed"
	VoiceTurnAborted     = "aborted"
	VoiceIntentPrepared  = "prepared"
	VoiceIntentUncertain = "uncertain"
	VoiceIntentAccepted  = "accepted"
)

type VoiceIntent struct {
	TurnID             string                   `json:"turnId"`
	BindingID          string                   `json:"bindingId"`
	Epoch              string                   `json:"epoch"`
	Ref                string                   `json:"ref"`
	ExpectedInstanceID string                   `json:"expectedInstanceId"`
	Method             string                   `json:"method"`
	Text               string                   `json:"text"`
	MutationID         string                   `json:"mutationId"`
	Params             json.RawMessage          `json:"params"`
	State              string                   `json:"state"`
	Receipt            *appwire.MutationReceipt `json:"receipt,omitempty"`
}

type VoiceJournal struct{ db *sql.DB }

type SpeechDelivery struct {
	ID                     string               `json:"id"`
	BindingID              string               `json:"bindingId"`
	Epoch                  string               `json:"epoch"`
	InstanceID             string               `json:"instanceId"`
	CanonicalSourceItemID  string               `json:"canonicalSourceItemId"`
	CanonicalText          string               `json:"canonicalText"`
	OriginatingRequestID   string               `json:"originatingRequestId"`
	FrontierID             string               `json:"frontierId"`
	RunGeneration          uint64               `json:"runGeneration"`
	PresentationGeneration uint64               `json:"presentationGeneration"`
	GeneratedTranscript    string               `json:"generatedTranscript"`
	Items                  []SpeechDeliveryItem `json:"items,omitempty"`
	OutputComplete         bool                 `json:"outputComplete"`
	Interrupted            bool                 `json:"interrupted"`
}

// Sample watermarks are cumulative in each provider item's 24 kHz timeline.
// Generated or queued samples are not evidence of rendering or human attention.
type SpeechDeliveryItem struct {
	ResponseID       string `json:"responseId"`
	ItemID           string `json:"itemId"`
	GeneratedSamples int64  `json:"generatedSamples"`
	QueuedSamples    int64  `json:"queuedSamples"`
	PlayedSamples    int64  `json:"playedSamples"`
	PlaybackComplete bool   `json:"playbackComplete"`
}

type SpeechDeliveryUpdate struct {
	Epoch                  string
	PresentationGeneration uint64
	Item                   *SpeechDeliveryItem
	GeneratedTranscript    *string
	OutputComplete         bool
	Interrupted            bool
}

func (j *VoiceJournal) CreateSpeechDelivery(ctx context.Context, delivery SpeechDelivery) error {
	if !validSpeechSource(delivery) || delivery.Interrupted || delivery.OutputComplete || delivery.GeneratedTranscript != "" || len(delivery.Items) != 0 {
		return ErrVoiceInvalid
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := readSpeechDelivery(ctx, tx, delivery.ID)
	if err == nil {
		existing.GeneratedTranscript, existing.Items, existing.Interrupted = "", nil, false
		existing.OutputComplete = false
		delivery.Items = nil
		if !reflect.DeepEqual(existing, delivery) {
			return ErrVoiceConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	b, err := activeVoiceBinding(ctx, tx, delivery.BindingID, delivery.Epoch)
	if err != nil {
		return err
	}
	if b.InstanceID != delivery.InstanceID {
		return ErrVoiceStale
	}
	generation := strconv.FormatUint(delivery.PresentationGeneration, 10)
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM voice_speech WHERE binding_id=? AND generation=?`, delivery.BindingID, generation).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrVoiceConflict
	}
	raw, err := json.Marshal(delivery)
	if err != nil {
		return ErrVoiceInvalid
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO voice_speech(id,binding_id,generation,interrupted,record) VALUES(?,?,?,0,?)`, delivery.ID, delivery.BindingID, generation, raw); err != nil {
		return err
	}
	return tx.Commit()
}

func (j *VoiceJournal) SpeechDelivery(ctx context.Context, id string) (SpeechDelivery, error) {
	return readSpeechDelivery(ctx, j.db, id)
}

// MaxSpeechGeneration includes interrupted deliveries. Their generations remain
// reserved across bridge and hub restarts, even when no audio reached the device.
func (j *VoiceJournal) MaxSpeechGeneration(ctx context.Context, bindingID, epoch string) (uint64, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := activeVoiceBinding(ctx, tx, bindingID, epoch); err != nil {
		return 0, err
	}
	var id string
	// Generations are canonical unsigned decimal strings, not signed SQLite
	// integers. Length then lexical order preserves their numeric ordering.
	err = tx.QueryRowContext(ctx, `SELECT id FROM voice_speech WHERE binding_id=? ORDER BY length(generation) DESC,generation DESC LIMIT 1`, bindingID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	delivery, err := readSpeechDelivery(ctx, tx, id)
	return delivery.PresentationGeneration, err
}

func validSpeechSource(d SpeechDelivery) bool {
	return d.ID != "" && d.BindingID != "" && d.Epoch != "" && d.InstanceID != "" && d.CanonicalSourceItemID != "" && strings.TrimSpace(d.CanonicalText) != "" && d.OriginatingRequestID != "" && d.FrontierID != ""
}

func validSpeechItem(item SpeechDeliveryItem) bool {
	return item.ResponseID != "" && item.ItemID != "" && item.PlayedSamples >= 0 && item.PlayedSamples <= item.QueuedSamples && item.QueuedSamples <= item.GeneratedSamples && (!item.PlaybackComplete || item.PlayedSamples == item.GeneratedSamples)
}

func readSpeechDelivery(ctx context.Context, q voiceQuerier, id string) (SpeechDelivery, error) {
	var raw []byte
	var bindingID, generation string
	var interrupted bool
	if err := q.QueryRowContext(ctx, `SELECT record,binding_id,generation,interrupted FROM voice_speech WHERE id=?`, id).Scan(&raw, &bindingID, &generation, &interrupted); err != nil {
		return SpeechDelivery{}, err
	}
	var d SpeechDelivery
	if json.Unmarshal(raw, &d) != nil || d.ID != id || d.BindingID != bindingID || strconv.FormatUint(d.PresentationGeneration, 10) != generation || !validSpeechSource(d) {
		return SpeechDelivery{}, ErrVoiceCorrupt
	}
	b, err := readVoiceBinding(ctx, q, bindingID)
	if err != nil {
		return SpeechDelivery{}, err
	}
	if d.Epoch != b.Epoch || d.InstanceID != b.InstanceID {
		return SpeechDelivery{}, ErrVoiceCorrupt
	}
	seen := make(map[string]bool, len(d.Items))
	for _, item := range d.Items {
		if !validSpeechItem(item) || seen[item.ItemID] {
			return SpeechDelivery{}, ErrVoiceCorrupt
		}
		seen[item.ItemID] = true
	}
	d.Interrupted = interrupted
	return d, nil
}

// Updates use cumulative values, so duplicate delivery of an acknowledgement is
// harmless. After interruption only already-generated text and played evidence
// may advance, never the generated or queued audio tail.
func (j *VoiceJournal) UpdateSpeechDelivery(ctx context.Context, id string, update SpeechDeliveryUpdate) (SpeechDelivery, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return SpeechDelivery{}, err
	}
	defer func() { _ = tx.Rollback() }()
	d, err := readSpeechDelivery(ctx, tx, id)
	if err != nil {
		return SpeechDelivery{}, err
	}
	if update.Epoch != d.Epoch || update.PresentationGeneration != d.PresentationGeneration {
		return SpeechDelivery{}, ErrVoiceStale
	}
	b, err := readVoiceBinding(ctx, tx, d.BindingID)
	if err != nil {
		return SpeechDelivery{}, err
	}
	stopped := d.Interrupted || !b.Active || !b.ExpiresAt.After(time.Now())
	changed := (!d.Interrupted && (stopped || update.Interrupted)) || (!d.OutputComplete && update.OutputComplete)
	if update.Item != nil {
		item := *update.Item
		if !validSpeechItem(item) {
			return SpeechDelivery{}, ErrVoiceConflict
		}
		index := -1
		for i, old := range d.Items {
			if old.ItemID == item.ItemID {
				index = i
				break
			}
		}
		if index < 0 {
			if stopped || d.OutputComplete {
				return SpeechDelivery{}, ErrVoiceStale
			}
			d.Items = append(d.Items, item)
			changed = true
		} else {
			old := d.Items[index]
			if old.ResponseID != item.ResponseID || item.GeneratedSamples < old.GeneratedSamples || item.QueuedSamples < old.QueuedSamples || item.PlayedSamples < old.PlayedSamples {
				return SpeechDelivery{}, ErrVoiceConflict
			}
			if (stopped || d.OutputComplete || old.PlaybackComplete) && (item.GeneratedSamples != old.GeneratedSamples || item.QueuedSamples != old.QueuedSamples) {
				return SpeechDelivery{}, ErrVoiceStale
			}
			item.PlaybackComplete = item.PlaybackComplete || old.PlaybackComplete
			changed = changed || item != old
			d.Items[index] = item
		}
	}
	if update.GeneratedTranscript != nil {
		if !strings.HasPrefix(*update.GeneratedTranscript, d.GeneratedTranscript) {
			return SpeechDelivery{}, ErrVoiceConflict
		}
		changed = changed || *update.GeneratedTranscript != d.GeneratedTranscript
		d.GeneratedTranscript = *update.GeneratedTranscript
	}
	d.OutputComplete = d.OutputComplete || update.OutputComplete
	d.Interrupted = stopped || update.Interrupted
	if !changed {
		return d, nil
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return SpeechDelivery{}, ErrVoiceInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE voice_speech SET record=?,interrupted=? WHERE id=?`, raw, d.Interrupted, id); err != nil {
		return SpeechDelivery{}, err
	}
	if err := tx.Commit(); err != nil {
		return SpeechDelivery{}, err
	}
	return d, nil
}

func NewVoiceJournal(root string) (*VoiceJournal, error) {
	if root == "" {
		return nil, ErrVoiceInvalid
	}
	fs := afero.NewOsFs()
	dir := filepath.Join(root, "voice")
	base, err := existingRecoveryDirectory(fs, dir)
	if err != nil {
		return nil, err
	}
	if err := createRecoveryDirectory(fs, dir, base); err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "journal.db")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", sqliteDSNImmediate(path)+"&_pragma=synchronous(FULL)&_pragma=foreign_keys(ON)")
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)
	_, err = db.ExecContext(context.Background(), `CREATE TABLE IF NOT EXISTS voice_bindings (
 id TEXT PRIMARY KEY, ref TEXT NOT NULL, active INTEGER NOT NULL, record BLOB NOT NULL);
 CREATE UNIQUE INDEX IF NOT EXISTS voice_binding_owner ON voice_bindings(ref) WHERE active = 1;
 CREATE TABLE IF NOT EXISTS voice_turns (
 id TEXT PRIMARY KEY, binding_id TEXT NOT NULL REFERENCES voice_bindings(id) ON DELETE CASCADE,
 epoch TEXT NOT NULL, sequence INTEGER NOT NULL, record BLOB NOT NULL,
 UNIQUE(binding_id,epoch,sequence));
 CREATE TABLE IF NOT EXISTS voice_intents (
 turn_id TEXT PRIMARY KEY REFERENCES voice_turns(id) ON DELETE CASCADE,
 binding_id TEXT NOT NULL REFERENCES voice_bindings(id) ON DELETE CASCADE,
 mutation_id TEXT NOT NULL UNIQUE, record BLOB NOT NULL);
 CREATE INDEX IF NOT EXISTS voice_intents_binding ON voice_intents(binding_id,turn_id);
 CREATE TABLE IF NOT EXISTS voice_speech (
 id TEXT PRIMARY KEY, binding_id TEXT NOT NULL REFERENCES voice_bindings(id) ON DELETE CASCADE,
 generation TEXT NOT NULL, interrupted INTEGER NOT NULL, record BLOB NOT NULL,
 UNIQUE(binding_id,generation));`)
	if err == nil {
		err = syncRecoveryDirectory(fs, dir)
	}
	if err != nil {
		return nil, errors.Join(err, db.Close())
	}
	return &VoiceJournal{db: db}, nil
}

func (j *VoiceJournal) Close() error { return j.db.Close() }

// Recovery cursors are insertion rowids scoped to one binding, not random IDs.
// An empty page returns the input cursor. Reads never dispatch or change custody.
func (j *VoiceJournal) ListIntents(ctx context.Context, bindingID string, after int64, limit int) ([]VoiceIntent, int64, error) {
	if after < 0 || limit < 1 || limit > 200 {
		return nil, after, ErrVoiceInvalid
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, after, err
	}
	defer func() { _ = tx.Rollback() }()
	ids, cursor, err := voicePageIDs(ctx, tx, `SELECT turn_id,rowid FROM voice_intents WHERE binding_id=? AND rowid>? ORDER BY rowid LIMIT ?`, bindingID, after, limit)
	if err != nil {
		return nil, after, err
	}
	result := make([]VoiceIntent, 0, len(ids))
	for _, id := range ids {
		in, err := readVoiceIntent(ctx, tx, id)
		if err != nil {
			return nil, after, err
		}
		result = append(result, in)
	}
	return result, cursor, nil
}

func (j *VoiceJournal) ListSpeechDeliveries(ctx context.Context, bindingID string, after int64, limit int) ([]SpeechDelivery, int64, error) {
	if after < 0 || limit < 1 || limit > 200 {
		return nil, after, ErrVoiceInvalid
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, after, err
	}
	defer func() { _ = tx.Rollback() }()
	ids, cursor, err := voicePageIDs(ctx, tx, `SELECT id,rowid FROM voice_speech WHERE binding_id=? AND rowid>? ORDER BY rowid LIMIT ?`, bindingID, after, limit)
	if err != nil {
		return nil, after, err
	}
	result := make([]SpeechDelivery, 0, len(ids))
	for _, id := range ids {
		d, err := readSpeechDelivery(ctx, tx, id)
		if err != nil {
			return nil, after, err
		}
		result = append(result, d)
	}
	return result, cursor, nil
}

func voicePageIDs(ctx context.Context, tx *sql.Tx, query, bindingID string, after int64, limit int) ([]string, int64, error) {
	rows, err := tx.QueryContext(ctx, query, bindingID, after, limit)
	if err != nil {
		return nil, after, err
	}
	defer func() { _ = rows.Close() }()
	ids := make([]string, 0, limit)
	cursor := after
	for rows.Next() {
		var id string
		if err := rows.Scan(&id, &cursor); err != nil {
			return nil, after, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, after, err
	}
	return ids, cursor, nil
}

// DeleteSession is called only after authoritative deletion of the owning session.
// Disarm, expiry and socket loss must never call it.
func (j *VoiceJournal) DeleteSession(ctx context.Context, ref string) error {
	parsed, err := appwire.ParseRef(ref)
	if err != nil || parsed.String() != ref {
		return ErrVoiceInvalid
	}
	_, err = j.db.ExecContext(ctx, `DELETE FROM voice_bindings WHERE ref=?`, ref)
	return err
}

type voiceQuerier interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func validVoiceBinding(b VoiceBinding) bool {
	ref, err := appwire.ParseRef(b.Ref)
	return err == nil && ref.String() == b.Ref && b.ID != "" && b.InstanceID != "" && b.Epoch != ""
}

func readVoiceBinding(ctx context.Context, q voiceQuerier, id string) (VoiceBinding, error) {
	var raw []byte
	var active bool
	var ref string
	if err := q.QueryRowContext(ctx, `SELECT record,active,ref FROM voice_bindings WHERE id=?`, id).Scan(&raw, &active, &ref); err != nil {
		return VoiceBinding{}, err
	}
	var b VoiceBinding
	if json.Unmarshal(raw, &b) != nil || b.ID != id || b.Ref != ref || !validVoiceBinding(b) {
		return VoiceBinding{}, ErrVoiceCorrupt
	}
	b.Active = active
	return b, nil
}

func (j *VoiceJournal) Binding(ctx context.Context, id string) (VoiceBinding, error) {
	return readVoiceBinding(ctx, j.db, id)
}

func (j *VoiceJournal) CreateBinding(ctx context.Context, b VoiceBinding, takeover bool) error {
	if !validVoiceBinding(b) {
		return ErrVoiceInvalid
	}
	record, err := json.Marshal(b)
	if err != nil {
		return ErrVoiceInvalid
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var current string
	err = tx.QueryRowContext(ctx, `SELECT id FROM voice_bindings WHERE ref=? AND active=1`, b.Ref).Scan(&current)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if current != "" && b.Active {
		owner, err := readVoiceBinding(ctx, tx, current)
		if err != nil {
			return err
		}
		now := time.Now()
		if !b.ExpiresAt.IsZero() && !b.ExpiresAt.After(now) {
			return ErrVoiceStale
		}
		// A missed expiry timer cannot reserve ownership indefinitely. Missing
		// expiry metadata is not evidence that an owner's grant has elapsed.
		expired := !owner.ExpiresAt.IsZero() && !owner.ExpiresAt.After(now)
		if !takeover && (!expired || !b.ExpiresAt.After(now)) {
			return ErrVoiceBindingBusy
		}
		if _, err := tx.ExecContext(ctx, `UPDATE voice_bindings SET active=0 WHERE id=?`, current); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE voice_speech SET interrupted=1 WHERE binding_id=?`, current); err != nil {
			return err
		}
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO voice_bindings(id,ref,active,record) VALUES(?,?,?,?)`, b.ID, b.Ref, b.Active, record); err != nil {
		return err
	}
	return tx.Commit()
}

func activeVoiceBinding(ctx context.Context, q voiceQuerier, id, epoch string) (VoiceBinding, error) {
	b, err := readVoiceBinding(ctx, q, id)
	if err != nil {
		return VoiceBinding{}, err
	}
	if b.Epoch != epoch || !b.Active || !b.ExpiresAt.After(time.Now()) {
		return VoiceBinding{}, ErrVoiceStale
	}
	return b, nil
}

func (j *VoiceJournal) RetireBinding(ctx context.Context, id, epoch string) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	b, err := readVoiceBinding(ctx, tx, id)
	if err != nil {
		return err
	}
	if b.Epoch != epoch {
		return ErrVoiceStale
	}
	if _, err := tx.ExecContext(ctx, `UPDATE voice_bindings SET active=0 WHERE id=?`, id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE voice_speech SET interrupted=1 WHERE binding_id=?`, id); err != nil {
		return err
	}
	return tx.Commit()
}

func (j *VoiceJournal) RenewBinding(ctx context.Context, id, epoch string, expiresAt time.Time) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	b, err := activeVoiceBinding(ctx, tx, id, epoch)
	if err != nil {
		return err
	}
	if expiresAt.Before(b.ExpiresAt) {
		return ErrVoiceConflict
	}
	b.ExpiresAt = expiresAt
	raw, err := json.Marshal(b)
	if err != nil {
		return ErrVoiceInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE voice_bindings SET record=? WHERE id=?`, raw, id); err != nil {
		return err
	}
	return tx.Commit()
}

func readVoiceTurn(ctx context.Context, q voiceQuerier, id string) (VoiceTurn, error) {
	var raw []byte
	var bindingID, epoch string
	var sequence int64
	if err := q.QueryRowContext(ctx, `SELECT record,binding_id,epoch,sequence FROM voice_turns WHERE id=?`, id).Scan(&raw, &bindingID, &epoch, &sequence); err != nil {
		return VoiceTurn{}, err
	}
	var turn VoiceTurn
	if json.Unmarshal(raw, &turn) != nil || turn.ID != id || turn.BindingID != bindingID || turn.Epoch != epoch || turn.Sequence != sequence || sequence < 0 {
		return VoiceTurn{}, ErrVoiceCorrupt
	}
	if turn.State != VoiceTurnOpen && turn.State != VoiceTurnCommitted && turn.State != VoiceTurnAborted {
		return VoiceTurn{}, ErrVoiceCorrupt
	}
	if (turn.State == VoiceTurnCommitted && turn.ProviderInputItemID == "") || (turn.State == VoiceTurnOpen && turn.ProviderInputItemID != "") {
		return VoiceTurn{}, ErrVoiceCorrupt
	}
	return turn, nil
}

func (j *VoiceJournal) Turn(ctx context.Context, id string) (VoiceTurn, error) {
	return readVoiceTurn(ctx, j.db, id)
}

func (j *VoiceJournal) CreateTurn(ctx context.Context, bindingID, epoch string, sequence int64) (VoiceTurn, error) {
	if sequence < 0 {
		return VoiceTurn{}, ErrVoiceInvalid
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return VoiceTurn{}, err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := activeVoiceBinding(ctx, tx, bindingID, epoch); err != nil {
		return VoiceTurn{}, err
	}
	var id string
	err = tx.QueryRowContext(ctx, `SELECT id FROM voice_turns WHERE binding_id=? AND epoch=? AND sequence=?`, bindingID, epoch, sequence).Scan(&id)
	if err == nil {
		return readVoiceTurn(ctx, tx, id)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return VoiceTurn{}, err
	}
	turn := VoiceTurn{ID: uuid.NewString(), BindingID: bindingID, Epoch: epoch, Sequence: sequence, State: VoiceTurnOpen}
	// Pin absence as well as identity before interpretation. Later speech and
	// reconnects cannot replace the presentation associated with this utterance.
	err = tx.QueryRowContext(ctx, `SELECT id FROM voice_speech WHERE binding_id=? ORDER BY rowid DESC LIMIT 1`, bindingID).Scan(&turn.PresentationDeliveryID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return VoiceTurn{}, err
	}
	raw, err := json.Marshal(turn)
	if err != nil {
		return VoiceTurn{}, ErrVoiceInvalid
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO voice_turns(id,binding_id,epoch,sequence,record) VALUES(?,?,?,?,?)`, turn.ID, bindingID, epoch, sequence, raw); err != nil {
		return VoiceTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return VoiceTurn{}, err
	}
	return turn, nil
}

func (j *VoiceJournal) CommitTurnInput(ctx context.Context, turnID, providerItemID string) (VoiceTurn, error) {
	if providerItemID == "" {
		return VoiceTurn{}, ErrVoiceInvalid
	}
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return VoiceTurn{}, err
	}
	defer func() { _ = tx.Rollback() }()
	turn, err := readVoiceTurn(ctx, tx, turnID)
	if err != nil {
		return VoiceTurn{}, err
	}
	if turn.State == VoiceTurnCommitted && turn.ProviderInputItemID == providerItemID {
		return turn, nil
	}
	if turn.State != VoiceTurnOpen {
		return VoiceTurn{}, ErrVoiceConflict
	}
	if _, err := activeVoiceBinding(ctx, tx, turn.BindingID, turn.Epoch); err != nil {
		return VoiceTurn{}, err
	}
	turn.State, turn.ProviderInputItemID = VoiceTurnCommitted, providerItemID
	raw, err := json.Marshal(turn)
	if err != nil {
		return VoiceTurn{}, ErrVoiceInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE voice_turns SET record=? WHERE id=?`, raw, turnID); err != nil {
		return VoiceTurn{}, err
	}
	if err := tx.Commit(); err != nil {
		return VoiceTurn{}, err
	}
	return turn, nil
}

func (j *VoiceJournal) AbortTurn(ctx context.Context, turnID string) error {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	turn, err := readVoiceTurn(ctx, tx, turnID)
	if err != nil {
		return err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM voice_intents WHERE turn_id=?`, turnID).Scan(&count); err != nil {
		return err
	}
	if count != 0 {
		return ErrVoiceConflict
	}
	turn.State = VoiceTurnAborted
	raw, err := json.Marshal(turn)
	if err != nil {
		return ErrVoiceInvalid
	}
	if _, err := tx.ExecContext(ctx, `UPDATE voice_turns SET record=? WHERE id=?`, raw, turnID); err != nil {
		return err
	}
	return tx.Commit()
}

// Params always contains one ordinary text item. Presentation is quoted evidence,
// never a change to the user's text or the authority selecting its target.
func voiceIntentParams(ref, instanceID, method, text, mutationID string, presentation *appwire.VoicePresentationContext) (json.RawMessage, error) {
	if method != appwire.MethodTurnStart && method != appwire.MethodTurnSteer {
		return nil, ErrVoiceInvalid
	}
	items := []appwire.InputItem{{Type: "text", Text: text, VoicePresentation: presentation}}
	input, err := appwire.NormalizeVoicePresentationInput(items, method, "human", instanceID)
	if err != nil || !input.HasContent() {
		return nil, ErrVoiceInvalid
	}
	raw, err := json.Marshal(appwire.TurnStartParams{Ref: ref, ExpectedInstanceID: instanceID, ClientMutationID: mutationID, Input: input.Items})
	if err != nil || appwire.ValidateMutationParams(method, raw) != nil {
		return nil, ErrVoiceInvalid
	}
	return raw, nil
}

func voicePresentationForTurn(ctx context.Context, q voiceQuerier, turn VoiceTurn) (*appwire.VoicePresentationContext, error) {
	if turn.PresentationDeliveryID == "" {
		return nil, nil
	}
	d, err := readSpeechDelivery(ctx, q, turn.PresentationDeliveryID)
	if err != nil {
		return nil, err
	}
	if d.BindingID != turn.BindingID || d.Epoch != turn.Epoch || !utf8.ValidString(d.GeneratedTranscript) {
		return nil, ErrVoiceCorrupt
	}
	p := &appwire.VoicePresentationContext{
		Version: 1, DeliveryID: d.ID, BindingID: d.BindingID, Epoch: d.Epoch, InstanceID: d.InstanceID,
		CanonicalSourceItemID: d.CanonicalSourceItemID, OriginatingRequestID: d.OriginatingRequestID, FrontierID: d.FrontierID,
		RunGeneration: d.RunGeneration, PresentationGeneration: d.PresentationGeneration,
		GeneratedTranscript: d.GeneratedTranscript, OutputComplete: d.OutputComplete, Interrupted: d.Interrupted, SampleRateHz: 24000,
		Items: make([]appwire.VoicePresentationItem, 0, len(d.Items)),
	}
	if len(p.GeneratedTranscript) > appwire.VoicePresentationMaxTranscriptBytes {
		p.GeneratedTranscript = p.GeneratedTranscript[:appwire.VoicePresentationMaxTranscriptBytes]
		for !utf8.ValidString(p.GeneratedTranscript) {
			p.GeneratedTranscript = p.GeneratedTranscript[:len(p.GeneratedTranscript)-1]
		}
		p.TranscriptTruncated = true
	}
	for _, item := range d.Items {
		p.Items = append(p.Items, appwire.VoicePresentationItem{
			ResponseID: item.ResponseID, ItemID: item.ItemID, GeneratedSamples: item.GeneratedSamples,
			QueuedSamples: item.QueuedSamples, PlayedSamples: item.PlayedSamples, PlaybackComplete: item.PlaybackComplete,
		})
	}
	return p, nil
}

func readVoiceIntent(ctx context.Context, q voiceQuerier, turnID string) (VoiceIntent, error) {
	var raw []byte
	var bindingID, mutationID string
	if err := q.QueryRowContext(ctx, `SELECT record,binding_id,mutation_id FROM voice_intents WHERE turn_id=?`, turnID).Scan(&raw, &bindingID, &mutationID); err != nil {
		return VoiceIntent{}, err
	}
	var in VoiceIntent
	if json.Unmarshal(raw, &in) != nil || in.TurnID != turnID || in.BindingID != bindingID || in.MutationID != mutationID {
		return VoiceIntent{}, ErrVoiceCorrupt
	}
	b, err := readVoiceBinding(ctx, q, bindingID)
	if err != nil {
		return VoiceIntent{}, err
	}
	turn, err := readVoiceTurn(ctx, q, turnID)
	if err != nil {
		return VoiceIntent{}, err
	}
	var params appwire.TurnStartParams
	if json.Unmarshal(in.Params, &params) != nil || len(params.Input) != 1 {
		return VoiceIntent{}, ErrVoiceCorrupt
	}
	presentation := params.Input[0].VoicePresentation
	if presentation == nil && turn.PresentationDeliveryID != "" || presentation != nil && (presentation.DeliveryID != turn.PresentationDeliveryID || presentation.BindingID != turn.BindingID || presentation.Epoch != turn.Epoch) {
		return VoiceIntent{}, ErrVoiceCorrupt
	}
	expected, err := voiceIntentParams(in.Ref, in.ExpectedInstanceID, in.Method, in.Text, in.MutationID, presentation)
	if err != nil || string(expected) != string(in.Params) || in.Ref != b.Ref || in.ExpectedInstanceID != b.InstanceID || in.Epoch != b.Epoch || turn.BindingID != b.ID || turn.Epoch != b.Epoch || turn.State != VoiceTurnCommitted {
		return VoiceIntent{}, ErrVoiceCorrupt
	}
	switch in.State {
	case VoiceIntentPrepared, VoiceIntentUncertain:
		if in.Receipt != nil {
			return VoiceIntent{}, ErrVoiceCorrupt
		}
	case VoiceIntentAccepted:
		if in.Receipt == nil || !validVoiceReceipt(in, *in.Receipt) {
			return VoiceIntent{}, ErrVoiceCorrupt
		}
	default:
		return VoiceIntent{}, ErrVoiceCorrupt
	}
	return in, nil
}

func (j *VoiceJournal) Intent(ctx context.Context, turnID string) (VoiceIntent, error) {
	return readVoiceIntent(ctx, j.db, turnID)
}

func (j *VoiceJournal) PrepareIntent(ctx context.Context, turnID, method, text, mutationID string) (VoiceIntent, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return VoiceIntent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	existing, err := readVoiceIntent(ctx, tx, turnID)
	if err == nil {
		if existing.Method != method || existing.Text != text || existing.MutationID != mutationID {
			return VoiceIntent{}, ErrVoiceConflict
		}
		return existing, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return VoiceIntent{}, err
	}
	turn, err := readVoiceTurn(ctx, tx, turnID)
	if err != nil {
		return VoiceIntent{}, err
	}
	if turn.State != VoiceTurnCommitted {
		return VoiceIntent{}, ErrVoiceConflict
	}
	b, err := activeVoiceBinding(ctx, tx, turn.BindingID, turn.Epoch)
	if err != nil {
		return VoiceIntent{}, err
	}
	presentation, err := voicePresentationForTurn(ctx, tx, turn)
	if err != nil {
		return VoiceIntent{}, err
	}
	params, err := voiceIntentParams(b.Ref, b.InstanceID, method, text, mutationID, presentation)
	if err != nil {
		return VoiceIntent{}, err
	}
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM voice_intents WHERE mutation_id=?`, mutationID).Scan(&count); err != nil {
		return VoiceIntent{}, err
	}
	if count != 0 {
		return VoiceIntent{}, ErrVoiceConflict
	}
	in := VoiceIntent{TurnID: turnID, BindingID: b.ID, Epoch: b.Epoch, Ref: b.Ref, ExpectedInstanceID: b.InstanceID, Method: method, Text: text, MutationID: mutationID, Params: params, State: VoiceIntentPrepared}
	raw, err := json.Marshal(in)
	if err != nil {
		return VoiceIntent{}, ErrVoiceInvalid
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO voice_intents(turn_id,binding_id,mutation_id,record) VALUES(?,?,?,?)`, turnID, b.ID, mutationID, raw); err != nil {
		return VoiceIntent{}, err
	}
	if err := tx.Commit(); err != nil {
		return VoiceIntent{}, err
	}
	return in, nil
}

func saveVoiceIntent(ctx context.Context, tx *sql.Tx, in VoiceIntent) error {
	raw, err := json.Marshal(in)
	if err != nil {
		return ErrVoiceInvalid
	}
	_, err = tx.ExecContext(ctx, `UPDATE voice_intents SET record=? WHERE turn_id=?`, raw, in.TurnID)
	return err
}

// RecordAttempt must succeed before sending Params. Reconciled means the caller
// checked authoritative mutation custody on the original instance, not a timeout.
func (j *VoiceJournal) RecordAttempt(ctx context.Context, turnID, currentInstanceID string, reconciled bool) (VoiceIntent, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return VoiceIntent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	in, err := readVoiceIntent(ctx, tx, turnID)
	if err != nil {
		return VoiceIntent{}, err
	}
	if in.State == VoiceIntentAccepted {
		return VoiceIntent{}, ErrVoiceConflict
	}
	if _, err := activeVoiceBinding(ctx, tx, in.BindingID, in.Epoch); err != nil {
		return VoiceIntent{}, err
	}
	if currentInstanceID != in.ExpectedInstanceID {
		return VoiceIntent{}, ErrVoiceStale
	}
	if in.State == VoiceIntentUncertain && !reconciled {
		return VoiceIntent{}, ErrVoiceReconcileRequired
	}
	in.State = VoiceIntentUncertain
	if err := saveVoiceIntent(ctx, tx, in); err != nil {
		return VoiceIntent{}, err
	}
	if err := tx.Commit(); err != nil {
		return VoiceIntent{}, err
	}
	return in, nil
}

func validVoiceReceipt(in VoiceIntent, r appwire.MutationReceipt) bool {
	ref, err := appwire.ParseRef(in.Ref)
	return err == nil && r.ClientMutationID == in.MutationID && r.ThreadID == ref.ThreadID && r.InstanceID == in.ExpectedInstanceID &&
		(r.Disposition == appwire.MutationDispositionApplied || r.Disposition == appwire.MutationDispositionReplayed) &&
		(r.ProjectionState == appwire.MutationProjectionPending || r.ProjectionState == appwire.MutationProjectionReflected || r.ProjectionState == appwire.MutationProjectionRemoved)
}

func (j *VoiceJournal) RecordReceipt(ctx context.Context, turnID string, receipt appwire.MutationReceipt) (VoiceIntent, error) {
	tx, err := j.db.BeginTx(ctx, nil)
	if err != nil {
		return VoiceIntent{}, err
	}
	defer func() { _ = tx.Rollback() }()
	in, err := readVoiceIntent(ctx, tx, turnID)
	if err != nil {
		return VoiceIntent{}, err
	}
	if !validVoiceReceipt(in, receipt) {
		return VoiceIntent{}, ErrVoiceConflict
	}
	if in.State == VoiceIntentPrepared {
		return VoiceIntent{}, ErrVoiceConflict
	}
	if in.State == VoiceIntentAccepted {
		return in, nil
	}
	in.State, in.Receipt = VoiceIntentAccepted, &receipt
	if err := saveVoiceIntent(ctx, tx, in); err != nil {
		return VoiceIntent{}, err
	}
	if err := tx.Commit(); err != nil {
		return VoiceIntent{}, err
	}
	return in, nil
}
