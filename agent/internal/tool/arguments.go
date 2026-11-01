package tool

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"

	jsonschema "github.com/santhosh-tekuri/jsonschema/v5"
)

// DecodeArguments preserves JSON numbers through all tool ingestion boundaries.
// It retains the former decoder's float-overflow rejection without storing its
// approximate view. Empty bytes and null still denote empty arguments.
func DecodeArguments(raw []byte) (map[string]any, error) {
	args := map[string]any{}
	if len(raw) == 0 {
		return args, nil
	}
	// Token's default numeric conversion checks every number's float range,
	// including values a later duplicate key discards. Do not retain that
	// approximate view: the actual argument map is decoded exactly below.
	ranges := json.NewDecoder(bytes.NewReader(raw))
	for {
		if _, err := ranges.Token(); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&args); err != nil {
		return nil, err
	}
	var extra any
	if err := dec.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("trailing JSON value")
		}
		return nil, err
	}
	if args == nil {
		args = map[string]any{}
	}
	return args, nil
}

// ValidateArguments contains the pinned validator's panics on numbers outside
// its rational domain. Recovery never covers normalizers or tool execution.
// This is a validation failure boundary, not an arithmetic resource budget.
func ValidateArguments(schema *jsonschema.Schema, args map[string]any) (err error) {
	defer func() {
		if recovered := recover(); recovered != nil {
			err = fmt.Errorf("schema validation could not represent arguments: %v", recovered)
		}
	}()
	return schema.Validate(args)
}

// WholeNumberInt reads an exact integral JSON number in the platform int range.
// Callers retain their own narrower bounds and existing non-Number branches.
func WholeNumberInt(number json.Number) (int, bool) {
	r, ok := new(big.Rat).SetString(number.String())
	if !ok || !r.IsInt() || !r.Num().IsInt64() {
		return 0, false
	}
	n := r.Num().Int64()
	i := int(n)
	return i, int64(i) == n
}
