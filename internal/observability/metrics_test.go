package observability

import (
	"testing"
	"time"
)

func TestMetricsSnapshotTracksWorkersAndTimingBuckets(t *testing.T) {
	metrics := NewMetrics()
	metrics.WorkerStarted()
	metrics.ObserveQueueWait(75 * time.Millisecond)
	metrics.ObserveCompile(2 * time.Second)
	metrics.WorkerFinished()

	snapshot := metrics.Snapshot()
	if snapshot.WorkerBusy != 0 {
		t.Fatalf("worker busy = %d, want 0", snapshot.WorkerBusy)
	}
	queue := snapshot.Timings["queueWait"]
	if queue.Count != 1 || queue.SumMs != 75 {
		t.Fatalf("queue timing = %+v, want one 75ms observation", queue)
	}
	if snapshot.Timings["compile"].Buckets["5s"] != 1 {
		t.Fatalf("compile buckets = %+v, want 5s bucket", snapshot.Timings["compile"].Buckets)
	}
}
