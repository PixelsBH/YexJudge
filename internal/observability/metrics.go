package observability

import (
	"sync"
	"sync/atomic"
	"time"
)

type Metrics struct {
	workerBusy atomic.Int64
	queueWait  Histogram
	compile    Histogram
	acquire    Histogram
	staging    Histogram
	testcase   Histogram
	reset      Histogram
	runtime    Histogram
}

type Histogram struct {
	mu      sync.Mutex
	count   int64
	sum     time.Duration
	buckets []time.Duration
	counts  []int64
}

type HistogramSnapshot struct {
	Count   int64            `json:"count"`
	SumMs   float64          `json:"sumMs"`
	Buckets map[string]int64 `json:"buckets"`
}

type Snapshot struct {
	WorkerBusy int64                        `json:"workerBusy"`
	Timings    map[string]HistogramSnapshot `json:"timings"`
}

var defaultBuckets = []time.Duration{
	10 * time.Millisecond,
	50 * time.Millisecond,
	100 * time.Millisecond,
	250 * time.Millisecond,
	500 * time.Millisecond,
	1 * time.Second,
	5 * time.Second,
	10 * time.Second,
}

func NewMetrics() *Metrics {
	metrics := &Metrics{}
	metrics.queueWait.init()
	metrics.compile.init()
	metrics.acquire.init()
	metrics.staging.init()
	metrics.testcase.init()
	metrics.reset.init()
	metrics.runtime.init()
	return metrics
}

func (h *Histogram) init() {
	h.buckets = append([]time.Duration(nil), defaultBuckets...)
	h.counts = make([]int64, len(h.buckets)+1)
}

func (m *Metrics) WorkerStarted() { m.workerBusy.Add(1) }
func (m *Metrics) WorkerFinished() {
	m.workerBusy.Add(-1)
}

func (m *Metrics) ObserveQueueWait(value time.Duration) { m.queueWait.Observe(value) }
func (m *Metrics) ObserveCompile(value time.Duration)   { m.compile.Observe(value) }
func (m *Metrics) ObserveAcquire(value time.Duration)   { m.acquire.Observe(value) }
func (m *Metrics) ObserveStaging(value time.Duration)   { m.staging.Observe(value) }
func (m *Metrics) ObserveTestcase(value time.Duration)  { m.testcase.Observe(value) }
func (m *Metrics) ObserveReset(value time.Duration)     { m.reset.Observe(value) }
func (m *Metrics) ObserveRuntime(value time.Duration)   { m.runtime.Observe(value) }

func (h *Histogram) Observe(value time.Duration) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.count++
	h.sum += value
	for index, bucket := range h.buckets {
		if value <= bucket {
			h.counts[index]++
			return
		}
	}
	h.counts[len(h.counts)-1]++
}

func (h *Histogram) Snapshot() HistogramSnapshot {
	h.mu.Lock()
	defer h.mu.Unlock()

	buckets := make(map[string]int64, len(h.counts))
	for index, count := range h.counts[:len(h.buckets)] {
		buckets[h.buckets[index].String()] = count
	}
	buckets["+Inf"] = h.counts[len(h.counts)-1]
	return HistogramSnapshot{
		Count:   h.count,
		SumMs:   float64(h.sum) / float64(time.Millisecond),
		Buckets: buckets,
	}
}

func (m *Metrics) Snapshot() Snapshot {
	return Snapshot{
		WorkerBusy: m.workerBusy.Load(),
		Timings: map[string]HistogramSnapshot{
			"queueWait":      m.queueWait.Snapshot(),
			"compile":        m.compile.Snapshot(),
			"sandboxAcquire": m.acquire.Snapshot(),
			"staging":        m.staging.Snapshot(),
			"testcase":       m.testcase.Snapshot(),
			"sandboxReset":   m.reset.Snapshot(),
			"runtime":        m.runtime.Snapshot(),
		},
	}
}
