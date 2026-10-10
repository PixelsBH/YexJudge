package judge

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestPostgresAdmissionEnforcesDurableQueueLimit(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPostgresSubmissionStoreWithQueueLimit(db, 1)
	firstID := fmt.Sprintf("admission-first-%d", time.Now().UnixNano())
	secondID := fmt.Sprintf("admission-second-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM submissions WHERE id IN ($1, $2)`, firstID, secondID) })

	if err := store.SaveAdmitted(context.Background(), integrationSubmission(firstID)); err != nil {
		t.Fatalf("first SaveAdmitted() error = %v", err)
	}
	if err := store.SaveAdmitted(context.Background(), integrationSubmission(secondID)); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("second SaveAdmitted() error = %v, want ErrQueueFull", err)
	}
}

func TestPostgresAdmissionSerializesConcurrentRequests(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPostgresSubmissionStoreWithQueueLimit(db, 1)
	ids := []string{
		fmt.Sprintf("admission-race-a-%d", time.Now().UnixNano()),
		fmt.Sprintf("admission-race-b-%d", time.Now().UnixNano()),
	}
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM submissions WHERE id IN ($1, $2)`, ids[0], ids[1]) })

	start := make(chan struct{})
	results := make(chan error, len(ids))
	var wait sync.WaitGroup
	for _, id := range ids {
		id := id
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			results <- store.SaveAdmitted(context.Background(), integrationSubmission(id))
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	admitted, rejected := 0, 0
	for err := range results {
		switch {
		case err == nil:
			admitted++
		case errors.Is(err, ErrQueueFull):
			rejected++
		default:
			t.Fatalf("SaveAdmitted() error = %v, want nil or ErrQueueFull", err)
		}
	}
	if admitted != 1 || rejected != 1 {
		t.Fatalf("concurrent admissions = %d accepted and %d rejected, want one each", admitted, rejected)
	}
}

func TestPostgresRunningUpdatePreservesRenewedLease(t *testing.T) {
	db := openIntegrationDB(t)
	store := NewPostgresSubmissionStore(db)
	id := fmt.Sprintf("lease-monotonic-%d", time.Now().UnixNano())
	t.Cleanup(func() { _, _ = db.Exec(`DELETE FROM submissions WHERE id = $1`, id) })
	if err := store.Save(integrationSubmission(id)); err != nil {
		t.Fatalf("Save() error = %v", err)
	}

	queue := NewPostgresSubmissionQueueWithOptions(db, 5*time.Millisecond, 2*time.Minute, 2)
	t.Cleanup(queue.Close)
	claim, err := queue.Dequeue(context.Background())
	if err != nil || claim.ID != id {
		t.Fatalf("Dequeue() = %+v, %v", claim, err)
	}
	stale, found := store.Get(id)
	if !found || stale.Status != SubmissionRunning || stale.LeaseExpiresAt == nil {
		t.Fatalf("loaded running submission = %+v, found %t", stale, found)
	}
	oldLease := *stale.LeaseExpiresAt
	if err := queue.RenewLease(context.Background(), claim); err != nil {
		t.Fatalf("RenewLease() error = %v", err)
	}
	var renewed time.Time
	if err := db.QueryRow(`SELECT lease_expires_at FROM submissions WHERE id = $1`, id).Scan(&renewed); err != nil {
		t.Fatal(err)
	}
	if renewed.Before(oldLease) {
		t.Fatalf("renewed lease %s precedes stale loaded lease %s", renewed, oldLease)
	}
	if err := store.Update(stale); err != nil {
		t.Fatalf("Update(stale running submission) error = %v", err)
	}
	var afterUpdate time.Time
	if err := db.QueryRow(`SELECT lease_expires_at FROM submissions WHERE id = $1`, id).Scan(&afterUpdate); err != nil {
		t.Fatal(err)
	}
	if afterUpdate.Before(renewed) {
		t.Fatalf("running update regressed lease: before=%s after=%s", renewed, afterUpdate)
	}
}
