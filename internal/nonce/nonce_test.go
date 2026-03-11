package nonce_test

import (
	"context"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"github.com/vaultkey/vaultkey/internal/nonce"
)

// startRedis spins up a real Redis container for the test. It is cleaned up
// automatically when the test ends.
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

// TestNonceConcurrentUniqueness spins up 50 goroutines all racing to call
// Next on the same wallet+chain. Every returned nonce must be unique and the
// full set must form a contiguous range with no gaps.
//
// Run with: go test -race ./internal/nonce/...
func TestNonceConcurrentUniqueness(t *testing.T) {
	client := startRedis(t)
	mgr := nonce.New(client)
	ctx := context.Background()

	const (
		chainID = "1"
		address = "0xTestAddress"
		workers = 50
	)

	// Seed the counter so we start at a known nonce (0).
	if err := mgr.SyncFromChain(ctx, chainID, address, 0); err != nil {
		t.Fatalf("sync: %v", err)
	}

	results := make([]uint64, workers)
	var wg sync.WaitGroup
	wg.Add(workers)

	for i := 0; i < workers; i++ {
		i := i
		go func() {
			defer wg.Done()
			n, err := mgr.Next(ctx, chainID, address)
			if err != nil {
				t.Errorf("worker %d: Next error: %v", i, err)
				return
			}
			results[i] = n
		}()
	}

	wg.Wait()

	// Check uniqueness
	seen := make(map[uint64]int)
	for i, n := range results {
		if prev, ok := seen[n]; ok {
			t.Errorf("duplicate nonce %d returned by workers %d and %d", n, prev, i)
		}
		seen[n] = i
	}

	// Check contiguous range [0, workers)
	for expected := uint64(0); expected < workers; expected++ {
		if _, ok := seen[expected]; !ok {
			t.Errorf("gap in nonce sequence: %d missing", expected)
		}
	}
}

// TestNonceResyncCorrectness simulates the broadcast-failure scenario:
//  1. Advance Redis counter by calling Next a few times.
//  2. Call SyncFromChain with a chain nonce that is behind the Redis counter
//     (as if the chain never confirmed those txs and we need to rewind).
//  3. Assert the next Next call returns the chain value, not the stale Redis value.
func TestNonceResyncCorrectness(t *testing.T) {
	client := startRedis(t)
	mgr := nonce.New(client)
	ctx := context.Background()

	const (
		chainID = "1"
		address = "0xResyncTest"
	)

	// Start at chain nonce 10
	if err := mgr.SyncFromChain(ctx, chainID, address, 10); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Advance Redis by consuming 3 nonces (10, 11, 12)
	for i := 0; i < 3; i++ {
		n, err := mgr.Next(ctx, chainID, address)
		if err != nil {
			t.Fatalf("Next[%d]: %v", i, err)
		}
		want := uint64(10 + i)
		if n != want {
			t.Fatalf("Next[%d]: got %d, want %d", i, n, want)
		}
	}

	// Simulate broadcast failure: chain is still at 10 (none confirmed).
	// Resync to chain nonce 10.
	if err := mgr.SyncFromChain(ctx, chainID, address, 10); err != nil {
		t.Fatalf("resync: %v", err)
	}

	// Next call must return 10, not 13.
	n, err := mgr.Next(ctx, chainID, address)
	if err != nil {
		t.Fatalf("Next after resync: %v", err)
	}
	if n != 10 {
		t.Errorf("after resync: got nonce %d, want 10", n)
	}
}

// TestNoncePeekDoesNotAdvance checks that Peek returns the last used nonce
// without side effects.
func TestNoncePeekDoesNotAdvance(t *testing.T) {
	client := startRedis(t)
	mgr := nonce.New(client)
	ctx := context.Background()

	const (
		chainID = "1"
		address = "0xPeekTest"
	)

	if err := mgr.SyncFromChain(ctx, chainID, address, 5); err != nil {
		t.Fatalf("sync: %v", err)
	}

	// Consume nonce 5
	n, err := mgr.Next(ctx, chainID, address)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	if n != 5 {
		t.Fatalf("expected 5, got %d", n)
	}

	// Peek three times — none should advance the counter
	for i := 0; i < 3; i++ {
		p, err := mgr.Peek(ctx, chainID, address)
		if err != nil {
			t.Fatalf("Peek[%d]: %v", i, err)
		}
		if p != 5 {
			t.Errorf("Peek[%d]: got %d, want 5", i, p)
		}
	}

	// Next call should return 6, not something higher
	n2, err := mgr.Next(ctx, chainID, address)
	if err != nil {
		t.Fatalf("Next after peeks: %v", err)
	}
	if n2 != 6 {
		t.Errorf("after 3 peeks, Next returned %d, want 6", n2)
	}
}

// TestNoncePeekOnMissingKey returns 0 when no counter exists yet.
func TestNoncePeekOnMissingKey(t *testing.T) {
	client := startRedis(t)
	mgr := nonce.New(client)
	ctx := context.Background()

	p, err := mgr.Peek(ctx, "999", "0xNobody")
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if p != 0 {
		t.Errorf("Peek on missing key: got %d, want 0", p)
	}
}