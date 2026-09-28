package expr

// Operators is the public condition vocabulary. Nested operands use the same
// syntax; Parse and ValidateCondition check structure and types before saving.
func Operators() []string {
	return []string{"all", "any", "not", "gt", "lt", "gte", "lte", "eq", "cross_up", "cross_down", "has_position", "pnl_pct", "days_held", "ratio_from_entry"}
}

func Schema() map[string]any {
	operand := map[string]any{"anyOf": []any{
		map[string]any{"type": "string", "maxLength": 128, "description": "OHLCV field or declared indicator alias, optionally alias.output (e.g. macd.dif)"},
		map[string]any{"type": "number"}, map[string]any{"type": "boolean"},
		map[string]any{"type": "object", "additionalProperties": true, "minProperties": 1, "maxProperties": 1,
			"description": "Nested expression. has_position returns boolean; days_held, pnl_pct and ratio_from_entry return numbers. Example numeric comparison: {\"gte\":[{\"days_held\":null},1]}. A string days_held is an indicator alias, not a helper call."},
	}}
	properties := map[string]any{}
	for _, op := range Operators() {
		switch op {
		case "all", "any":
			properties[op] = map[string]any{"type": "array", "minItems": 1, "maxItems": 32, "items": operand}
		case "not", "ratio_from_entry":
			properties[op] = operand
		case "has_position", "pnl_pct", "days_held":
			properties[op] = map[string]any{"anyOf": []any{map[string]any{"type": "boolean", "const": true}, map[string]any{"type": "null"}}}
		default:
			properties[op] = map[string]any{"type": "array", "minItems": 2, "maxItems": 2, "items": operand}
		}
	}
	return map[string]any{"type": "object", "additionalProperties": false, "minProperties": 1, "maxProperties": 1, "properties": properties,
		"description": "Exactly one operator per object; the rule must return boolean. all/any/not require boolean operands, never bare numbers or numeric helpers. Numeric comparisons: {\"gt\":[\"close\",\"slow\"]}, {\"gte\":[{\"days_held\":null},1]}; eq also accepts two booleans. Crossovers: {\"cross_up\":[\"fast\",\"slow\"]}, {\"cross_down\":[\"fast\",\"slow\"]}. has_position is boolean; days_held/pnl_pct are numeric; helper arguments are null or true. ratio_from_entry takes an OHLCV field string. Do not use cross_above, cross_below or left/op/right."}
}
