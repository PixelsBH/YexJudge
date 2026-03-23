package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"
	"yexjudge/internal/judge"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

var (
	judgeService    *judge.Service
	submissionStore judge.SubmissionStore
	submissionQueue *judge.MemorySubmissionQueue
)

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

	submission := judge.Submission{
		ID:     fmt.Sprintf("%d", time.Now().UnixNano()),
		Job:    job,
		Status: judge.SubmissionQueued,
	}

	if err := submissionStore.Save(submission); err != nil {
		http.Error(w, "internal error", http.StatusInternalServerError)
		log.Println("failed to save submission:", err)
		return
	}

	if err := submissionQueue.Enqueue(submission.ID); err != nil {
		submission.Status = judge.SubmissionFailed
		if updateErr := submissionStore.Update(submission); updateErr != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			log.Println("failed to mark submission as failed after enqueue error:", updateErr)
			return
		}
		http.Error(w, "submission queue is full", http.StatusServiceUnavailable)
		log.Println("failed to enqueue submission:", err)
		return
	}

	response := judge.SubmissionAcceptedResponse{
		SubmissionID: submission.ID,
		Status:       submission.Status,
	}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	json.NewEncoder(w).Encode(response)

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
	queue := judge.NewMemorySubmissionQueue(100)
	submissionQueue = queue

	executor := judge.NewDockerExecutor(cmdRunner)
	pool := judge.NewExecutorSandboxPool(executor)
	store := judge.NewMemorySubmissionStore()
	submissionStore = store

	judgeService = judge.NewService(executor, pool, store, registry)

	go func() {
		for submissionID := range submissionQueue.Channel() {
			submission, ok := submissionStore.Get(submissionID)
			if !ok {
				log.Println("submission not found in store:", submissionID)
				continue
			}

			if _, err := judgeService.ProcessSubmission(context.Background(), submission); err != nil {
				log.Println("failed to process submission:", submissionID, err)
			}
		}
	}()

	http.HandleFunc("/health", healthHandler)
	http.HandleFunc("/judge", judgeHandler)
	http.HandleFunc("/submissions/", submissionHandler)

	log.Println("YexJudge server running on :8080")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
