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
	Encoding  BodyEncoding `json:"encoding"`
	Data      string       `json:"data"`
	ByteCount int          `json:"byte_count"`
}

func EncodeBody(data []byte) EncodedBody {
	body := EncodedBody{ByteCount: len(data)}
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
