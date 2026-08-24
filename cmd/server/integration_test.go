package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"yexjudge/internal/judge"

	_ "github.com/jackc/pgx/v5/stdlib"
)

func TestAPIIntegration(t *testing.T) {
	databaseURL := os.Getenv("YEXJUDGE_TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("YEXJUDGE_TEST_DATABASE_URL is not set")
	}
	if err := inspectDockerImage(judge.RuntimeSandboxImage); err != nil {
		t.Skipf("runtime Docker image is unavailable: %v", err)
	}
	if err := inspectDockerImage("gcc:13"); err != nil {
		t.Skipf("C/C++ compile Docker image is unavailable: %v", err)
	}
	if err := inspectDockerImage("golang:1.24-alpine"); err != nil {
		t.Skipf("Go compile Docker image is unavailable: %v", err)
	}
	if err := inspectDockerImage("eclipse-temurin:17-jdk"); err != nil {
		t.Skipf("Java compile Docker image is unavailable: %v", err)
	}

	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binaryPath := filepath.Join(t.TempDir(), "yexjudge-server")
	build := exec.Command("go", "build", "-o", binaryPath, "./cmd/server")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("server build failed: %v\n%s", err, output)
	}

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()

	staleID := fmt.Sprintf("phase4-stale-%d", time.Now().UnixNano())
	staleJob := judge.Job{
		Language:   "python",
		SourceCode: "print(3 + 4)",
		TestCases:  []judge.TestCase{{ID: 1, ExpectedOutput: "7"}},
		Limits:     judge.Limits{TimeLimitMs: 1000, MemoryLimitMb: 128},
	}
	staleJobJSON, err := json.Marshal(staleJob)
	if err != nil {
		t.Fatal(err)
	}
	db, err := sql.Open("pgx", databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`
		INSERT INTO submissions
		(id, status, job, attempt_count, started_at, lease_expires_at, failure_message)
		VALUES ($1, $2, $3, 1, NOW() - INTERVAL '2 minutes', NOW() - INTERVAL '1 second', $4)
		on conflict (id) do nothing`, staleID, judge.SubmissionRunning, staleJobJSON, "test worker interrupted"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	_ = db.Close()
	t.Cleanup(func() {
		cleanupDB, openErr := sql.Open("pgx", databaseURL)
		if openErr == nil {
			_, _ = cleanupDB.Exec(`DELETE FROM submissions WHERE id = $1`, staleID)
			_ = cleanupDB.Close()
		}
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	server := exec.CommandContext(ctx, binaryPath)
	server.Dir = root
	server.Env = append(os.Environ(),
		"DATABASE_URL="+databaseURL,
		fmt.Sprintf("PORT=%d", port),
		"WORKER_COUNT=1",
		"SANDBOX_POOL_SIZE=1",
		"QUEUE_POLL_INTERVAL_MS=50",
		// Keep the synchronous test budget below the HTTP client's five-second
		// timeout while allowing a cold compile to finish.
		"SUBMIT_TIMEOUT_MS=4000",
	)
	var serverLog bytes.Buffer
	server.Stdout = &serverLog
	server.Stderr = &serverLog
	if err := server.Start(); err != nil {
		t.Fatalf("server start failed: %v", err)
	}
	defer func() {
		if server.Process != nil {
			_ = server.Process.Signal(os.Interrupt)
			wait := make(chan error, 1)
			go func() { wait <- server.Wait() }()
			select {
			case <-wait:
			case <-time.After(10 * time.Second):
				_ = server.Process.Kill()
				<-wait
			}
		}
	}()

	baseURL := fmt.Sprintf("http://127.0.0.1:%d", port)
	client := &http.Client{Timeout: 5 * time.Second}
	waitForHealth(t, client, baseURL+"/health", &serverLog)

	t.Run("readiness", func(t *testing.T) {
		response, err := client.Get(baseURL + "/ready")
		if err != nil {
			t.Fatalf("GET /ready failed: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET /ready returned %d; log:\n%s", response.StatusCode, serverLog.String())
		}
		var readiness ReadinessResponse
		if err := json.NewDecoder(response.Body).Decode(&readiness); err != nil {
			t.Fatalf("decode readiness response: %v", err)
		}
		if readiness.Status != "ready" || readiness.Checks["postgres"] != "ready" || readiness.Checks["runtime"] != "ready" {
			t.Fatalf("readiness response = %+v, want all dependencies ready", readiness)
		}
	})

	t.Run("diagnostics", func(t *testing.T) {
		response, err := client.Get(baseURL + "/diagnostics")
		if err != nil {
			t.Fatalf("GET /diagnostics failed: %v", err)
		}
		defer response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Fatalf("GET /diagnostics returned %d; log:\n%s", response.StatusCode, serverLog.String())
		}
		var diagnostics diagnosticsResponse
		if err := json.NewDecoder(response.Body).Decode(&diagnostics); err != nil {
			t.Fatalf("decode diagnostics response: %v", err)
		}
		if diagnostics.Sandboxes.Total != 1 || diagnostics.Sandboxes.Available < 0 {
			t.Fatalf("diagnostics sandboxes = %+v, want one configured sandbox", diagnostics.Sandboxes)
		}
	})

	t.Run("startup recovers stale running submission", func(t *testing.T) {
		result := waitForIntegrationSubmission(t, client, baseURL+"/submissions/"+staleID, &serverLog)
		if result.Status != judge.SubmissionFinished || result.Result == nil || result.Result.Status != judge.Accepted {
			t.Fatalf("recovered result = %+v, want finished accepted; log:\n%s", result, serverLog.String())
		}
	})

	var submissionIDs []string
	t.Cleanup(func() {
		db, err := sql.Open("pgx", databaseURL)
		if err != nil {
			return
		}
		defer db.Close()
		for _, id := range submissionIDs {
			_, _ = db.Exec(`DELETE FROM submissions WHERE id = $1`, id)
		}
	})

	t.Run("async submission and polling", func(t *testing.T) {
		id := postSubmission(t, client, baseURL+"/submissions", map[string]any{
			"language":   "python",
			"sourceCode": "print(2 + 3)",
			"testCases": []map[string]any{{
				"id":             1,
				"expectedOutput": "5",
			}},
			"limits": map[string]any{"timeLimitMs": 1000, "memoryLimitMb": 128},
		})
		submissionIDs = append(submissionIDs, id)
		result := waitForIntegrationSubmission(t, client, baseURL+"/submissions/"+id, &serverLog)
		if result.Status != judge.SubmissionFinished || result.Result == nil || result.Result.Status != judge.Accepted {
			t.Fatalf("submission result = %+v, want finished accepted; log:\n%s", result, serverLog.String())
		}
	})

	t.Run("all stdin languages", func(t *testing.T) {
		tests := []struct {
			name     string
			language string
			source   string
		}{
			{name: "c", language: "c", source: "#include <stdio.h>\nint main(void) { printf(\"5\\n\"); return 0; }"},
			{name: "cpp", language: "cpp", source: "#include <iostream>\nint main() { std::cout << 5 << '\\n'; }"},
			{name: "python", language: "python", source: "print(2 + 3)"},
			{name: "go", language: "go", source: "package main\nimport \"fmt\"\nfunc main() { fmt.Println(5) }"},
			{name: "java", language: "java", source: "public class Main { public static void main(String[] args) { System.out.println(5); } }"},
		}
		for _, test := range tests {
			t.Run(test.name, func(t *testing.T) {
				id := postSubmission(t, client, baseURL+"/submissions", map[string]any{
					"language":   test.language,
					"sourceCode": test.source,
					"testCases":  []map[string]any{{"id": 1, "expectedOutput": "5"}},
					"limits":     map[string]any{"timeLimitMs": 1000, "memoryLimitMb": 128},
				})
				submissionIDs = append(submissionIDs, id)
				result := waitForIntegrationSubmission(t, client, baseURL+"/submissions/"+id, &serverLog)
				if result.Status != judge.SubmissionFinished || result.Result == nil || result.Result.Status != judge.Accepted {
					t.Fatalf("%s result = status=%s result=%+v, want finished accepted; log:\n%s", test.language, result.Status, result.Result, serverLog.String())
				}
			})
		}
	})

	t.Run("synchronous submit", func(t *testing.T) {
		response := postAndDecode(t, client, baseURL+"/submit", map[string]any{
			"language":   "python",
			"sourceCode": "print(8 + 1)",
			"testCases":  []map[string]any{{"id": 1, "expectedOutput": "9"}},
			"limits":     map[string]any{"timeLimitMs": 1000, "memoryLimitMb": 128},
		})
		var result judge.SubmissionResponse
		decodeJSON(t, response, &result)
		if result.Status != judge.SubmissionFinished || result.Result == nil || result.Result.Status != judge.Accepted {
			t.Fatalf("/submit response = %+v, want finished accepted; log:\n%s", result, serverLog.String())
		}
		submissionIDs = append(submissionIDs, result.ID)
	})

	t.Run("C++ function mode", func(t *testing.T) {
		id := postSubmission(t, client, baseURL+"/submissions", map[string]any{
			"language":   "cpp",
			"mode":       "function",
			"sourceCode": "#include <bits/stdc++.h>\nusing namespace std;\nclass Solution { public: int maxValue(vector<int>& values) { return *max_element(values.begin(), values.end()); } };",
			"function": map[string]any{
				"name":       "maxValue",
				"returnType": "int",
				"params":     []map[string]any{{"name": "values", "type": "vector<int>&"}},
			},
			"testCases": []map[string]any{{
				"id":       1,
				"args":     []any{[]int{1, 7, 3}},
				"expected": 7,
			}},
			"limits": map[string]any{"timeLimitMs": 1000, "memoryLimitMb": 128},
		})
		submissionIDs = append(submissionIDs, id)
		result := waitForIntegrationSubmission(t, client, baseURL+"/submissions/"+id, &serverLog)
		if result.Status != judge.SubmissionFinished || result.Result == nil || result.Result.Status != judge.Accepted {
			t.Fatalf("function-mode result = status=%s result=%+v, want finished accepted; log:\n%s", result.Status, result.Result, serverLog.String())
		}
	})

	t.Run("C++ class mode", func(t *testing.T) {
		response := postAndDecode(t, client, baseURL+"/submit", map[string]any{
			"language":   "cpp",
			"mode":       "class",
			"sourceCode": "class Counter { int value; public: Counter(int initial) : value(initial) {} void add(int amount) { value += amount; } int get() { return value; } };",
			"class": map[string]any{
				"name": "Counter",
				"constructor": map[string]any{
					"params": []map[string]any{{"name": "initial", "type": "int"}},
				},
				"operations": []map[string]any{
					{"name": "add", "returnType": "void", "params": []map[string]any{{"name": "amount", "type": "int"}}},
					{"name": "get", "returnType": "int", "params": []map[string]any{}},
				},
			},
			"testCases": []map[string]any{{
				"id":              1,
				"constructorArgs": []any{3},
				"operations": []map[string]any{
					{"name": "add", "args": []any{4}},
					{"name": "get", "args": []any{}},
				},
				"expected": []any{nil, 7},
			}},
			"limits": map[string]any{"timeLimitMs": 1000, "memoryLimitMb": 128},
		})
		var result judge.SubmissionResponse
		decodeJSON(t, response, &result)
		if result.Status != judge.SubmissionFinished || result.Result == nil || result.Result.Status != judge.Accepted {
			t.Fatalf("class-mode response = status=%s result=%+v, want finished accepted; log:\n%s", result.Status, result.Result, serverLog.String())
		}
		submissionIDs = append(submissionIDs, result.ID)
	})

	t.Run("C++ LRU Cache class mode", func(t *testing.T) {
		response := postAndDecode(t, client, baseURL+"/submit", map[string]any{
			"language": "cpp",
			"mode":     "class",
			"sourceCode": `class LRUCache {
	int capacity;
	list<pair<int, int>> items;
	unordered_map<int, list<pair<int, int>>::iterator> index;
public:
	LRUCache(int capacity) : capacity(capacity) {}
	int get(int key) {
		auto found = index.find(key);
		if (found == index.end()) return -1;
		items.splice(items.begin(), items, found->second);
		return found->second->second;
	}
	void put(int key, int value) {
		if (capacity <= 0) return;
		auto found = index.find(key);
		if (found != index.end()) {
			found->second->second = value;
			items.splice(items.begin(), items, found->second);
			return;
		}
		items.emplace_front(key, value);
		index[key] = items.begin();
		if (static_cast<int>(index.size()) > capacity) {
			auto leastRecent = prev(items.end());
			index.erase(leastRecent->first);
			items.pop_back();
		}
	}
};`,
			"class": map[string]any{
				"name": "LRUCache",
				"constructor": map[string]any{
					"params": []map[string]any{{"name": "capacity", "type": "int"}},
				},
				"operations": []map[string]any{
					{"name": "get", "returnType": "int", "params": []map[string]any{{"name": "key", "type": "int"}}},
					{"name": "put", "returnType": "void", "params": []map[string]any{{"name": "key", "type": "int"}, {"name": "value", "type": "int"}}},
				},
			},
			"testCases": []map[string]any{
				{
					"id":              1,
					"constructorArgs": []any{2},
					"operations": []map[string]any{
						{"name": "put", "args": []any{1, 1}},
						{"name": "put", "args": []any{2, 2}},
						{"name": "get", "args": []any{1}},
						{"name": "put", "args": []any{3, 3}},
						{"name": "get", "args": []any{2}},
						{"name": "put", "args": []any{4, 4}},
						{"name": "get", "args": []any{1}},
						{"name": "get", "args": []any{3}},
						{"name": "get", "args": []any{4}},
					},
					"expected": []any{nil, nil, 1, nil, -1, nil, -1, 3, 4},
				},
				{
					"id":              2,
					"constructorArgs": []any{1},
					"operations": []map[string]any{
						{"name": "put", "args": []any{10, 10}},
						{"name": "get", "args": []any{10}},
						{"name": "put", "args": []any{20, 20}},
						{"name": "get", "args": []any{10}},
						{"name": "get", "args": []any{20}},
					},
					"expected": []any{nil, 10, nil, -1, 20},
				},
			},
			"limits": map[string]any{"timeLimitMs": 1000, "memoryLimitMb": 128},
		})
		var result judge.SubmissionResponse
		decodeJSON(t, response, &result)
		if result.Status != judge.SubmissionFinished || result.Result == nil || result.Result.Status != judge.Accepted {
			t.Fatalf("LRU class-mode response = status=%s result=%+v, want finished accepted; log:\n%s", result.Status, result.Result, serverLog.String())
		}
		submissionIDs = append(submissionIDs, result.ID)
	})

	t.Run("synchronous submit timeout", func(t *testing.T) {
		response := postAndDecode(t, client, baseURL+"/submit", map[string]any{
			"language":   "python",
			"sourceCode": "import time; time.sleep(5)",
			"testCases":  []map[string]any{{"id": 1, "expectedOutput": ""}},
			"limits":     map[string]any{"timeLimitMs": 10000, "memoryLimitMb": 128},
		})
		var result judge.SubmissionAcceptedResponse
		decodeJSON(t, response, &result)
		if result.SubmissionID == "" || result.Status == judge.SubmissionFinished {
			t.Fatalf("/submit timeout response = %+v, want accepted response; log:\n%s", result, serverLog.String())
		}
		submissionIDs = append(submissionIDs, result.SubmissionID)
	})

	t.Run("compilation error", func(t *testing.T) {
		id := postSubmission(t, client, baseURL+"/submissions", map[string]any{
			"language":   "cpp",
			"sourceCode": "int main( {",
			"testCases":  []map[string]any{{"id": 1, "input": "", "expectedOutput": ""}},
			"limits":     map[string]any{"timeLimitMs": 1000, "memoryLimitMb": 128},
		})
		submissionIDs = append(submissionIDs, id)
		result := waitForIntegrationSubmission(t, client, baseURL+"/submissions/"+id, &serverLog)
		if result.Status != judge.SubmissionFinished || result.Result == nil || result.Result.Status != judge.CompilationError {
			t.Fatalf("compilation result = %+v, want finished compilation_error; log:\n%s", result, serverLog.String())
		}
	})
}

func inspectDockerImage(image string) error {
	return exec.Command("docker", "image", "inspect", image).Run()
}

func waitForHealth(t *testing.T, client *http.Client, url string, serverLog *bytes.Buffer) {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("server did not become healthy; log:\n%s", serverLog.String())
}

func postSubmission(t *testing.T, client *http.Client, url string, payload map[string]any) string {
	t.Helper()
	response := postAndDecode(t, client, url, payload)
	var accepted judge.SubmissionAcceptedResponse
	decodeJSON(t, response, &accepted)
	if accepted.SubmissionID == "" {
		t.Fatalf("submission response has no ID: %s", string(response))
	}
	return accepted.SubmissionID
}

func postAndDecode(t *testing.T, client *http.Client, url string, payload map[string]any) []byte {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	request, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("POST %s failed: %v", url, err)
	}
	defer response.Body.Close()
	result, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		t.Fatalf("POST %s returned %d: %s", url, response.StatusCode, result)
	}
	return result
}

func waitForIntegrationSubmission(t *testing.T, client *http.Client, url string, serverLog *bytes.Buffer) judge.SubmissionResponse {
	t.Helper()
	deadline := time.Now().Add(45 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			body, readErr := io.ReadAll(response.Body)
			_ = response.Body.Close()
			if readErr == nil && response.StatusCode == http.StatusOK {
				var result judge.SubmissionResponse
				if json.Unmarshal(body, &result) == nil && (result.Status == judge.SubmissionFinished || result.Status == judge.SubmissionFailed) {
					return result
				}
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("submission did not finish; log:\n%s", serverLog.String())
	return judge.SubmissionResponse{}
}

func decodeJSON(t *testing.T, body []byte, target any) {
	t.Helper()
	if err := json.Unmarshal(body, target); err != nil {
		t.Fatalf("decode response %q: %v", strings.TrimSpace(string(body)), err)
	}
}
