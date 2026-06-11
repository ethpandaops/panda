package server

import "testing"

func TestFormatClickHouseParamValueScalars(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "nil", value: nil, want: ""},
		{name: "string", value: "network-a", want: "network-a"},
		{name: "true", value: true, want: "1"},
		{name: "false", value: false, want: "0"},
		{name: "number", value: 42, want: "42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatClickHouseParamValue(tt.value); got != tt.want {
				t.Fatalf("formatClickHouseParamValue(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}

func TestFormatClickHouseParamValueArrays(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "strings", value: []any{"0xabc", "0xdef"}, want: "['0xabc','0xdef']"},
		{name: "numbers", value: []any{float64(1), float64(2)}, want: "[1,2]"},
		{name: "bool and nil", value: []any{true, false, nil}, want: "[1,0,NULL]"},
		{name: "nested", value: []any{[]any{"a", "b"}}, want: "[['a','b']]"},
		{name: "escaping", value: []string{"quote's", `back\slash`}, want: `['quote\'s','back\\slash']`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := formatClickHouseParamValue(tt.value); got != tt.want {
				t.Fatalf("formatClickHouseParamValue(%v) = %q, want %q", tt.value, got, tt.want)
			}
		})
	}
}
