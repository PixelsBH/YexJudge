package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"yexjudge/internal/judge"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/observability"
	"yexjudge/internal/runner"

	_ "github.com/jackc/pgx/v5/stdlib"
)

var (
	judgeService    *judge.Service
	submissionStore judge.SubmissionStore
	submissionQueue judge.SubmissionQueue
	submitTimeout   = 10 * time.Second
)

func createSubmissionHandler(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()

	if r.Method != http.MethodPost {
		writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
		return
	}

	job, ok := decodeJudgeJob(w, r)
	if !ok {
		return
	}

	if err := judge.ValidateJob(job); err != nil {
		writeAPIError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}

	submission, err := createAndQueueSubmission(job)
	if err != nil {
		writeAPIError(w, http.StatusInternalServerError, "internal_error", "internal error")
		log.Println("failed to create submission:", err)
		return
	}

	response := judge.SubmissionAcceptedResponse{
		SubmissionID: submission.ID,
		Status:       submission.Status,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", "/submissions/"+submission.ID)
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Println("failed to encode submission response:", err)
	}
}

func createAndQueueSubmission(job judge.Job) (judge.Submission, error) {
	submission := judge.Submission{
		ID:     fmt.Sprintf("%d", time.Now().UnixNano()),
		Job:    job,
		Status: judge.SubmissionQueued,
	}

	if err := submissionStore.Save(submission); err != nil {
		return judge.Submission{}, err
	}

	if err := submissionQueue.Enqueue(submission.ID); err != nil {
		submission.Status = judge.SubmissionFailed
		if updateErr := submissionStore.Update(submission); updateErr != nil {
			return judge.Submission{}, fmt.Errorf("enqueue failed: %v; failed to update submission: %w", err, updateErr)
		}
		return judge.Submission{}, err
	}
	slog.Info("submission queued",
		"submission_id", submission.ID,
		"language", job.Language,
		"status", submission.Status,
	)

	return submission, nil
}

func submissionsCollectionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		createSubmissionHandler(w, r)
		return
	}

	writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
}

func startWorker(ctx context.Context, workerID int, workers *sync.WaitGroup) {
	workers.Add(1)
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			claim, err := submissionQueue.Dequeue(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Println("worker", workerID, "failed to dequeue submission:", err)
				continue
			}

			submission, ok := submissionStore.Get(claim.ID)
			if !ok {
				log.Println("worker", workerID, "submission not found in store:", claim.ID)
				continue
			}
			if submission.AttemptCount != claim.Attempt {
				log.Println("worker", workerID, "submission claim attempt mismatch:", claim.ID, claim.Attempt, submission.AttemptCount)
				continue
			}
			if submission.Status != judge.SubmissionQueued && submission.Status != judge.SubmissionRunning {
				slog.Warn("worker skipped submission", "submission_id", claim.ID, "worker_id", workerID, "status", submission.Status)
				continue
			}
			if runtimeMetrics != nil && submission.CreatedAt != nil {
				runtimeMetrics.ObserveQueueWait(time.Since(*submission.CreatedAt))
			}
			queueWaitMs := int64(0)
			if submission.CreatedAt != nil {
				queueWaitMs = time.Since(*submission.CreatedAt).Milliseconds()
			}
			slog.Info("submission claimed",
				"submission_id", claim.ID,
				"language", submission.Job.Language,
				"worker_id", workerID,
				"attempt", claim.Attempt,
				"from_status", submission.Status,
				"to_status", judge.SubmissionRunning,
				"queue_wait_ms", queueWaitMs,
			)
			processCtx, cancelProcess := context.WithCancel(judge.WithWorkerID(ctx, workerID))
			leaseCtx, cancelLease := context.WithCancel(ctx)
			leaseDone := make(chan struct{})
			leaseLost := make(chan error, 1)
			go renewSubmissionLease(leaseCtx, claim, leaseDone, cancelProcess, leaseLost)
			if runtimeMetrics != nil {
				runtimeMetrics.WorkerStarted()
			}
			if _, err := judgeService.ProcessSubmission(processCtx, submission); err != nil {
				slog.Error("worker failed to process submission", "submission_id", claim.ID, "worker_id", workerID, "error", err)
			}
			if runtimeMetrics != nil {
				runtimeMetrics.WorkerFinished()
			}
			cancelProcess()
			cancelLease()
			<-leaseDone
			select {
			case err := <-leaseLost:
				slog.Warn("submission processing stopped after lease loss", "submission_id", claim.ID, "worker_id", workerID, "error", err)
			default:
			}
		}
	}()
}

func renewSubmissionLease(ctx context.Context, claim judge.SubmissionClaim, done chan<- struct{}, cancelProcess context.CancelFunc, leaseLost chan<- error) {
	defer close(done)
	interval := submissionQueue.LeaseDuration() / 3
	if interval < time.Millisecond {
		interval = time.Millisecond
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := submissionQueue.RenewLease(ctx, claim); err != nil {
				slog.Error("failed to renew submission lease", "submission_id", claim.ID, "attempt", claim.Attempt, "error", err)
				select {
				case leaseLost <- err:
				default:
				}
				cancelProcess()
				return
			}
		}
	}
}

func startLeaseRecovery(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				recovered, err := submissionQueue.RecoverExpired(ctx)
				if err != nil {
					slog.Error("failed to recover expired submissions", "error", err)
				} else if recovered > 0 {
					slog.Info("recovered expired submissions", "count", recovered)
				}
			}
		}
	}()
}

func buildSandboxPool(executor judge.Executor, size int) ([]*judge.Sandbox, error) {
	sandboxes := make([]*judge.Sandbox, 0, size)
	for i := 0; i < size; i++ {
		sandbox, err := executor.StartSandbox(context.Background())
		if err != nil {
			for _, started := range sandboxes {
				executor.RemoveSandbox(started)
			}
			return nil, err
		}
		sandboxes = append(sandboxes, sandbox)
	}

	return sandboxes, nil
}

func cleanupSandboxes(executor judge.Executor, sandboxes []*judge.Sandbox) {
	for _, sandbox := range sandboxes {
		executor.RemoveSandbox(sandbox)
	}
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal("invalid configuration:", err)
	}
	submitTimeout = cfg.submitTimeout
	runtimeWorkerCapacity = cfg.workerCount
	runtimeCompileSlots = cfg.compileSlots
	cmdRunner := &runner.DockerRunner{}
	registry := languages.NewRegistry(
		languages.Cpp{},
		languages.C{},
		languages.Python{},
		languages.Go{},
		languages.Java{},
	)

	executor := judge.NewDockerExecutor(cmdRunner)

	sandboxes, err := buildSandboxPool(executor, cfg.sandboxPoolSize)
	if err != nil {
		log.Fatal("failed to build sandbox pool:", err)
	}
	runtimeMetrics = observability.NewMetrics()
	runtimePool = judge.NewExecutorSandboxPoolWithMetrics(executor, sandboxes, runtimeMetrics)
	defer runtimePool.Close()
	pool := runtimePool

	store, queue, cleanup, err := buildPersistence(cfg)
	if err != nil {
		runtimePool.Close()
		log.Fatal("failed to build persistence:", err)
	}
	defer cleanup()

	submissionStore = store
	submissionQueue = queue

	judgeService = judge.NewServiceWithMetricsAndCompileSlots(executor, pool, store, registry, runtimeMetrics, cfg.compileSlots)
	defer judgeService.Close()

	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	var workers sync.WaitGroup
	if recovered, err := submissionQueue.RecoverExpired(context.Background()); err != nil {
		cleanup()
		judgeService.Close()
		runtimePool.Close()
		log.Fatal("failed to recover expired submissions:", err)
	} else if recovered > 0 {
		log.Println("recovered expired submissions at startup:", recovered)
	}
	startLeaseRecovery(workerCtx, cfg.queueRecovery)

	for i := 1; i <= cfg.workerCount; i++ {
		startWorker(workerCtx, i, &workers)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/diagnostics", diagnosticsHandler)
	mux.HandleFunc("/judge", createSubmissionHandler)
	mux.HandleFunc("/submissions", submissionsCollectionHandler)
	mux.HandleFunc("/submissions/", submissionHandler)
	mux.HandleFunc("/submit", submitHandler)

	server := &http.Server{
		Addr:    ":" + cfg.port,
		Handler: requestIDMiddleware(mux),
	}

	shutdownDone := make(chan struct{})
	go func() {
		sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		<-sigCtx.Done()
		log.Println("shutdown signal received")

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Println("server shutdown error:", err)
		}

		cancelWorkers()
		submissionQueue.Close()
		workersDone := make(chan struct{})
		go func() {
			workers.Wait()
			close(workersDone)
		}()
		select {
		case <-workersDone:
			log.Println("workers stopped")
		case <-shutdownCtx.Done():
			log.Println("worker shutdown deadline reached; removing runtime sandboxes")
		}
		judgeService.Close()
		runtimePool.Close()

		close(shutdownDone)
	}()

	slog.Info("server started",
		"address", ":"+cfg.port,
		"workers", cfg.workerCount,
		"sandbox_pool_size", cfg.sandboxPoolSize,
		"compile_slots", cfg.compileSlots,
	)

	err = server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}

	<-shutdownDone
}

func buildPersistence(cfg config) (judge.SubmissionStore, judge.SubmissionQueue, func(), error) {
	if cfg.databaseURL == "" {
		return nil, nil, nil, fmt.Errorf("DATABASE_URL is required")
	}

	db, err := sql.Open("pgx", cfg.databaseURL)
	if err != nil {
		return nil, nil, nil, err
	}

	if err := db.Ping(); err != nil {
		db.Close()
		return nil, nil, nil, err
	}

	log.Println("using Postgres store and queue")

	store := judge.NewPostgresSubmissionStore(db)
	queue := judge.NewPostgresSubmissionQueueWithOptions(
		db,
		cfg.queuePoll,
		cfg.queueLease,
		cfg.queueMaxAttempts,
	)

	cleanup := func() {
		if err := db.Close(); err != nil {
			log.Println("failed to close database:", err)
		}
	}

	return store, queue, cleanup, nil
}
