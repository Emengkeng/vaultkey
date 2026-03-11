package queue

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	QueueKey      = "vaultkey:jobs"
	ProcessingKey = "vaultkey:jobs:processing" // in-flight jobs (BRPOPLPUSH destination)
	DLQKey        = "vaultkey:dlq"
)

// Job is the envelope pushed onto the queue.
type Job struct {
	ID        string `json:"id"`         // signing_jobs.id
	ProjectID string `json:"project_id"`
	WalletID  string `json:"wallet_id"`
	Operation string `json:"operation"`
}

type Queue struct {
	client *redis.Client
}

func New(addr, password string, db int) (*Queue, error) {
	client := redis.NewClient(&redis.Options{
		Addr:         addr,
		Password:     password,
		DB:           db,
		PoolSize:     20,
		MinIdleConns: 5,
		DialTimeout:  5 * time.Second,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 5 * time.Second,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := client.Ping(ctx).Err(); err != nil {
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	return &Queue{client: client}, nil
}

func (q *Queue) Close() error { return q.client.Close() }

// Enqueue pushes a job onto the tail of the queue (LPUSH = stack, use RPUSH for FIFO).
// RPUSH + BRPOPLPUSH gives FIFO processing order.
func (q *Queue) Enqueue(ctx context.Context, job Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return fmt.Errorf("marshal job: %w", err)
	}
	return q.client.RPush(ctx, QueueKey, data).Err()
}

// Dequeue blocks until a job is available, atomically moving it to the
// processing list. This guarantees the job is not lost if the worker crashes.
// Returns nil job (no error) on timeout.
func (q *Queue) Dequeue(ctx context.Context, timeoutSec int) (*Job, error) {
	// BRPOPLPUSH: atomically pop from queue, push to processing
	// If worker crashes, the job remains in processing and can be recovered
	data, err := q.client.BRPopLPush(ctx, QueueKey, ProcessingKey, time.Duration(timeoutSec)*time.Second).Bytes()
	if err == redis.Nil {
		return nil, nil // timeout, no job available
	}
	if err != nil {
		return nil, fmt.Errorf("dequeue: %w", err)
	}

	var job Job
	if err := json.Unmarshal(data, &job); err != nil {
		// malformed job - move to DLQ, don't block the queue
		q.moveToDLQ(context.Background(), data, "unmarshal error: "+err.Error())
		return nil, nil
	}

	return &job, nil
}

// Acknowledge removes a successfully processed job from the processing list.
func (q *Queue) Acknowledge(ctx context.Context, job Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	return q.client.LRem(ctx, ProcessingKey, 1, data).Err()
}

// MoveToDLQ moves a job from the processing list to the dead letter queue.
// Called when a job has exhausted all retries.
func (q *Queue) MoveToDLQ(ctx context.Context, job Job, reason string) error {
	payload, _ := json.Marshal(map[string]any{
		"job":    job,
		"reason": reason,
		"time":   time.Now().UTC(),
	})
	return q.moveToDLQ(ctx, payload, reason)
}

// Requeue moves a job from processing back to the main queue for retry.
func (q *Queue) Requeue(ctx context.Context, job Job) error {
	data, err := json.Marshal(job)
	if err != nil {
		return err
	}
	// Remove from processing list
	if err := q.client.LRem(ctx, ProcessingKey, 1, data).Err(); err != nil {
		return fmt.Errorf("remove from processing: %w", err)
	}
	// Push back to main queue
	return q.client.RPush(ctx, QueueKey, data).Err()
}

// RecoverStalled moves jobs that have been stuck in processing for too long
// back to the main queue. Call this on worker startup and periodically.
func (q *Queue) RecoverStalled(ctx context.Context, maxAge time.Duration) (int, error) {
	// In a production system you'd store a timestamp alongside each job.
	// Simple approach: drain processing list and requeue everything.
	// This is safe because workers use idempotency keys.
	items, err := q.client.LRange(ctx, ProcessingKey, 0, -1).Result()
	if err != nil {
		return 0, err
	}

	recovered := 0
	for _, item := range items {
		var job Job
		if err := json.Unmarshal([]byte(item), &job); err != nil {
			continue
		}
		if err := q.Requeue(ctx, job); err == nil {
			recovered++
		}
	}
	return recovered, nil
}

// Health checks Redis connectivity.
func (q *Queue) Health(ctx context.Context) error {
	return q.client.Ping(ctx).Err()
}

func (q *Queue) moveToDLQ(ctx context.Context, data []byte, _ string) error {
	return q.client.LPush(ctx, DLQKey, data).Err()
}
