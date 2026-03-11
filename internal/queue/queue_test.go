package queue_test

import (
	"context"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/vaultkey/vaultkey/internal/queue"
)

func startRedis(t *testing.T) *redis.Client {
	t.Helper()

	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "redis:7-alpine",
		ExposedPorts: []string{"6379/tcp"},
		WaitingFor:   wait.ForLog("Ready to accept connections"),
	}

	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Fatalf("start redis container: %v", err)
	}
	t.Cleanup(func() { container.Terminate(ctx) }) //nolint:errcheck

	host, err := container.Host(ctx)
	if err != nil {
		t.Fatalf("container host: %v", err)
	}
	port, err := container.MappedPort(ctx, "6379")
	if err != nil {
		t.Fatalf("container port: %v", err)
	}

	client := redis.NewClient(&redis.Options{
		Addr: host + ":" + port.Port(),
	})
	t.Cleanup(func() { client.Close() })

	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("redis ping: %v", err)
	}

	return client
}

func newQueue(t *testing.T, client *redis.Client) *queue.Queue {
	t.Helper()
	// Extract addr from the client options to pass to queue.New
	opts := client.Options()
	q, err := queue.New(opts.Addr, opts.Password, opts.DB)
	if err != nil {
		t.Fatalf("queue.New: %v", err)
	}
	t.Cleanup(func() { q.Close() })
	return q
}

// TestCrashRecovery simulates a worker crash:
//  1. Push a job directly into the processing queue (as if a worker picked it
//     up but then crashed before acknowledging it).
//  2. Call RecoverStalled.
//  3. Assert the job is now in the main queue.
//  4. Assert it can be dequeued and processed exactly once.
func TestCrashRecovery(t *testing.T) {
	client := startRedis(t)
	q := newQueue(t, client)
	ctx := context.Background()

	stalledJob := queue.Job{
		ID:        "stalled-job-1",
		ProjectID: "proj-1",
		WalletID:  "wallet-1",
		Operation: "sign_tx_evm",
	}

	// Simulate the job already in the processing list (worker died mid-flight).
	// We do this by enqueueing normally then dequeuing without acknowledging.
	if err := q.Enqueue(ctx, stalledJob); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	// Non-blocking dequeue: poll with 1-second timeout.
	dequeued, err := q.Dequeue(ctx, 1)
	if err != nil {
		t.Fatalf("dequeue: %v", err)
	}
	if dequeued == nil {
		t.Fatal("expected to dequeue the job, got nil")
	}
	// Do NOT acknowledge — simulates a crash.

	// Main queue should now be empty; processing list has 1 entry.
	mainLen := client.LLen(ctx, queue.QueueKey).Val()
	procLen := client.LLen(ctx, queue.ProcessingKey).Val()

	if mainLen != 0 {
		t.Errorf("main queue should be empty, got %d", mainLen)
	}
	if procLen != 1 {
		t.Errorf("processing queue should have 1 entry, got %d", procLen)
	}

	// Recover stalled jobs.
	recovered, err := q.RecoverStalled(ctx, 0 /* maxAge=0 recovers everything */)
	if err != nil {
		t.Fatalf("RecoverStalled: %v", err)
	}
	if recovered != 1 {
		t.Errorf("expected 1 recovered job, got %d", recovered)
	}

	// Job must now be back in the main queue.
	mainLen = client.LLen(ctx, queue.QueueKey).Val()
	if mainLen != 1 {
		t.Errorf("after recovery, main queue should have 1 entry, got %d", mainLen)
	}

	// Dequeue again and verify it's the same job.
	reprocessed, err := q.Dequeue(ctx, 1)
	if err != nil {
		t.Fatalf("dequeue after recovery: %v", err)
	}
	if reprocessed == nil {
		t.Fatal("expected to dequeue recovered job, got nil")
	}
	if reprocessed.ID != stalledJob.ID {
		t.Errorf("recovered wrong job: got %s, want %s", reprocessed.ID, stalledJob.ID)
	}

	// Acknowledge successful processing.
	if err := q.Acknowledge(ctx, *reprocessed); err != nil {
		t.Fatalf("acknowledge: %v", err)
	}

	// After ack, processing queue must be empty — no double processing.
	procLen = client.LLen(ctx, queue.ProcessingKey).Val()
	if procLen != 0 {
		t.Errorf("processing queue should be empty after ack, got %d", procLen)
	}

	// Main queue must also be empty — job was not double-enqueued.
	mainLen = client.LLen(ctx, queue.QueueKey).Val()
	if mainLen != 0 {
		t.Errorf("main queue should be empty after ack, got %d", mainLen)
	}
}

// TestEnqueueDequeueOrder verifies FIFO order is maintained.
func TestEnqueueDequeueOrder(t *testing.T) {
	client := startRedis(t)
	q := newQueue(t, client)
	ctx := context.Background()

	jobs := []queue.Job{
		{ID: "job-1", ProjectID: "p", WalletID: "w", Operation: "sign_tx_evm"},
		{ID: "job-2", ProjectID: "p", WalletID: "w", Operation: "sign_tx_evm"},
		{ID: "job-3", ProjectID: "p", WalletID: "w", Operation: "sign_tx_evm"},
	}

	for _, j := range jobs {
		if err := q.Enqueue(ctx, j); err != nil {
			t.Fatalf("enqueue %s: %v", j.ID, err)
		}
	}

	for i, expected := range jobs {
		got, err := q.Dequeue(ctx, 1)
		if err != nil {
			t.Fatalf("dequeue[%d]: %v", i, err)
		}
		if got == nil {
			t.Fatalf("dequeue[%d]: got nil", i)
		}
		if got.ID != expected.ID {
			t.Errorf("dequeue[%d]: got %s, want %s", i, got.ID, expected.ID)
		}
		q.Acknowledge(ctx, *got) //nolint:errcheck
	}
}

// TestDLQMove verifies that a job moved to the DLQ is removed from processing.
func TestDLQMove(t *testing.T) {
	client := startRedis(t)
	q := newQueue(t, client)
	ctx := context.Background()

	job := queue.Job{ID: "dlq-job", ProjectID: "p", WalletID: "w", Operation: "sign_tx_evm"}

	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("enqueue: %v", err)
	}

	dequeued, err := q.Dequeue(ctx, 1)
	if err != nil || dequeued == nil {
		t.Fatalf("dequeue: %v / %v", err, dequeued)
	}

	if err := q.MoveToDLQ(ctx, *dequeued, "exhausted retries"); err != nil {
		t.Fatalf("MoveToDLQ: %v", err)
	}

	q.Acknowledge(ctx, *dequeued) //nolint:errcheck

	// Main queue empty
	if n := client.LLen(ctx, queue.QueueKey).Val(); n != 0 {
		t.Errorf("main queue should be empty, got %d", n)
	}
	// DLQ has entry
	if n := client.LLen(ctx, queue.DLQKey).Val(); n != 1 {
		t.Errorf("DLQ should have 1 entry, got %d", n)
	}
}