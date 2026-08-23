package main

import (
	"encoding/json"
	"net/http"
	"time"

	"yexjudge/internal/judge"
	"yexjudge/internal/observability"
)

var (
	runtimeMetrics        *observability.Metrics
	runtimePool           *judge.ExecutorSandboxPool
	runtimeWorkerCapacity int
	runtimeCompileSlots   int
)

type diagnosticsResponse struct {
	Timestamp   time.Time                                  `json:"timestamp"`
	Submissions judge.SubmissionCounts                     `json:"submissions"`
	Capacity    diagnosticsCapacity                        `json:"capacity"`
	Workers     diagnosticsWorkers                         `json:"workers"`
	Sandboxes   judge.PoolStats                            `json:"sandboxes"`
	Timings     map[string]observability.HistogramSnapshot `json:"timings"`
}

type diagnosticsWorkers struct {
	Busy  int64 `json:"busy"`
	Total int   `json:"total"`
}

type diagnosticsCapacity struct {
	CompileSlots int `json:"compileSlots"`
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
		Capacity:    diagnosticsCapacity{CompileSlots: runtimeCompileSlots},
		Workers:     diagnosticsWorkers{Busy: snapshot.WorkerBusy, Total: runtimeWorkerCapacity},
		Sandboxes:   poolStats,
		Timings:     snapshot.Timings,
	})
}
