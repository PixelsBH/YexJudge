package judge

import (
	"strings"
	"testing"
)

func TestDecodeSupervisorResults(t *testing.T) {
	t.Run("success and WA output", func(t *testing.T) {
		output := "{\"stdout\":\"answer\",\"stderr\":\"\",\"exitCode\":0,\"cpuMs\":1,\"maxRssKb\":512,\"timedOut\":false,\"outputLimit\":false}\n" +
			"{\"stdout\":\"different\",\"stderr\":\"\",\"exitCode\":0,\"cpuMs\":2,\"maxRssKb\":768,\"timedOut\":false,\"outputLimit\":false}\n"
		results, err := decodeSupervisorResults([]byte(output), 2)
		if err != nil || len(results) != 2 {
			t.Fatalf("decodeSupervisorResults() = %v, %v; want two results", results, err)
		}
		if results[0].Stdout != "answer" || results[1].Stdout != "different" || results[1].MemoryUsed != 768*1024 || results[0].TimeUsed != 1_000_000 {
			t.Fatalf("results = %+v", results)
		}
	})
	t.Run("nonzero exit stops result count", func(t *testing.T) {
		output := "{\"stdout\":\"partial\",\"stderr\":\"failed\",\"exitCode\":2,\"cpuMs\":3,\"maxRssKb\":1,\"timedOut\":false,\"outputLimit\":false}\n"
		results, err := decodeSupervisorResults([]byte(output), 4)
		if err != nil || len(results) != 1 || results[0].ExitCode != 2 {
			t.Fatalf("decodeSupervisorResults() = %v, %v", results, err)
		}
	})
	t.Run("timeout and output overflow", func(t *testing.T) {
		for _, flags := range []string{`"timedOut":true,"outputLimit":false`, `"timedOut":false,"outputLimit":true`} {
			output := `{"stdout":"","stderr":"","exitCode":124,"cpuMs":0,"maxRssKb":0,` + flags + "}\n"
			results, err := decodeSupervisorResults([]byte(output), 3)
			if err != nil || len(results) != 1 {
				t.Fatalf("decodeSupervisorResults(%s) = %v, %v", flags, results, err)
			}
		}
	})
}

func TestDecodeSupervisorResultsFailsClosed(t *testing.T) {
	cases := []struct {
		name, output string
		expected     int
	}{
		{"malformed", "{bad}\n", 1},
		{"missing success result", `{"stdout":"","stderr":"","exitCode":0,"cpuMs":0,"maxRssKb":0,"timedOut":false,"outputLimit":false}` + "\n", 2},
		{"negative measurements", `{"stdout":"","stderr":"","exitCode":0,"cpuMs":-1,"maxRssKb":0,"timedOut":false,"outputLimit":false}` + "\n", 1},
		{"trailing data", `{"stdout":"","stderr":"","exitCode":0,"cpuMs":0,"maxRssKb":0,"timedOut":false,"outputLimit":false}` + "\n{}\n", 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := decodeSupervisorResults([]byte(tc.output), tc.expected); err == nil || strings.TrimSpace(tc.output) == "" {
				t.Fatal("decodeSupervisorResults() succeeded, want fail-closed error")
			}
		})
	}
}

func TestCappedCommandBufferBoundsMemory(t *testing.T) {
	buffer := &cappedCommandBuffer{limit: 4}
	if n, err := buffer.Write([]byte("abcdefgh")); err != nil || n != 8 {
		t.Fatalf("Write() = %d, %v", n, err)
	}
	if buffer.String() != "abcd" || !buffer.exceeded {
		t.Fatalf("buffer = %q, exceeded=%t", buffer.String(), buffer.exceeded)
	}
}
