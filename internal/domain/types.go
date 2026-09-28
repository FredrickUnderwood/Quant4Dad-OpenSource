package domain

import (
	"database/sql/driver"
	"errors"

	"github.com/bytedance/sonic"
)

// StringSlice is a []string persisted as a JSON-encoded text column,
// so it works uniformly across sqlite / mysql without driver-specific arrays.
type StringSlice []string

func (s StringSlice) Value() (driver.Value, error) {
	if s == nil {
		return "[]", nil
	}
	return sonic.MarshalString(s)
}

func (s *StringSlice) Scan(src any) error {
	if src == nil {
		*s = nil
		return nil
	}
	var raw []byte
	switch v := src.(type) {
	case []byte:
		raw = v
	case string:
		raw = []byte(v)
	default:
		return errors.New("StringSlice: unsupported scan type")
	}
	if len(raw) == 0 {
		*s = nil
		return nil
	}
	return sonic.Unmarshal(raw, s)
}
