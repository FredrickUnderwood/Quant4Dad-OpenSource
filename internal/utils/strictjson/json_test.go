package strictjson

import "testing"

func TestStrictJSONObjectRejectsAmbiguousAndInvalidInput(t *testing.T) {
	for _, input := range []string{`{"a":1,"a":2}`, `{"a":1,"\u0061":2}`, `{"a":{"b":1,"b":2}}`, `{"a":[{"b":1,"b":2}]}`, `{"a":null}`, `{"a":"\ud800"}`, `{"a":"\udc00"}`, `{"a":1} {"a":2}`, `[]`, `null`, "{\"a\":\"\xff\"}"} {
		var value map[string]any
		if Decode([]byte(input), &value, 65536) == nil {
			t.Fatalf("accepted %q", input)
		}
	}
	for _, input := range []string{`{"a":"🐉"}`, `{"a":"\ud83d\udc09"}`, " {\"a\": [1,2,{\"b\":true}]} \n", `{"a":"\\ud800"}`} {
		var value map[string]any
		if err := Decode([]byte(input), &value, 65536); err != nil {
			t.Fatalf("rejected %q: %v", input, err)
		}
	}
	var typed struct {
		A int `json:"a"`
	}
	for _, input := range []string{`{"A":1}`, `{"a":1,"run_id":"forged"}`} {
		if Decode([]byte(input), &typed, 65536) == nil {
			t.Fatal("accepted unknown field")
		}
	}
}
