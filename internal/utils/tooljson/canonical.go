// Package tooljson hashes the bounded JSON object domain accepted by
// Q4D Tools. Number spelling, string escaping and UTF-16 key order follow RFC 8785.
package tooljson

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/utils/strictjson"
)

func Canonical(body []byte) ([]byte, error) {
	var checked map[string]any
	if len(body) > 64<<10 || strictjson.DecodeNullable(body, &checked, 64<<10) != nil {
		return nil, strictjson.ErrInvalid
	}
	var value map[string]any
	if sonic.Unmarshal(body, &value) != nil {
		return nil, strictjson.ErrInvalid
	}
	return appendValue(nil, value)
}
func Hash(body []byte) (string, error) {
	data, err := Canonical(body)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
func appendString(out []byte, value string) []byte {
	out = append(out, '"')
	const digits = "0123456789abcdef"
	for _, r := range value {
		switch r {
		case '"', '\\':
			out = append(out, '\\', byte(r))
		case '\b':
			out = append(out, '\\', 'b')
		case '\t':
			out = append(out, '\\', 't')
		case '\n':
			out = append(out, '\\', 'n')
		case '\f':
			out = append(out, '\\', 'f')
		case '\r':
			out = append(out, '\\', 'r')
		default:
			if r < 32 {
				out = append(out, '\\', 'u', '0', '0', digits[r>>4], digits[r&15])
			} else {
				out = append(out, string(r)...)
			}
		}
	}
	return append(out, '"')
}
func appendValue(out []byte, value any) ([]byte, error) {
	switch v := value.(type) {
	case nil:
		return append(out, "null"...), nil
	case map[string]any:
		keys := make([]string, 0, len(v))
		for key := range v {
			keys = append(keys, key)
		}
		sort.Slice(keys, func(i, j int) bool {
			return slices.Compare(utf16.Encode([]rune(keys[i])), utf16.Encode([]rune(keys[j]))) < 0
		})
		out = append(out, '{')
		for i, key := range keys {
			if i > 0 {
				out = append(out, ',')
			}
			out = appendString(out, key)
			out = append(out, ':')
			var err error
			out, err = appendValue(out, v[key])
			if err != nil {
				return nil, err
			}
		}
		return append(out, '}'), nil
	case []any:
		out = append(out, '[')
		for i, item := range v {
			if i > 0 {
				out = append(out, ',')
			}
			var err error
			out, err = appendValue(out, item)
			if err != nil {
				return nil, err
			}
		}
		return append(out, ']'), nil
	case string:
		return appendString(out, v), nil
	case bool:
		return strconv.AppendBool(out, v), nil
	case float64:
		if math.IsNaN(v) || math.IsInf(v, 0) {
			return nil, strictjson.ErrInvalid
		}
		if v == 0 {
			return append(out, '0'), nil
		}
		format := byte('f')
		if math.Abs(v) < 1e-6 || math.Abs(v) >= 1e21 {
			format = 'e'
		}
		number := strconv.FormatFloat(v, format, -1, 64)
		if n := strings.IndexByte(number, 'e'); n >= 0 {
			exponent, _ := strconv.Atoi(number[n+1:])
			number = number[:n+1]
			if exponent >= 0 {
				number += "+"
			}
			number += strconv.Itoa(exponent)
		}
		return append(out, number...), nil
	default:
		return nil, strictjson.ErrInvalid
	}
}
