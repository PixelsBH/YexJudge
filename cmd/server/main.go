package main

import (
	"encoding/json"
	"log"
	"net/http"
	"yexjudge/internal/judge"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

var judgeService *judge.Service

func judgeHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	job, ok := decodeJudgeJob(w, r)

	if !ok {
		return
	}

	if err := judge.ValidateJob(job); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	result, err := judgeService.Judge(r.Context(), job)

	if err != nil {
		http.Error(w, "Execution failed", http.StatusInternalServerError)
		log.Println("judge service error:", err)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(result)
}

func main() {
	cmdRunner := &runner.DockerRunner{}
	registry := languages.NewRegistry(
		languages.Cpp{},
		languages.C{},
		languages.Python{},
		languages.Go{},
		languages.Java{},
	)

	executor := judge.NewDockerExecutor(cmdRunner)
	pool := judge.NewExecutorSandboxPool(executor)

	judgeService = judge.NewService(executor, pool, registry)

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/judge", judgeHandler)

	log.Println("YexJudge server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
