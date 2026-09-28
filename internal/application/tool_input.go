package application

import (
	"math"
	"reflect"
	"regexp"
	"slices"
	"unicode/utf8"

	"github.com/bytedance/sonic"
	"github.com/quant4dad/internal/service"
	"github.com/quant4dad/internal/utils/tooljson"
)

// ValidateInput returns the exact canonical bytes used for hashing and dispatch.
// Schemas are server-owned and normalized at registration; caller keys never
// choose a schema, reference, Profile or execution context.
func (a *ToolCatalogApplication) ValidateInput(profile, name string, body []byte) ([]byte, error) {
	t, ok := a.tools[name]
	if !ok || !slices.Contains(t.Profiles, profile) {
		return nil, ErrToolForbidden
	}
	if len(body) == 0 {
		body = []byte("{}")
	}
	canonical, err := tooljson.Canonical(body)
	if err != nil {
		return nil, service.ErrToolInput
	}
	var value any
	if sonic.Unmarshal(canonical, &value) != nil || !matchesToolSchema(t.InputSchema, value) {
		return nil, service.ErrToolInput
	}
	return canonical, nil
}
func matchesToolSchema(schema map[string]any, value any) bool {
	if options, ok := schema["oneOf"].([]any); ok {
		matches := 0
		for _, option := range options {
			if child, ok := option.(map[string]any); ok && matchesToolSchema(child, value) {
				matches++
			}
		}
		return matches == 1
	}
	if options, ok := schema["anyOf"].([]any); ok {
		for _, option := range options {
			if child, ok := option.(map[string]any); ok && matchesToolSchema(child, value) {
				return true
			}
		}
		return false
	}
	if constant, ok := schema["const"]; ok && !reflect.DeepEqual(constant, value) {
		return false
	}
	if values, ok := schema["enum"].([]any); ok && !slices.ContainsFunc(values, func(v any) bool { return reflect.DeepEqual(v, value) }) {
		return false
	}
	switch schema["type"] {
	case "object":
		object, ok := value.(map[string]any)
		if !ok {
			return false
		}
		if min, ok := schema["minProperties"].(float64); ok && float64(len(object)) < min {
			return false
		}
		if max, ok := schema["maxProperties"].(float64); ok && float64(len(object)) > max {
			return false
		}
		properties, _ := schema["properties"].(map[string]any)
		if required, ok := schema["required"].([]any); ok {
			for _, key := range required {
				if _, exists := object[key.(string)]; !exists {
					return false
				}
			}
		}
		for key, child := range object {
			def, ok := properties[key].(map[string]any)
			if !ok {
				if extra, ok := schema["additionalProperties"].(map[string]any); ok {
					if !matchesToolSchema(extra, child) {
						return false
					}
					continue
				}
				if schema["additionalProperties"] == true {
					continue
				}
				return false
			}
			if !matchesToolSchema(def, child) {
				return false
			}
		}
		return true
	case "string":
		text, ok := value.(string)
		if !ok {
			return false
		}
		if max, ok := schema["maxLength"].(float64); ok && float64(utf8.RuneCountInString(text)) > max {
			return false
		}
		if min, ok := schema["minLength"].(float64); ok && float64(utf8.RuneCountInString(text)) < min {
			return false
		}
		if pattern, ok := schema["pattern"].(string); ok {
			valid, err := regexp.MatchString(pattern, text)
			if err != nil || !valid {
				return false
			}
		}
		if values, ok := schema["enum"].([]any); ok && !slices.Contains(values, any(text)) {
			return false
		}
		return true
	case "integer", "number":
		number, ok := value.(float64)
		if !ok || math.IsNaN(number) || math.IsInf(number, 0) || math.Abs(number) > 9007199254740991 || (schema["type"] == "integer" && math.Trunc(number) != number) {
			return false
		}
		if min, ok := schema["minimum"].(float64); ok && number < min {
			return false
		}
		if max, ok := schema["maximum"].(float64); ok && number > max {
			return false
		}
		return true
	case "boolean":
		_, ok := value.(bool)
		return ok
	case "null":
		return value == nil
	case "array":
		items, ok := value.([]any)
		if !ok {
			return false
		}
		if max, ok := schema["maxItems"].(float64); ok && float64(len(items)) > max {
			return false
		}
		if min, ok := schema["minItems"].(float64); ok && float64(len(items)) < min {
			return false
		}
		child, ok := schema["items"].(map[string]any)
		if !ok {
			return false
		}
		for i, item := range items {
			if !matchesToolSchema(child, item) {
				return false
			}
			if schema["uniqueItems"] == true {
				for _, previous := range items[:i] {
					if reflect.DeepEqual(previous, item) {
						return false
					}
				}
			}
		}
		return true
	default:
		return false
	}
}
