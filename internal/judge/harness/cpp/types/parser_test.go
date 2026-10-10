package types

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseTypeLengthLimit(t *testing.T) {
	valid := strings.Repeat("a", maxTypeLength)
	ref, err := Parse(valid)
	if err != nil {
		t.Fatalf("Parse() rejected a type at the %d-byte limit: %v", maxTypeLength, err)
	}
	if len(ref.Name) != maxTypeLength {
		t.Fatalf("parsed name length = %d, want %d", len(ref.Name), maxTypeLength)
	}

	if _, err := Parse(valid + "a"); err == nil || !strings.Contains(err.Error(), "maximum length of 4096 bytes") {
		t.Fatalf("Parse() error for over-limit input = %v, want a clear byte-length error", err)
	}
}

func TestParseTypeLengthLimitCountsUTF8Bytes(t *testing.T) {
	valid := strings.Repeat("é", maxTypeLength/len("é"))
	if len(valid) != maxTypeLength {
		t.Fatalf("test input length = %d bytes, want %d", len(valid), maxTypeLength)
	}
	if _, err := Parse(valid); err != nil {
		t.Fatalf("Parse() rejected a valid UTF-8 type at the byte limit: %v", err)
	}

	if _, err := Parse(valid + "é"); err == nil || !strings.Contains(err.Error(), "maximum length of 4096 bytes") {
		t.Fatalf("Parse() error for over-limit UTF-8 input = %v, want a clear byte-length error", err)
	}
}

func TestParseTypeTemplateDepthLimit(t *testing.T) {
	for _, depth := range []int{1, 8, maxTypeDepth} {
		t.Run(fmt.Sprintf("depth_%d", depth), func(t *testing.T) {
			if _, err := Parse(nestedVectorType(depth)); err != nil {
				t.Fatalf("Parse() rejected valid nesting depth %d: %v", depth, err)
			}
		})
	}

	if _, err := Parse(nestedVectorType(maxTypeDepth + 1)); err == nil || !strings.Contains(err.Error(), "maximum template nesting depth of 64") {
		t.Fatalf("Parse() error beyond depth limit = %v, want a clear nesting-depth error", err)
	}
}

func nestedVectorType(depth int) string {
	return strings.Repeat("vector<", depth) + "int" + strings.Repeat(">", depth)
}

func BenchmarkParseNestedType(b *testing.B) {
	for _, depth := range []int{8, 16, 32, maxTypeDepth} {
		b.Run(fmt.Sprintf("depth_%d", depth), func(b *testing.B) {
			declaredType := nestedVectorType(depth)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := Parse(declaredType); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
