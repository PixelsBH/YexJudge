package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
	"yexjudge/internal/judge"
	"yexjudge/internal/runner"
)

type HealthResponse struct {
	Status string `json:"status"`
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	resp := HealthResponse{Status: "ok"}

	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(resp); err != nil {
		log.Println("failed to encode health response:", err)
	}
}

func judgeHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var job judge.Job

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		log.Println("failed to decode job:", err)
		return
	}

	// Create workspace
	workspace, err := os.MkdirTemp("", "yexjudge-*")
	if err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		log.Println("failed to create workspace:", err)
		return
	}
	defer os.RemoveAll(workspace)

	sourcePath := filepath.Join(workspace, "main.cpp")

	// Write source file
	if err := os.WriteFile(sourcePath, []byte(job.SourceCode), 0644); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		log.Println("failed to write source file:", err)
		return
	}

	runner := &runner.DockerRunner{}

	// Compile program
	ctxCompile, cancelCompile := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancelCompile()

	compileRes, err := runner.Run(
		ctxCompile,
		"",
		"docker",
		"run",
		"--rm",
		"-v", workspace+":/workspace",
		"gcc:13",
		"g++",
		"-O2",
		"-pipe",
		"-static",
		"-s",
		"/workspace/main.cpp",
		"-o",
		"/workspace/main",
	)

	if err != nil {
		http.Error(w, "Execution failed", http.StatusInternalServerError)
		log.Println("compile error:", err)
		return
	}

	if compileRes.ExitCode != 0 {
		result := judge.Result{
			Status:       judge.CompilationError,
			ErrorMessage: compileRes.Stderr,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(result)
		return
	}

	memoryLimit := fmt.Sprintf("%dm", job.Limits.MemoryLimitMb)
	maxRuntimeMs := 0

	//Persistent container creation
	containerName := fmt.Sprintf("yexjudge-%d", time.Now().UnixNano())

	ctxContainer, cancelContainer := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancelContainer()

	_, err = runner.Run(
		ctxContainer,
		"",
		"docker",
		"run",
		"-d",
		"--name", containerName,
		"--memory", memoryLimit,
		"--cpus", "1",
		"--network", "none",
		"--pids-limit", "64",
		"--security-opt", "no-new-privileges",
		"--tmpfs", "/tmp",
		"--workdir", "/workspace",
		"-v", workspace+":/workspace",
		"alpine",
		"sleep", "60",
	)

	if err != nil {
		http.Error(w, "Execution failed", http.StatusInternalServerError)
		log.Println("container start error:", err)
		return
	}

	defer runner.Run(
		context.Background(),
		"",
		"docker",
		"rm",
		"-f",
		containerName,
	)

	// Run test cases
	for _, tc := range job.TestCases {

		ctxRun, cancelRun := context.WithTimeout(
			r.Context(),
			time.Duration(job.Limits.TimeLimitMs)*time.Millisecond,
		)

		runRes, err := runner.Run(
			ctxRun,
			tc.Input+"\n",
			"docker",
			"exec",
			"-i",
			containerName,
			"/workspace/main",
		)

		cancelRun()

		log.Println("stdout:", runRes.Stdout)
		log.Println("stderr:", runRes.Stderr)
		log.Println("exitCode:", runRes.ExitCode)
		log.Println("timeout:", runRes.TimedOut)

		if err != nil {
			http.Error(w, "Execution failed", http.StatusInternalServerError)
			log.Println("runner error:", err)
			return
		}

		if runRes.TimedOut {
			result := judge.Result{
				Status:         judge.TimeLimitExceeded,
				FailedTestCase: &tc,
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
			return
		}

		if runRes.ExitCode != 0 {
			result := judge.Result{
				Status:         judge.RuntimeError,
				FailedTestCase: &tc,
				ErrorMessage:   runRes.Stderr,
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
			return
		}

		output := strings.TrimSpace(runRes.Stdout)
		expected := strings.TrimSpace(tc.ExpectedOutput)

		if output != expected {
			result := judge.Result{
				Status:         judge.WrongAnswer,
				FailedTestCase: &tc,
			}

			w.Header().Set("Content-Type", "application/json")
			json.NewEncoder(w).Encode(result)
			return
		}

		runtimeMs := int(runRes.TimeUsed.Milliseconds())
		if runtimeMs > maxRuntimeMs {
			maxRuntimeMs = runtimeMs
		}
	}
	// All tests passed
	result := judge.Result{
		Status:    judge.Accepted,
		RuntimeMs: maxRuntimeMs,
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func main() {
	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/judge", judgeHandler)

	log.Println("YexJudge server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
