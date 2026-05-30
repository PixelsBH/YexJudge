package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"yexjudge/internal/judge"
	"yexjudge/internal/judge/languages"
	"yexjudge/internal/runner"
)

var (
	judgeService    *judge.Service
	submissionStore judge.SubmissionStore
	submissionQueue judge.SubmissionQueue
)

func createSubmissionHandler(w http.ResponseWriter, r *http.Request) {
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
	w.Header().Set("Location", "/submissions/"+submission.ID)
	w.WriteHeader(http.StatusAccepted)
	if err := json.NewEncoder(w).Encode(response); err != nil {
		log.Println("failed to encode submission response:", err)
	}
}

func submissionsCollectionHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		createSubmissionHandler(w, r)
		return
	}

	http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
}

func startWorker(ctx context.Context, workerID int) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			default:
			}

			submissionID, err := submissionQueue.Dequeue(ctx)
			if err != nil {
				if ctx.Err() != nil {
					return
				}
				log.Println("worker", workerID, "failed to dequeue submission:", err)
				continue
			}

			submission, ok := submissionStore.Get(submissionID)
			if !ok {
				log.Println("worker", workerID, "submission not found in store:", submissionID)
				continue
			}
			if submission.Status != judge.SubmissionQueued && submission.Status != judge.SubmissionRunning {
				log.Println("worker", workerID, "skipping submission with status:", submissionID, submission.Status)
				continue
			}
			if _, err := judgeService.ProcessSubmission(ctx, submission); err != nil {
				log.Println("worker", workerID, "failed to process submission:", submissionID, err)
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
	cfg := loadConfig()
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
	defer cleanupSandboxes(executor, sandboxes)

	pool := judge.NewExecutorSandboxPool(executor, sandboxes)

	store, queue, cleanup, err := buildPersistence(cfg)
	if err != nil {
		log.Fatal("failed to build persistence:", err)
	}
	defer cleanup()

	submissionStore = store
	submissionQueue = queue

	judgeService = judge.NewService(executor, pool, store, registry)

	workerCtx, cancelWorkers := context.WithCancel(context.Background())
	defer cancelWorkers()

	for i := 1; i <= cfg.workerCount; i++ {
		startWorker(workerCtx, i)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/health", healthHandler)
	mux.HandleFunc("/judge", createSubmissionHandler)
	mux.HandleFunc("/submissions", submissionsCollectionHandler)
	mux.HandleFunc("/submissions/", submissionHandler)

	server := &http.Server{
		Addr:    ":" + cfg.port,
		Handler: mux,
	}

	shutdownDone := make(chan struct{})
	go func() {
		sigCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		defer stop()

		<-sigCtx.Done()
		log.Println("shutdown signal received")

		cancelWorkers()
		submissionQueue.Close()

		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()

		if err := server.Shutdown(shutdownCtx); err != nil {
			log.Println("server shutdown error:", err)
		}

		close(shutdownDone)
	}()

	log.Printf(
		"YexJudge server running on :%s with %d workers, queue size %d, sandbox pool size %d",
		cfg.port,
		cfg.workerCount,
		cfg.queueSize,
		cfg.sandboxPoolSize,
	)

	err = server.ListenAndServe()
	if err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}

	<-shutdownDone
}

func buildPersistence(cfg config) (judge.SubmissionStore, judge.SubmissionQueue, func(), error) {
	if cfg.databaseURL == "" {
		log.Println("DATABASE_URL not set, using in-memory store and queue")
		store := judge.NewMemorySubmissionStore()
		queue := judge.NewMemorySubmissionQueue(cfg.queueSize)
		return store, queue, func() {}, nil
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
	queue := judge.NewPostgresSubmissionQueue(db, cfg.queuePoll)

	cleanup := func() {
		if err := db.Close(); err != nil {
			log.Println("failed to close database:", err)
		}
	}

	return store, queue, cleanup, nil
}
