package apilog

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"
)

type EncodedHeaderValue struct {
	Encoding  BodyEncoding `json:"encoding"`
	Data      string       `json:"data"`
	ByteCount int          `json:"byte_count"`
}

func EncodeHeaderValue(data []byte) EncodedHeaderValue {
	value := EncodedHeaderValue{ByteCount: len(data)}
	if utf8.Valid(data) {
		value.Encoding = BodyUTF8
		value.Data = string(data)
		return value
	}
	value.Encoding = BodyBase64
	value.Data = base64.StdEncoding.EncodeToString(data)
	return value
}

func DecodeHeaderValue(value EncodedHeaderValue) ([]byte, error) {
	if value.ByteCount < 0 {
		return nil, errors.New("encoded header value byte count must be non-negative")
	}
	var data []byte
	switch value.Encoding {
	case BodyUTF8:
		if !utf8.ValidString(value.Data) {
			return nil, errors.New("encoded UTF-8 header value contains invalid UTF-8")
		}
		data = []byte(value.Data)
	case BodyBase64:
		var err error
		data, err = base64.StdEncoding.DecodeString(value.Data)
		if err != nil {
			return nil, fmt.Errorf("decode base64 header value: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown header value encoding %q", value.Encoding)
	}
	if len(data) != value.ByteCount {
		return nil, fmt.Errorf("encoded header value byte count is %d, decoded %d bytes", value.ByteCount, len(data))
	}
	return data, nil
}

func (value *EncodedHeaderValue) UnmarshalJSON(data []byte) error {
	type encodedHeaderValueJSON EncodedHeaderValue
	var decoded encodedHeaderValueJSON
	if err := decodeStrict(data, &decoded); err != nil {
		return err
	}
	*value = EncodedHeaderValue(decoded)
	if _, err := DecodeHeaderValue(*value); err != nil {
		return err
	}
	return nil
}

// EncodedHeader keeps exact header bytes in memory while using explicit UTF-8
// or base64 values in the durable JSON representation.
type EncodedHeader map[string][]string

func (header EncodedHeader) MarshalJSON() ([]byte, error) {
	encoded := make(map[string][]EncodedHeaderValue, len(header))
	for name, values := range header {
		encodedValues := make([]EncodedHeaderValue, len(values))
		for i, value := range values {
			encodedValues[i] = EncodeHeaderValue([]byte(value))
		}
		encoded[name] = encodedValues
	}
	return json.Marshal(encoded)
}

func (header *EncodedHeader) UnmarshalJSON(data []byte) error {
	var encoded map[string][]EncodedHeaderValue
	if err := decodeStrict(data, &encoded); err != nil {
		return err
	}
	if encoded == nil {
		*header = nil
		return nil
	}
	decoded := make(EncodedHeader, len(encoded))
	for name, values := range encoded {
		decodedValues := make([]string, len(values))
		for i, value := range values {
			data, err := DecodeHeaderValue(value)
			if err != nil {
				return fmt.Errorf("decode header %q value %d: %w", name, i, err)
			}
			decodedValues[i] = string(data)
		}
		decoded[name] = decodedValues
	}
	*header = decoded
	return nil
}
