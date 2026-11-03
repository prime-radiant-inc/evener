package jsonschema_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"testing"

	"primeradiant.com/evener/llm/jsonschema"
)

func TestCompiledNumericConcurrentValidation(t *testing.T) {
	const exponent = "99999999999999999999999999999999"
	const previous = "99999999999999999999999999999998"
	s := numericSchema(t, fmt.Sprintf(`{
		"type":"object","required":["n","items","label"],"minProperties":3,"maxProperties":3,
		"properties":{
			"n":{"type":"integer","minimum":1e%s,"maximum":3e%s,"multipleOf":2e%s},
			"items":{"type":"array","minItems":21,"maxItems":1e%s,"uniqueItems":true,
				"items":{"type":"object","properties":{"nested":{"const":[2e%s]}},"required":["nested"]}},
			"label":{"type":"string","minLength":2,"maxLength":3}
		}}`, exponent, exponent, previous, exponent, exponent))
	before := s.Properties["n"].Minimum.String()
	const workers, rounds = 32, 16
	start := make(chan struct{})
	failures := make(chan error, workers)
	var wg sync.WaitGroup
	for worker := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for round := range rounds {
				items := make([]any, 21)
				for i := range items {
					items[i] = map[string]any{"index": worker*10000 + round*100 + i, "nested": []any{json.Number("20e" + previous)}}
				}
				instance := map[string]any{"n": json.Number("2e" + exponent), "items": items, "label": "é界"}
				if err := s.Validate(instance); err != nil {
					failures <- fmt.Errorf("worker=%d round=%d valid: %w", worker, round, err)
					return
				}
				var keyword, location string
				switch round % 4 {
				case 0:
					instance["n"] = json.Number("4e" + exponent)
					keyword, location = "/properties/n/maximum", "/n"
				case 1:
					items[20] = items[0]
					keyword, location = "/properties/items/uniqueItems", "/items"
				case 2:
					instance["items"] = items[:20]
					keyword, location = "/properties/items/minItems", "/items"
				case 3:
					instance["label"] = "é"
					keyword, location = "/properties/label/minLength", "/label"
				}
				err := s.Validate(instance)
				var ve *jsonschema.ValidationError
				if !errors.As(err, &ve) || ve.KeywordLocation != "" || ve.InstanceLocation != "" || len(ve.Causes) != 1 {
					failures <- fmt.Errorf("worker=%d round=%d root: %#v", worker, round, err)
					return
				}
				cause := ve.Causes[0]
				if cause.KeywordLocation != keyword || cause.InstanceLocation != location || cause.AbsoluteKeywordLocation != s.Location+keyword || len(cause.Causes) != 0 {
					failures <- fmt.Errorf("worker=%d round=%d cause: %#v", worker, round, cause)
					return
				}
			}
		}()
	}
	close(start)
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Error(err)
	}
	if after := s.Properties["n"].Minimum.String(); after != before {
		t.Fatalf("compiled minimum changed: %s -> %s", before, after)
	}
	t.Logf("%d goroutines each validated %d distinct valid/invalid instance pairs against one schema, numeric/identity/cardinality error locations stable", workers, rounds)
}
