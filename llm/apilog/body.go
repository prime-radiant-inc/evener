package apilog

import (
	"encoding/base64"
	"fmt"
	"unicode/utf8"
)

type BodyEncoding string

const (
	BodyUTF8   BodyEncoding = "utf8"
	BodyBase64 BodyEncoding = "base64"
)

type EncodedBody struct {
	Encoding                 BodyEncoding `json:"encoding"`
	Data                     string       `json:"data"`
	ByteCount                int          `json:"byte_count"`
	Exact                    bool         `json:"exact"`
	CredentialValuesExcluded bool         `json:"credential_values_excluded"`
}

func EncodeBody(data []byte) EncodedBody {
	body := EncodedBody{ByteCount: len(data), Exact: true}
	if utf8.Valid(data) {
		body.Encoding = BodyUTF8
		body.Data = string(data)
		return body
	}
	body.Encoding = BodyBase64
	body.Data = base64.StdEncoding.EncodeToString(data)
	return body
}

func DecodeBody(body EncodedBody) ([]byte, error) {
	if body.Exact && body.CredentialValuesExcluded {
		return nil, fmt.Errorf("encoded body cannot be exact when credential values were excluded")
	}
	if body.CredentialValuesExcluded {
		if body.Encoding != "" || body.Data != "" || body.ByteCount != 0 {
			return nil, fmt.Errorf("credential-excluded body must not retain content")
		}
		return nil, nil
	}
	if body.ByteCount < 0 {
		return nil, fmt.Errorf("encoded body byte count must be non-negative")
	}

	var data []byte
	switch body.Encoding {
	case BodyUTF8:
		if !utf8.ValidString(body.Data) {
			return nil, fmt.Errorf("encoded UTF-8 body contains invalid UTF-8")
		}
		data = []byte(body.Data)
	case BodyBase64:
		var err error
		data, err = base64.StdEncoding.DecodeString(body.Data)
		if err != nil {
			return nil, fmt.Errorf("decode base64 body: %w", err)
		}
	default:
		return nil, fmt.Errorf("unknown body encoding %q", body.Encoding)
	}
	if len(data) != body.ByteCount {
		return nil, fmt.Errorf("encoded body byte count is %d, decoded %d bytes", body.ByteCount, len(data))
	}
	return data, nil
}

func (body *EncodedBody) UnmarshalJSON(data []byte) error {
	type encodedBodyJSON struct {
		Encoding                 BodyEncoding `json:"encoding"`
		Data                     string       `json:"data"`
		ByteCount                int          `json:"byte_count"`
		Exact                    *bool        `json:"exact"`
		CredentialValuesExcluded *bool        `json:"credential_values_excluded"`
	}
	var decoded encodedBodyJSON
	if err := decodeStrict(data, &decoded); err != nil {
		return err
	}
	if decoded.Exact == nil {
		return fmt.Errorf("encoded body exact truth field is required")
	}
	if decoded.CredentialValuesExcluded == nil {
		return fmt.Errorf("encoded body credential_values_excluded truth field is required")
	}
	*body = EncodedBody{
		Encoding:                 decoded.Encoding,
		Data:                     decoded.Data,
		ByteCount:                decoded.ByteCount,
		Exact:                    *decoded.Exact,
		CredentialValuesExcluded: *decoded.CredentialValuesExcluded,
	}
	if _, err := DecodeBody(*body); err != nil {
		return err
	}
	return nil
}
