// Package strictjson validates bounded, unambiguous JSON request objects.
package strictjson

import (
	"bytes"
	"errors"
	"strconv"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/bytedance/sonic/ast"
)

var ErrInvalid = errors.New("invalid_json")
var decoder = sonic.Config{CaseSensitive: true, DisallowUnknownFields: true, UseNumber: true, ValidateString: true, UseUnicodeErrors: true}.Froze()

func Decode(body []byte, target any, maxBytes int) error {
	return decode(body, target, maxBytes, false)
}

// DecodeNullable keeps the same object, duplicate-key and depth constraints,
// but permits nested null values. Tool schemas decide which fields allow null;
// control requests continue to use Decode.
func DecodeNullable(body []byte, target any, maxBytes int) error {
	return decode(body, target, maxBytes, true)
}

func decode(body []byte, target any, maxBytes int, nullable bool) error {
	body = bytes.TrimSpace(body)
	if len(body) == 0 || len(body) > maxBytes || body[0] != '{' || !utf8.Valid(body) || !validEscapes(body) || !sonic.Valid(body) {
		return ErrInvalid
	}
	parser := ast.NewParser(string(body))
	node, code := parser.Parse()
	if code != 0 || !unique(&node, 0, nullable) || decoder.Unmarshal(body, target) != nil {
		return ErrInvalid
	}
	return nil
}
func unique(node *ast.Node, depth int, nullable bool) bool {
	if depth > 32 || (!nullable && node.Type() == ast.V_NULL) {
		return false
	}
	if node.Type() != ast.V_OBJECT && node.Type() != ast.V_ARRAY {
		return true
	}
	keys := map[string]bool{}
	valid := true
	err := node.ForEach(func(seq ast.Sequence, value *ast.Node) bool {
		if seq.Key != nil {
			if keys[*seq.Key] {
				valid = false
				return false
			}
			keys[*seq.Key] = true
		}
		if !unique(value, depth+1, nullable) {
			valid = false
			return false
		}
		return true
	})
	return err == nil && valid
}

// Sonic versions may replace an unpaired UTF-16 escape; reject before decoding.
func validEscapes(body []byte) bool {
	for i := 0; i < len(body); i++ {
		if body[i] != '\\' {
			continue
		}
		i++
		if i >= len(body) {
			return false
		}
		if body[i] != 'u' {
			continue
		}
		if i+4 >= len(body) {
			return false
		}
		n, err := strconv.ParseUint(string(body[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if n >= 0xdc00 && n <= 0xdfff {
			return false
		}
		if n >= 0xd800 && n <= 0xdbff {
			if i+6 >= len(body) || body[i+1] != '\\' || body[i+2] != 'u' {
				return false
			}
			low, err := strconv.ParseUint(string(body[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}
