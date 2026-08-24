package judge

import (
	"context"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

func TestCompileBaseline(t *testing.T) {
	if !compileBenchmarkEnabled(t) {
		return
	}

	executor := NewDockerExecutor(&runner.DockerRunner{})
	runCompileBenchmark(t, "baseline", executor.Compile)
}

func TestCompileWorkerPoolBenchmark(t *testing.T) {
	if !compileBenchmarkEnabled(t) {
		return
	}

	executor := NewDockerExecutor(&runner.DockerRunner{})
	pool := NewCompileWorkerPool(2, executor.Compile)
	defer pool.Close()
	runCompileBenchmark(t, "worker_pool", pool.Compile)
}

func compileBenchmarkEnabled(t *testing.T) bool {
	if os.Getenv("YEXJUDGE_RUN_COMPILE_BENCHMARK") != "1" {
		t.Skip("set YEXJUDGE_RUN_COMPILE_BENCHMARK=1 to run the Docker compile benchmark")
		return false
	}
	return true
}

func compileBenchmarkIterations(t *testing.T) int {
	t.Helper()
	iterations := 6
	if raw := os.Getenv("YEXJUDGE_COMPILE_BENCHMARK_ITERATIONS"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 2 {
			t.Fatalf("YEXJUDGE_COMPILE_BENCHMARK_ITERATIONS must be an integer >= 2, got %q", raw)
		}
		iterations = parsed
	}
	return iterations
}

func runCompileBenchmark(t *testing.T, label string, compile CompileFunc) {
	t.Helper()
	iterations := compileBenchmarkIterations(t)
	job := Job{
		Language:   "cpp",
		SourceCode: "#include <bits/stdc++.h>\nint main() { std::vector<int> values(10000, 7); return values.front() == 7 ? 0 : 1; }\n",
	}
	limits := Limits{TimeLimitMs: 1000, MemoryLimitMb: 128}
	durations := make([]time.Duration, 0, iterations)

	for iteration := 1; iteration <= iterations; iteration++ {
		workspace, err := createWorkspace(job, languages.Cpp{})
		if err != nil {
			t.Fatalf("createWorkspace() iteration %d: %v", iteration, err)
		}

		started := time.Now()
		result, compileErr := compile(context.Background(), workspace, languages.Cpp{}, limits)
		elapsed := time.Since(started)
		_ = os.RemoveAll(workspace)
		if compileErr != nil {
			t.Fatalf("Compile() iteration %d: %v", iteration, compileErr)
		}
		if result == nil || result.ExitCode != 0 {
			t.Fatalf("Compile() iteration %d result = %+v, want exit code 0", iteration, result)
		}

		durations = append(durations, elapsed)
		t.Logf("%s iteration=%d wall_ms=%.2f runner_ms=%.2f", label, iteration, milliseconds(elapsed), milliseconds(result.TimeUsed))
	}

	warm := append([]time.Duration(nil), durations[1:]...)
	sort.Slice(warm, func(i, j int) bool { return warm[i] < warm[j] })
	var warmTotal time.Duration
	for _, duration := range warm {
		warmTotal += duration
	}
	t.Logf(
		"%s iterations=%d first_wall_ms=%.2f warm_avg_ms=%.2f warm_median_ms=%.2f",
		label,
		iterations,
		milliseconds(durations[0]),
		milliseconds(warmTotal/time.Duration(len(warm))),
		milliseconds(warm[len(warm)/2]),
	)
}

func milliseconds(duration time.Duration) float64 {
	return float64(duration) / float64(time.Millisecond)
}
