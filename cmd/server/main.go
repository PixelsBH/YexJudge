package main

import (
	"context"
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

	"github.com/jackc/pgx/v5/stdlib"

	schema "yexjudge/db"
	"yexjudge/internal/judge"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/observability"
	"yexjudge/internal/runner"
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
	submission, err := createAndQueueSubmission(r, job)
	if err != nil {
		writeAdmissionError(w, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Location", "/submissions/"+submission.ID)
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(judge.SubmissionAcceptedResponse{SubmissionID: submission.ID, Status: submission.Status}); err != nil {
		slog.Warn("submission response write failed")
	}
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
			if ctx.Err() != nil {
				return
			}
			claim, err := submissionQueue.Dequeue(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				slog.Error("worker dequeue failed", "worker_id", workerID, "category", "persistence")
				continue
			}
			submission, ok := submissionStore.Get(claim.ID)
			if !ok {
				slog.Warn("claimed submission unavailable", "submission_id", claim.ID, "worker_id", workerID)
				continue
			}
			if submission.AttemptCount != claim.Attempt {
				slog.Warn("submission claim attempt mismatch", "submission_id", claim.ID, "worker_id", workerID)
				continue
			}
			if submission.Status != judge.SubmissionQueued && submission.Status != judge.SubmissionRunning {
				slog.Warn("worker skipped submission", "submission_id", claim.ID, "worker_id", workerID, "status", submission.Status)
				continue
			}
			queueWaitMs := int64(0)
			if submission.CreatedAt != nil {
				queueWaitMs = time.Since(*submission.CreatedAt).Milliseconds()
				if runtimeMetrics != nil {
					runtimeMetrics.ObserveQueueWait(time.Since(*submission.CreatedAt))
				}
			}
			slog.Info("submission claimed", "submission_id", claim.ID, "language", submission.Job.Language, "worker_id", workerID, "attempt", claim.Attempt, "from_status", submission.Status, "to_status", judge.SubmissionRunning, "queue_wait_ms", queueWaitMs)
			processCtx, cancelProcess := context.WithCancel(judge.WithWorkerID(ctx, workerID))
			leaseCtx, cancelLease := context.WithCancel(ctx)
			leaseDone := make(chan struct{})
			leaseLost := make(chan error, 1)
			go renewSubmissionLease(leaseCtx, claim, leaseDone, cancelProcess, leaseLost)
			if runtimeMetrics != nil {
				runtimeMetrics.WorkerStarted()
			}
			if _, err := judgeService.ProcessSubmission(processCtx, submission); err != nil {
				slog.Error("worker processing failed", "submission_id", claim.ID, "worker_id", workerID, "category", "processing_or_persistence")
			}
			if runtimeMetrics != nil {
				runtimeMetrics.WorkerFinished()
			}
			cancelProcess()
			cancelLease()
			<-leaseDone
			select {
			case <-leaseLost:
				slog.Warn("submission stopped after lease loss", "submission_id", claim.ID, "worker_id", workerID)
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
				slog.Error("submission lease renewal failed", "submission_id", claim.ID, "attempt", claim.Attempt, "category", "persistence_or_lease")
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
				if recovered, err := submissionQueue.RecoverExpired(ctx); err != nil {
					if ctx.Err() == nil {
						slog.Error("expired submission recovery failed", "category", "persistence")
					}
				} else if recovered > 0 {
					slog.Info("expired submissions recovered", "count", recovered)
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
			cleanupSandboxes(executor, sandboxes)
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

func serviceMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/ready", readinessHandler)
	mux.HandleFunc("/diagnostics", diagnosticsHandler)
	mux.HandleFunc("/judge", createSubmissionHandler)
	mux.HandleFunc("/submissions", submissionsCollectionHandler)
	mux.HandleFunc("/submissions/", submissionHandler)
	mux.HandleFunc("/submit", submitHandler)
	return mux
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	if os.Getenv("APP_ENV") != "production" {
		if err := loadEnvFile(".env"); err != nil {
			log.Fatal("failed to load local environment file")
		}
	}
	cfg, err := loadConfig()
	if err != nil {
		log.Fatal("invalid configuration: ", err)
	}
	submitTimeout = cfg.submitTimeout
	runtimeWorkerCapacity = cfg.workerCount
	runtimeCompileSlots = cfg.compileSlots
	runtimeSecurity = newAPISecurity(cfg.security)
	if cfg.security.insecureLocal {
		slog.Warn("unauthenticated development mode enabled; loopback access only")
	}

	// Refuse an invalid/outdated production schema before creating containers.
	store, queue, cleanup, err := buildPersistence(cfg)
	if err != nil {
		log.Fatal(err)
	}
	defer cleanup()
	submissionStore, submissionQueue = store, queue
	executor, err := judge.NewDockerExecutorWithOptions(&runner.DockerRunner{}, cfg.executorOptions)
	if err != nil {
		cleanup()
		log.Fatal("invalid execution configuration")
	}
	sandboxes, err := buildSandboxPool(executor, cfg.sandboxPoolSize)
	if err != nil {
		cleanup()
		log.Fatal("runtime sandbox initialization failed; check reviewed images, cgroups, and Docker permissions")
	}
	runtimeMetrics = observability.NewMetrics()
	runtimePool = judge.NewExecutorSandboxPoolWithMetrics(executor, sandboxes, runtimeMetrics)
	defer runtimePool.Close()
	registry := languages.NewRegistry(languages.Cpp{}, languages.C{}, languages.Python{}, languages.Go{}, languages.Java{})
	judgeService = judge.NewServiceWithMetricsAndCompileSlots(executor, runtimePool, store, registry, runtimeMetrics, cfg.compileSlots)
	defer judgeService.Close()

	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()
	var workers sync.WaitGroup
	if recovered, err := submissionQueue.RecoverExpired(workerCtx); err != nil {
		judgeService.Close()
		runtimePool.Close()
		cleanup()
		log.Fatal("startup lease recovery failed")
	} else if recovered > 0 {
		slog.Info("expired submissions recovered at startup", "count", recovered)
	}
	startLeaseRecovery(workerCtx, cfg.queueRecovery)
	startRetention(workerCtx, cfg.retentionDays)
	for i := 1; i <= cfg.workerCount; i++ {
		startWorker(workerCtx, i, &workers)
	}
	server := newHTTPServer(cfg, requestIDMiddleware(runtimeSecurity.middleware(serviceMux())))

	shutdownDone := make(chan struct{})
	go func() {
		sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()
		<-sigCtx.Done()
		slog.Info("shutdown signal received")
		serverDraining.Store(true)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			slog.Warn("HTTP shutdown deadline reached")
		}
		cancelWorkers()
		submissionQueue.Close()
		workersDone := make(chan struct{})
		go func() { workers.Wait(); close(workersDone) }()
		select {
		case <-workersDone:
			slog.Info("workers stopped")
		case <-shutdownCtx.Done():
			slog.Warn("worker shutdown deadline reached; removing runtime sandboxes")
		}
		judgeService.Close()
		runtimePool.Close()
		close(shutdownDone)
	}()
	slog.Info("server started", "address", server.Addr, "workers", cfg.workerCount, "sandbox_pool_size", cfg.sandboxPoolSize, "compile_slots", cfg.compileSlots, "authenticated", !cfg.security.insecureLocal, "accepting_submissions", cfg.security.acceptSubmissions)
	if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
		cancelWorkers()
		submissionQueue.Close()
		judgeService.Close()
		runtimePool.Close()
		cleanup()
		log.Fatal("HTTP listener failed")
	}
	<-shutdownDone
}

func buildPersistence(cfg config) (judge.SubmissionStore, judge.SubmissionQueue, func(), error) {
	connectionConfig := cfg.databaseConfig
	if connectionConfig == nil {
		var err error
		connectionConfig, err = databaseConnectionConfig(cfg.databaseURL, cfg.security.production)
		if err != nil {
			return nil, nil, nil, err
		}
	}
	sqlDB := stdlib.OpenDB(*connectionConfig)
	maxConnections := cfg.dbMaxOpenConns
	if maxConnections <= 0 {
		maxConnections = 16
	}
	sqlDB.SetMaxOpenConns(maxConnections)
	sqlDB.SetMaxIdleConns(maxConnections / 2)
	sqlDB.SetConnMaxLifetime(30 * time.Minute)
	sqlDB.SetConnMaxIdleTime(5 * time.Minute)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		sqlDB.Close()
		return nil, nil, nil, fmt.Errorf("database connection failed")
	}
	var schemaErr error
	if cfg.autoMigrate && !cfg.security.production {
		schemaErr = schema.Apply(ctx, sqlDB)
	} else {
		schemaErr = schema.Check(ctx, sqlDB)
	}
	if schemaErr != nil {
		sqlDB.Close()
		return nil, nil, nil, fmt.Errorf("database schema unavailable or outdated; review and run cmd/migrate with separate migration credentials")
	}
	slog.Info("PostgreSQL persistence initialized", "automatic_migrations", cfg.autoMigrate)
	store := judge.NewPostgresSubmissionStore(sqlDB)
	queue := judge.NewPostgresSubmissionQueueWithOptions(sqlDB, cfg.queuePoll, cfg.queueLease, cfg.queueMaxAttempts)
	cleanup := func() {
		if err := sqlDB.Close(); err != nil {
			slog.Warn("database close failed")
		}
	}
	return store, queue, cleanup, nil
}
