package hubendpoint

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const SchemaVersion = 1
const maxEndpointBytes = 64 << 10

// Report describes the selected configuration and, only after verification, a live instance.
type Report struct {
	SchemaVersion        int     `json:"schema_version"`
	State                string  `json:"state"`
	ConfigPath           string  `json:"config_path"`
	ConfigExists         bool    `json:"config_exists"`
	HubStateRoot         string  `json:"hub_state_root"`
	ConfiguredListenAddr string  `json:"configured_listen_addr"`
	ListenSource         string  `json:"listen_source"`
	PublicURL            string  `json:"public_url,omitempty"`
	Running              *Record `json:"running,omitempty"`
	Reason               string  `json:"reason,omitempty"`
}

// DecodeReport bounds a command result and rejects malformed or unknown schemas.
func DecodeReport(r io.Reader) (Report, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxEndpointBytes+1))
	if err != nil {
		return Report{}, err
	}
	if len(data) > maxEndpointBytes {
		return Report{}, errors.New("endpoint report exceeds size limit")
	}
	var report Report
	if err := decodeJSON(data, &report); err != nil {
		return Report{}, err
	}
	if report.SchemaVersion != SchemaVersion {
		return Report{}, fmt.Errorf("unsupported endpoint report schema: %d", report.SchemaVersion)
	}
	switch report.State {
	case "stopped", "starting", "unavailable":
		if report.Running != nil {
			return Report{}, errors.New("non-running endpoint has a running record")
		}
	case "running":
		if report.Running == nil {
			return Report{}, errors.New("running endpoint lacks a record")
		}
		if err := validateRecord(*report.Running, report.HubStateRoot); err != nil {
			return Report{}, err
		}
	default:
		return Report{}, errors.New("unknown endpoint state")
	}
	if report.ConfigPath == "" || report.HubStateRoot == "" || report.ConfiguredListenAddr == "" || report.ListenSource == "" {
		return Report{}, errors.New("incomplete endpoint report")
	}
	return report, nil
}
func decodeJSON(data []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err := d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("trailing endpoint JSON")
	}
	return nil
}
