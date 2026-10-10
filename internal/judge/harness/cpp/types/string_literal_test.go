package types

import (
	"encoding/json"
	"testing"
)

func TestStringLiteralPreservesCXXBytes(t *testing.T) {
	tests := []struct {
		name string
		raw  json.RawMessage
		want string
	}{
		{
			name: "embedded NUL",
			raw:  json.RawMessage(`"a\u0000b"`),
			want: `std::string("a\000b", 3)`,
		},
		{
			name: "control byte followed by hex digit",
			raw:  json.RawMessage(`"\u0001f"`),
			want: `std::string("\001f", 2)`,
		},
		{
			name: "ASCII quote and backslash",
			raw:  json.RawMessage(`"\"\\"`),
			want: `std::string("\"\\", 2)`,
		},
		{
			name: "Unicode UTF-8 bytes",
			raw:  json.RawMessage(`"é☃"`),
			want: `std::string("\303\251\342\230\203", 5)`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := stringLiteral(test.raw)
			if err != nil {
				t.Fatalf("stringLiteral() error = %v", err)
			}
			if got != test.want {
				t.Errorf("stringLiteral() = %q, want %q", got, test.want)
			}
		})
	}
}
