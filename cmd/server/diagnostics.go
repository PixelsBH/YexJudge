package main

import (
	"encoding/json"
	"net/http"
	"time"

	"yexjudge/internal/judge"
	"yexjudge/internal/observability"
)

var (
	runtimeMetrics *observability.Metrics
	runtimePool    *judge.ExecutorSandboxPool
)

type diagnosticsResponse struct {
	Timestamp   time.Time                                  `json:"timestamp"`
	Submissions judge.SubmissionCounts                     `json:"submissions"`
	Workers     diagnosticsWorkers                         `json:"workers"`
	Sandboxes   judge.PoolStats                            `json:"sandboxes"`
	Timings     map[string]observability.HistogramSnapshot `json:"timings"`
}

type diagnosticsWorkers struct {
	Busy int64 `json:"busy"`
}

func diagnosticsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	provider, ok := submissionStore.(judge.SubmissionStatsProvider)
	if !ok {
		writeAPIError(w, http.StatusServiceUnavailable, "diagnostics_unavailable", "submission statistics are unavailable")
		return
	}
	counts, err := provider.Counts()
	if err != nil {
		writeAPIError(w, http.StatusServiceUnavailable, "diagnostics_unavailable", "submission statistics are unavailable")
		return
	}

	metrics := runtimeMetrics
	if metrics == nil {
		metrics = observability.NewMetrics()
	}
	snapshot := metrics.Snapshot()
	poolStats := judge.PoolStats{}
	if runtimePool != nil {
		poolStats = runtimePool.Stats()
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(diagnosticsResponse{
		Timestamp:   time.Now().UTC(),
		Submissions: counts,
		Workers:     diagnosticsWorkers{Busy: snapshot.WorkerBusy},
		Sandboxes:   poolStats,
		Timings:     snapshot.Timings,
	})
}
