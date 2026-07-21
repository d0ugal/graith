// Package sessionlabel defines the bounded, case-insensitive label semantics
// shared by daemon mutation and CLI filtering.
package sessionlabel

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
)

const (
	MaxBytes      = 64
	MaxPerSession = 32
)

// Equal reports whether two normalized label spellings identify the same
// label. EqualFold is locale-independent Unicode simple case folding.
func Equal(a, b string) bool {
	return strings.EqualFold(a, b)
}

// Normalize trims, validates, and case-insensitively deduplicates labels while
// preserving the first display spelling. It always returns a non-nil slice on
// success so callers can expose an explicit empty JSON array.
func Normalize(labels []string) ([]string, error) {
	result := make([]string, 0, len(labels))

	for _, raw := range labels {
		label, err := normalizeOne(raw)
		if err != nil {
			return nil, err
		}

		if Contains(result, label) {
			continue
		}

		if len(result) == MaxPerSession {
			return nil, fmt.Errorf("session labels exceed maximum of %d", MaxPerSession)
		}

		result = append(result, label)
	}

	return result, nil
}

// Apply returns the label set produced by atomically removing then adding the
// requested identities. Existing display spellings win over add spellings.
func Apply(existing, add, remove []string) ([]string, error) {
	current, err := Normalize(existing)
	if err != nil {
		return nil, fmt.Errorf("invalid existing session labels: %w", err)
	}

	adds, err := Normalize(add)
	if err != nil {
		return nil, err
	}

	removes, err := Normalize(remove)
	if err != nil {
		return nil, err
	}

	for _, label := range adds {
		if Contains(removes, label) {
			return nil, fmt.Errorf("label %q cannot be both added and removed", label)
		}
	}

	result := make([]string, 0, len(current)+len(adds))
	for _, label := range current {
		if !Contains(removes, label) {
			result = append(result, label)
		}
	}

	for _, label := range adds {
		if Contains(result, label) {
			continue
		}

		if len(result) == MaxPerSession {
			return nil, fmt.Errorf("session labels exceed maximum of %d", MaxPerSession)
		}

		result = append(result, label)
	}

	return result, nil
}

// Contains reports whether labels contains candidate under label identity
// rules. Callers should normalize user input before using it as candidate.
func Contains(labels []string, candidate string) bool {
	for _, label := range labels {
		if Equal(label, candidate) {
			return true
		}
	}

	return false
}

// ContainsAll reports whether labels contains every requested identity.
func ContainsAll(labels, requested []string) bool {
	for _, label := range requested {
		if !Contains(labels, label) {
			return false
		}
	}

	return true
}

func normalizeOne(raw string) (string, error) {
	label := strings.TrimSpace(raw)
	if label == "" {
		return "", errors.New("label must not be empty")
	}

	if len(label) > MaxBytes {
		return "", fmt.Errorf("label %q exceeds %d bytes", label, MaxBytes)
	}

	if strings.ContainsRune(label, ',') {
		return "", fmt.Errorf("label %q contains a comma", label)
	}

	for _, r := range label {
		if unicode.IsControl(r) {
			return "", fmt.Errorf("label %q contains a control character", label)
		}
	}

	return label, nil
}
