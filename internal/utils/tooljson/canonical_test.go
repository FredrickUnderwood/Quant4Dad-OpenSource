package tooljson

import (
	"math"
	"strconv"
	"testing"
)

func TestToolCanonicalUnicodeNumbersAndRejections(t *testing.T) {
	input := []byte(` {"\ue000":2,"😀":1,"z":[-0,1e+21,1e-6,1e-7,333333333.33333329],"s":"<>&\u2028\u000f\n"} `)
	got, err := Canonical(input)
	want := "{\"s\":\"<>&\u2028\\u000f\\n\",\"z\":[0,1e+21,0.000001,1e-7,333333333.3333333],\"😀\":1,\"\ue000\":2}"
	if err != nil || string(got) != want {
		t.Fatalf("canonical mismatch: %q %v", got, err)
	}
	h1, _ := Hash([]byte(`{"a":1,"b":2}`))
	h2, _ := Hash([]byte(`{"b":2.0,"a":1e0}`))
	if h1 != h2 {
		t.Fatal("equivalent numbers/order hash differently")
	}
	for _, raw := range []string{`null`, `{"a":1,"\u0061":2}`, `{"s":"\ud800"}`, `{"n":1e999}`, `[]`, `{"n":NaN}`, `{"x":1} {}`} {
		if _, err := Canonical([]byte(raw)); err == nil {
			t.Fatal("accepted invalid object", raw)
		}
	}
	if got, err := Canonical([]byte(`{"z":[null],"condition":null}`)); err != nil || string(got) != `{"condition":null,"z":[null]}` {
		t.Fatalf("nullable business fields: %s %v", got, err)
	}
	// RFC 8785 Appendix B edge cases, interpreted as IEEE-754 binary64.
	for bits, want := range map[uint64]string{0x8000000000000000: "0", 1: "5e-324", 0x7fefffffffffffff: "1.7976931348623157e+308", 0x4430000000000000: "295147905179352830000", 0x44b52d02c7e14af5: "9.999999999999997e+22", 0x44b52d02c7e14af6: "1e+23", 0x44b52d02c7e14af7: "1.0000000000000001e+23", 0x444b1ae4d6e2ef4e: "999999999999999700000", 0x444b1ae4d6e2ef4f: "999999999999999900000", 0x444b1ae4d6e2ef50: "1e+21", 0x3eb0c6f7a0b5ed8c: "9.999999999999997e-7", 0x3eb0c6f7a0b5ed8d: "0.000001"} {
		f := math.Float64frombits(bits)
		raw := `{"n":` + strconv.FormatFloat(f, 'g', -1, 64) + `}`
		got, err := Canonical([]byte(raw))
		if err != nil || string(got) != `{"n":`+want+`}` {
			t.Fatalf("number %x mismatch: %s", bits, got)
		}
	}
}
