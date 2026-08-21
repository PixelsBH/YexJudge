package main

import (
	"net/http"
	"yexjudge/internal/judge"
)

func decodeJudgeJob(w http.ResponseWriter, r *http.Request) (judge.Job, bool) {
	var job judge.Job

	if !decodeJSONBody(w, r, &job) {
		return judge.Job{}, false
	}

	return job, true
}
