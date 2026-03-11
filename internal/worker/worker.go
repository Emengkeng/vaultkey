package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/vaultkey/vaultkey/internal/queue"
	"github.com/vaultkey/vaultkey/internal/relayer"
	"github.com/vaultkey/vaultkey/internal/storage"
	"github.com/vaultkey/vaultkey/internal/wallet"
	"github.com/vaultkey/vaultkey/internal/webhook"
)

type Worker struct {
	store       *storage.Store
	queue       *queue.Queue
	walletSvc   *wallet.Service
	relayerSvc  *relayer.Service
	webhookSvc  *webhook.Deliverer
	concurrency int
	pollTimeout int
}

func New(
	store *storage.Store,
	q *queue.Queue,
	walletSvc *wallet.Service,
	relayerSvc *relayer.Service,
	webhookSvc *webhook.Deliverer,
	concurrency, pollTimeout int,
) *Worker {
	return &Worker{
		store:       store,
		queue:       q,
		walletSvc:   walletSvc,
		relayerSvc:  relayerSvc,
		webhookSvc:  webhookSvc,
		concurrency: concurrency,
		pollTimeout: pollTimeout,
	}
}

// Start launches the worker pool. Blocks until ctx is cancelled.
func (w *Worker) Start(ctx context.Context) {
	// Recover any stalled jobs from a previous crash on startup
	recovered, err := w.queue.RecoverStalled(ctx, 5*time.Minute)
	if err != nil {
		log.Printf("worker: stall recovery error: %v", err)
	} else if recovered > 0 {
		log.Printf("worker: recovered %d stalled jobs", recovered)
	}

	var wg sync.WaitGroup
	for i := 0; i < w.concurrency; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			log.Printf("worker[%d]: started", id)
			w.loop(ctx, id)
			log.Printf("worker[%d]: stopped", id)
		}(i)
	}
	wg.Wait()
}

func (w *Worker) loop(ctx context.Context, workerID int) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		job, err := w.queue.Dequeue(ctx, w.pollTimeout)
		if err != nil {
			log.Printf("worker[%d]: dequeue error: %v", workerID, err)
			time.Sleep(time.Second) // backoff on Redis errors
			continue
		}
		if job == nil {
			continue // poll timeout, loop again
		}

		if err := w.processJob(ctx, job); err != nil {
			log.Printf("worker[%d]: process job %s error: %v", workerID, job.ID, err)
		}
	}
}

func (w *Worker) processJob(ctx context.Context, qJob *queue.Job) error {
	// Fetch full job from DB
	dbJob, err := w.store.GetSigningJob(ctx, qJob.ID, qJob.ProjectID)
	if err != nil {
		return fmt.Errorf("fetch job from db: %w", err)
	}
	if dbJob == nil {
		// Job doesn't exist - acknowledge and discard
		w.queue.Acknowledge(ctx, *qJob) //nolint:errcheck
		return fmt.Errorf("job %s not found in db, discarding", qJob.ID)
	}

	// Skip if already completed or dead (duplicate delivery protection)
	if dbJob.Status == "completed" || dbJob.Status == "dead" {
		w.queue.Acknowledge(ctx, *qJob) //nolint:errcheck
		return nil
	}

	// Fetch the wallet
	wlt, err := w.store.GetWalletByID(ctx, dbJob.WalletID, dbJob.ProjectID)
	if err != nil || wlt == nil {
		w.handleJobFailure(ctx, qJob, dbJob, "wallet not found or db error")
		return nil
	}

	// Fetch project for webhook config and retry settings
	proj, err := w.store.GetProjectByID(ctx, dbJob.ProjectID)
	if err != nil || proj == nil {
		w.handleJobFailure(ctx, qJob, dbJob, "project not found")
		return nil
	}

	// Mark as processing in DB
	if err := w.store.MarkJobProcessing(ctx, dbJob.ID); err != nil {
		return fmt.Errorf("mark processing: %w", err)
	}

	// Execute the signing operation
	result, signingErr := w.sign(ctx, dbJob, wlt)

	if signingErr != nil {
		w.handleJobFailure(ctx, qJob, dbJob, signingErr.Error())
		w.deliverWebhook(ctx, proj, dbJob, nil, signingErr.Error())
		return nil
	}

	// Mark completed
	resultJSON, _ := json.Marshal(result)
	if err := w.store.MarkJobCompleted(ctx, dbJob.ID, resultJSON); err != nil {
		log.Printf("job %s: failed to mark completed: %v", dbJob.ID, err)
	}

	// Acknowledge job from processing queue
	w.queue.Acknowledge(ctx, *qJob) //nolint:errcheck

	// Write audit log
	walletID := wlt.ID
	jobID := dbJob.ID
	w.store.WriteAuditLog(ctx, dbJob.ProjectID, &walletID, &jobID, dbJob.Operation, "worker", map[string]any{ //nolint:errcheck
		"status": "completed",
	})

	// Deliver webhook
	w.deliverWebhook(ctx, proj, dbJob, result, "")

	return nil
}

type signingResult struct {
	Signature string `json:"signature,omitempty"`
	SignedTx  string `json:"signed_tx,omitempty"`
}

func (w *Worker) sign(ctx context.Context, job *storage.SigningJob, wlt *storage.Wallet) (*signingResult, error) {
	// Route gasless jobs through the relayer service
	if job.Gasless {
		return w.signGasless(ctx, job, wlt)
	}

	switch job.Operation {
	case "sign_tx_evm":
		var tx wallet.EVMTransaction
		if err := json.Unmarshal(job.Payload, &tx); err != nil {
			return nil, fmt.Errorf("invalid evm transaction payload: %w", err)
		}
		signed, err := w.walletSvc.SignEVMTransaction(ctx, wlt.EncryptedKey, wlt.EncryptedDEK, tx)
		if err != nil {
			return nil, err
		}
		return &signingResult{SignedTx: "0x" + fmt.Sprintf("%x", signed)}, nil

	case "sign_msg_evm":
		var msg struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(job.Payload, &msg); err != nil {
			return nil, fmt.Errorf("invalid evm message payload: %w", err)
		}
		sig, err := w.walletSvc.SignEVMMessage(ctx, wlt.EncryptedKey, wlt.EncryptedDEK, []byte(msg.Message))
		if err != nil {
			return nil, err
		}
		return &signingResult{Signature: "0x" + fmt.Sprintf("%x", sig)}, nil

	case "sign_tx_solana":
		var msg struct {
			Message string `json:"message"` // hex encoded tx bytes
		}
		if err := json.Unmarshal(job.Payload, &msg); err != nil {
			return nil, fmt.Errorf("invalid solana tx payload: %w", err)
		}
		var txBytes []byte
		fmt.Sscanf(msg.Message, "%x", &txBytes)
		sig, err := w.walletSvc.SignSolanaTransaction(ctx, wlt.EncryptedKey, wlt.EncryptedDEK, txBytes)
		if err != nil {
			return nil, err
		}
		return &signingResult{Signature: fmt.Sprintf("%x", sig)}, nil

	case "sign_msg_solana":
		var msg struct {
			Message string `json:"message"`
		}
		if err := json.Unmarshal(job.Payload, &msg); err != nil {
			return nil, fmt.Errorf("invalid solana message payload: %w", err)
		}
		sig, err := w.walletSvc.SignSolanaMessage(ctx, wlt.EncryptedKey, wlt.EncryptedDEK, []byte(msg.Message))
		if err != nil {
			return nil, err
		}
		return &signingResult{Signature: fmt.Sprintf("%x", sig)}, nil

	default:
		return nil, fmt.Errorf("unknown operation: %s", job.Operation)
	}
}

// signGasless routes the job through the relayer service.
// The relayer pays gas; the user wallet's intent is encoded in the payload.
func (w *Worker) signGasless(ctx context.Context, job *storage.SigningJob, wlt *storage.Wallet) (*signingResult, error) {
	switch job.Operation {
	case "sign_tx_evm":
		var payload relayer.EVMRelayPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid evm relay payload: %w", err)
		}
		result, err := w.relayerSvc.RelayEVM(ctx, job.ProjectID, wlt, payload)
		if err != nil {
			return nil, err
		}
		return &signingResult{SignedTx: result.SignedTx}, nil

	case "sign_tx_solana":
		var payload relayer.SolanaRelayPayload
		if err := json.Unmarshal(job.Payload, &payload); err != nil {
			return nil, fmt.Errorf("invalid solana relay payload: %w", err)
		}
		result, err := w.relayerSvc.RelaySolana(ctx, job.ProjectID, wlt, payload)
		if err != nil {
			return nil, err
		}
		return &signingResult{Signature: result.Signature}, nil

	default:
		return nil, fmt.Errorf("gasless not supported for operation: %s", job.Operation)
	}
}

func (w *Worker) handleJobFailure(ctx context.Context, qJob *queue.Job, dbJob *storage.SigningJob, reason string) {
	log.Printf("job %s failed: %s (attempt %d)", dbJob.ID, reason, dbJob.Attempts)

	// Fetch project to get max retries config
	proj, _ := w.store.GetProjectByID(ctx, dbJob.ProjectID)
	maxRetries := 3
	if proj != nil {
		maxRetries = proj.MaxRetries
	}

	if dbJob.Attempts >= maxRetries {
		// Exhausted retries - move to DLQ
		w.store.MarkJobDead(ctx, dbJob.ID, reason)        //nolint:errcheck
		w.queue.MoveToDLQ(ctx, *qJob, reason)             //nolint:errcheck
		w.queue.Acknowledge(ctx, *qJob)                   //nolint:errcheck
		log.Printf("job %s moved to DLQ after %d attempts", dbJob.ID, dbJob.Attempts)
	} else {
		// Requeue for retry
		w.store.MarkJobFailed(ctx, dbJob.ID, reason) //nolint:errcheck
		w.queue.Requeue(ctx, *qJob)                  //nolint:errcheck
	}
}

func (w *Worker) deliverWebhook(ctx context.Context, proj *storage.Project, job *storage.SigningJob, result *signingResult, errMsg string) {
	if proj.WebhookURL == nil || *proj.WebhookURL == "" {
		w.store.MarkWebhookFailed(ctx, job.ID) //nolint:errcheck - no webhook configured, mark skipped
		return
	}

	status := "completed"
	if errMsg != "" {
		status = "failed"
		if job.Status == "dead" {
			status = "dead"
		}
	}

	var resultJSON json.RawMessage
	if result != nil {
		resultJSON, _ = json.Marshal(result)
	}

	secret := ""
	if proj.WebhookSecret != nil {
		secret = *proj.WebhookSecret
	}

	payload := webhook.Payload{
		JobID:     job.ID,
		ProjectID: job.ProjectID,
		WalletID:  job.WalletID,
		Operation: job.Operation,
		Status:    status,
		Result:    resultJSON,
		Error:     errMsg,
		Timestamp: time.Now().UTC().Format(time.RFC3339),
	}

	maxRetries := proj.MaxRetries
	if err := w.webhookSvc.Deliver(ctx, *proj.WebhookURL, secret, payload, maxRetries); err != nil {
		log.Printf("webhook delivery failed for job %s: %v", job.ID, err)
		w.store.MarkWebhookFailed(ctx, job.ID) //nolint:errcheck
		return
	}

	w.store.MarkWebhookDelivered(ctx, job.ID) //nolint:errcheck
}
