package hubendpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

const recordName = "hub-endpoint.json"

// Record is the credential-free runtime identity published by the lock owner.
type Record struct {
	SchemaVersion       int       `json:"schema_version"`
	BootID              string    `json:"boot_id"`
	PID                 int       `json:"pid"`
	StartedAt           time.Time `json:"started_at"`
	ConfigPath          string    `json:"config_path"`
	HubStateRoot        string    `json:"hub_state_root"`
	RequestedListenAddr string    `json:"requested_listen_addr"`
	ListenSource        string    `json:"listen_source"`
	BoundAddr           string    `json:"bound_addr"`
	LocalURL            string    `json:"local_url,omitempty"`
	LoopbackURL         string    `json:"loopback_url,omitempty"`
	PublicURL           string    `json:"public_url,omitempty"`
	Version             string    `json:"version,omitempty"`
	BackendGitSHA       string    `json:"backend_git_sha,omitempty"`
}

func validateRecord(record Record, root string) error {
	if record.SchemaVersion != SchemaVersion {
		return fmt.Errorf("unsupported endpoint record schema: %d", record.SchemaVersion)
	}
	if record.BootID == "" || record.PID <= 0 || record.StartedAt.IsZero() || record.ConfigPath == "" || record.HubStateRoot != root {
		return errors.New("incomplete endpoint record")
	}
	if _, err := ParseListenAddr(record.RequestedListenAddr); err != nil {
		return err
	}
	bound, err := ParseListenAddr(record.BoundAddr)
	if err != nil || bound.port == 0 {
		return errors.New("invalid bound listener")
	}
	switch record.ListenSource {
	case "flag", "env", "config", "default":
	default:
		return errors.New("invalid listener source")
	}
	if record.LocalURL != "" {
		local, err := LocalOrigin(record.BoundAddr)
		if err != nil || record.LocalURL != local.String() {
			return errors.New("invalid local origin")
		}
	}
	if record.LoopbackURL != "" {
		origin, err := ParseOrigin(record.LoopbackURL)
		if err != nil || origin.String() != record.LoopbackURL || origin.Scheme() != "http" || origin.Port() != bound.port || !originIsLoopback(origin) || record.LoopbackURL != record.LocalURL {
			return errors.New("invalid loopback origin")
		}
	}
	if record.PublicURL != "" {
		origin, err := ParseOrigin(record.PublicURL)
		if err != nil || origin.String() != record.PublicURL {
			return errors.New("invalid public origin")
		}
	}
	return nil
}

func checkRecordFile(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0022 != 0 || !ownedByCurrentUser(info) {
		return errors.New("unsafe endpoint record")
	}
	if info.Size() > maxEndpointBytes {
		return errors.New("endpoint record exceeds size limit")
	}
	return nil
}

// Read reads a bounded private record and never follows a symlink.
func Read(stateRoot string) (Record, error) {
	path := filepath.Join(stateRoot, recordName)
	before, err := os.Lstat(path)
	if err != nil {
		return Record{}, err
	}
	if err := checkRecordFile(before); err != nil {
		return Record{}, err
	}
	f, err := openRecord(path)
	if err != nil {
		return Record{}, err
	}
	defer func() { _ = f.Close() }()
	opened, err := f.Stat()
	if err != nil {
		return Record{}, err
	}
	if !os.SameFile(before, opened) {
		return Record{}, errors.New("endpoint record changed while opening")
	}
	if err := checkRecordFile(opened); err != nil {
		return Record{}, err
	}
	data, err := io.ReadAll(io.LimitReader(f, maxEndpointBytes+1))
	if err != nil {
		return Record{}, err
	}
	if len(data) > maxEndpointBytes {
		return Record{}, errors.New("endpoint record exceeds size limit")
	}
	var record Record
	if err := decodeJSON(data, &record); err != nil {
		return Record{}, err
	}
	if err := validateRecord(record, stateRoot); err != nil {
		return Record{}, err
	}
	return record, nil
}

// Publish atomically replaces stale metadata while the caller owns hub.lock.
// The returned cleanup must be invoked before releasing the lock.
func Publish(stateRoot string, record Record) (func() error, error) {
	if err := validateRecord(record, stateRoot); err != nil {
		return nil, err
	}
	data, err := jsonBytes(record)
	if err != nil {
		return nil, err
	}
	if len(data) > maxEndpointBytes {
		return nil, errors.New("endpoint record exceeds size limit")
	}
	path := filepath.Join(stateRoot, recordName)
	parent := filepath.Dir(path)
	temp, err := os.CreateTemp(parent, ".hub-endpoint-*")
	if err != nil {
		return nil, err
	}
	tmpName := temp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if err := temp.Chmod(0600); err != nil {
		_ = temp.Close()
		return nil, err
	}
	if _, err := temp.Write(data); err != nil {
		_ = temp.Close()
		return nil, err
	}
	if err := temp.Sync(); err != nil {
		_ = temp.Close()
		return nil, err
	}
	if err := temp.Close(); err != nil {
		return nil, err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return nil, err
	}
	if err := syncDir(parent); err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	published, err := os.Lstat(path)
	if err != nil {
		_ = os.Remove(path)
		return nil, err
	}
	return func() error {
		now, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if !os.SameFile(now, published) {
			return nil
		}
		current, err := Read(stateRoot)
		if err != nil {
			return err
		}
		if current.BootID != record.BootID || !current.StartedAt.Equal(record.StartedAt) {
			return nil
		}
		return os.Remove(path)
	}, nil
}
func syncDir(root string) error {
	d, err := os.Open(root)
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	err = d.Sync()
	if errors.Is(err, syscall.ENOSYS) || errors.Is(err, syscall.ENOTSUP) || errors.Is(err, syscall.EINVAL) {
		return nil
	}
	return err
}
func jsonBytes(r Record) ([]byte, error) {
	var b bytes.Buffer
	err := json.NewEncoder(&b).Encode(r)
	return b.Bytes(), err
}
