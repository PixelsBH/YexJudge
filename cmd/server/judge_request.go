package main

import (
	"encoding/json"
	"log"
	"net/http"
	"yexjudge/internal/judge"
)

func decodeJudgeJob(w http.ResponseWriter, r *http.Request) (judge.Job, bool) {
	var job judge.Job

	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)

	if err := json.NewDecoder(r.Body).Decode(&job); err != nil {
		http.Error(w, "Invalid request payload", http.StatusBadRequest)
		log.Println("failed to decode job:", err)
		return judge.Job{}, false
	}

	return job, true
}
