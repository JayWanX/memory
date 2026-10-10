package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"time"
)

func decodeCanonicalObject(label string, raw []byte, maximum int, destination any) error {
	if len(raw) == 0 || len(raw) > maximum {
		return fmt.Errorf("%s exceeds its canonical byte bound", label)
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return fmt.Errorf("%s: %w", label, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode canonical %s: %w", label, err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return fmt.Errorf("canonical %s has trailing data", label)
	}
	reconstructed, err := json.Marshal(destination)
	if err != nil {
		return fmt.Errorf("reconstruct canonical %s: %w", label, err)
	}
	if !bytes.Equal(reconstructed, raw) {
		return fmt.Errorf("%s is not in canonical field order and encoding", label)
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := consumeJSONValue(decoder); err != nil {
		return err
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return errors.New("JSON has trailing data")
	}
	return nil
}

func consumeJSONValue(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, composite := token.(json.Delim)
	if !composite {
		return nil
	}
	switch delimiter {
	case '{':
		return consumeJSONObject(decoder)
	case '[':
		return consumeJSONArray(decoder)
	default:
		return errors.New("JSON has an unexpected delimiter")
	}
}

func consumeJSONObject(decoder *json.Decoder) error {
	seen := make(map[string]struct{})
	for decoder.More() {
		keyToken, err := decoder.Token()
		if err != nil {
			return err
		}
		key, ok := keyToken.(string)
		if !ok {
			return errors.New("JSON object key is not a string")
		}
		if _, duplicate := seen[key]; duplicate {
			return fmt.Errorf("JSON repeats key %q", key)
		}
		seen[key] = struct{}{}
		if err := consumeJSONValue(decoder); err != nil {
			return err
		}
	}
	return consumeJSONClosing(decoder, '}')
}

func consumeJSONArray(decoder *json.Decoder) error {
	for decoder.More() {
		if err := consumeJSONValue(decoder); err != nil {
			return err
		}
	}
	return consumeJSONClosing(decoder, ']')
}

func consumeJSONClosing(decoder *json.Decoder, expected json.Delim) error {
	closing, err := decoder.Token()
	if err != nil {
		return err
	}
	if closing != expected {
		return errors.New("JSON has a mismatched delimiter")
	}
	return nil
}

func parseCanonicalTime(label, value string) (time.Time, error) {
	parsed, err := time.Parse(time.RFC3339Nano, value)
	if err != nil || value != parsed.UTC().Format(time.RFC3339Nano) {
		return time.Time{}, fmt.Errorf("%s is not canonical UTC RFC3339Nano", label)
	}
	return parsed.UTC(), nil
}

func parseStoredTime(label, value string) (time.Time, error) {
	const layout = "2006-01-02T15:04:05.000000000Z"
	parsed, err := time.Parse(layout, value)
	if err != nil || value != parsed.UTC().Format(layout) {
		return time.Time{}, fmt.Errorf("%s is not canonical authority time", label)
	}
	return parsed.UTC(), nil
}

func cloneEventRef(value *eventRefWire) *eventRefWire {
	if value == nil {
		return nil
	}
	copyValue := *value
	return &copyValue
}

func equalStringsAsSet(left, right []string) bool {
	leftCopy, rightCopy := append([]string{}, left...), append([]string{}, right...)
	slices.Sort(leftCopy)
	slices.Sort(rightCopy)
	return slices.Equal(leftCopy, rightCopy)
}
