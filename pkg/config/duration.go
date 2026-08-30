package config

import (
	"fmt"
	"strings"
	"time"
)

// Duration is a time.Duration that TOML decodes from duration strings such as
// "30m" and encodes back to the same form, so bad values fail at decode time
// with a line number instead of panicking later.
type Duration struct {
	time.Duration
}

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(text []byte) error {
	value, err := time.ParseDuration(strings.TrimSpace(string(text)))
	if err != nil {
		return fmt.Errorf("invalid duration %q: %w", string(text), err)
	}
	d.Duration = value
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) {
	return []byte(compactDuration(d.Duration)), nil
}

// compactDuration renders whole hours and minutes without their zero
// components, so defaults read as "30m" instead of "30m0s".
func compactDuration(value time.Duration) string {
	switch {
	case value == 0:
		return "0s"
	case value%time.Second != 0:
		return value.String()
	case value%time.Hour == 0:
		return fmt.Sprintf("%dh", value/time.Hour)
	case value%time.Minute == 0:
		return fmt.Sprintf("%dm", value/time.Minute)
	default:
		return value.String()
	}
}
