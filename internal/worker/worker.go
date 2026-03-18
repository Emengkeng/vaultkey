package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/vaultkey/vaultkey/internal/credits"
	"github.com/vaultkey/vaultkey/internal/nonce"
	"github.com/vaultkey/vaultkey/internal/queue"
	"github.com/vaultkey/vaultkey/internal/relayer"
	"github.com/vaultkey/vaultkey/internal/rpc"
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
	nonceMgr    *nonce.Manager
	rpcMgr      *rpc.Manager
	creditsMgr *credits.Manager
	concurrency int
	pollTimeout int
}

func New(
	store *storage.Store,
	q *queue.Queue,
	walletSvc *wallet.Service,
	relayerSvc *relayer.Service,
	webhookSvc *webhook.Deliverer,
	nonceMgr *nonce.Manager,
	rpcMgr *rpc.Manager,
	creditsMgr *credits.Manager,
	concurrency, pollTimeout int,
) *Worker {
	return &Worker{
		store:       store,
		queue:       q,
		walletSvc:   walletSvc,
		relayerSvc:  relayerSvc,
		webhookSvc:  webhookSvc,
		nonceMgr:    nonceMgr,
		rpcMgr:      rpcMgr,
		concurrency: concurrency,
		creditsMgr: creditsMgr,
		pollTimeout: pollTimeout,
	}
}

func (w *Worker) Start(ctx context.Context) {
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
			time.Sleep(time.Second)
			continue
		}
		if job == nil {
			continue
		}

		if err := w.processJob(ctx, job); err != nil {
			log.Printf("worker[%d]: process job %s error: %v", workerID, job.ID, err)
		}
	}
}

func (w *Worker) processJob(ctx context.Context, qJob *queue.Job) error {
	dbJob, err := w.store.GetSigningJob(ctx, qJob.ID, qJob.ProjectID)
	if err != nil {
		return fmt.Errorf("fetch job from db: %w", err)
	}
	if dbJob == nil {
		w.queue.Acknowledge(ctx, *qJob) //nolint:errcheck
		return fmt.Errorf("job %s not found in db, discarding", qJob.ID)
	}

	if dbJob.Status == "completed" || dbJob.Status == "dead" {
		w.queue.Acknowledge(ctx, *qJob) //nolint:errcheck
		return nil
	}

	wlt, err := w.store.GetWalletByID(ctx, dbJob.WalletID, dbJob.ProjectID)
	if err != nil || wlt == nil {
		w.handleJobFailure(ctx, qJob, dbJob, "wallet not found or db error")
		return nil
	}

	proj, err := w.store.GetProjectByID(ctx, dbJob.ProjectID)
	if err != nil || proj == nil {
		w.handleJobFailure(ctx, qJob, dbJob, "project not found")
		return nil
	}

	if err := w.store.MarkJobProcessing(ctx, dbJob.ID); err != nil {
		return fmt.Errorf("mark processing: %w", err)
	}

	result, signingErr := w.sign(ctx, dbJob, wlt)

	if signingErr != nil {
		w.handleJobFailure(ctx, qJob, dbJob, signingErr.Error())
		w.dispatchWebhook(ctx, proj, dbJob, nil, signingErr.Error())
		return nil
	}

	resultJSON, _ := json.Marshal(result)
	if err := w.store.MarkJobCompleted(ctx, dbJob.ID, resultJSON); err != nil {
		log.Printf("job %s: failed to mark completed: %v", dbJob.ID, err)
	}

	w.queue.Acknowledge(ctx, *qJob) //nolint:errcheck

	walletID := wlt.ID
	jobID := dbJob.ID
	w.store.WriteAuditLog(ctx, dbJob.ProjectID, &walletID, &jobID, dbJob.Operation, "worker", map[string]any{ //nolint:errcheck
		"status": "completed",
	})

	w.dispatchWebhook(ctx, proj, dbJob, result, "")

	return nil
}

type signingResult struct {
	Signature string `json:"signature,omitempty"`
	SignedTx  string `json:"signed_tx,omitempty"`
	TxHash    string `json:"tx_hash,omitempty"`
}

func (w *Worker) sign(ctx context.Context, job *storage.SigningJob, wlt *storage.Wallet) (*signingResult, error) {
	// Sweep operations are always gasless and have their own dedicated path.
	// They must be checked before the gasless flag because sweep sets gasless=true
	// but needs different payload parsing and post-signing side effects (MarkWalletSwept).
	if job.Operation == "sweep_evm" || job.Operation == "sweep_solana" {
		return w.signSweep(ctx, job, wlt)
	}

	if job.Gasless {
		return w.signGasless(ctx, job, wlt)
	}

	switch job.Operation {
	case "sign_tx_evm":
		var tx wallet.EVMTransaction
		if err := json.Unmarshal(job.Payload, &tx); err != nil {
			return nil, fmt.Errorf("invalid evm transaction payload: %w", err)
		}

		chainIDStr := fmt.Sprintf("%d", tx.ChainID)

		if err := w.ensureNonce(ctx, chainIDStr, wlt.Address); err != nil {
			return nil, fmt.Errorf("nonce init: %w", err)
		}

		txNonce, err := w.nonceMgr.Next(ctx, chainIDStr, wlt.Address)
		if err != nil {
			return nil, fmt.Errorf("get nonce: %w", err)
		}
		tx.Nonce = txNonce

		signed, err := w.walletSvc.SignEVMTransaction(ctx, wlt.EncryptedKey, wlt.EncryptedDEK, tx)
		if err != nil {
			w.resyncNonce(ctx, chainIDStr, wlt.Address) //nolint:errcheck
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
			Message string `json:"message"`
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
		return &signingResult{TxHash: result.TxHash}, nil

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

// signSweep handles sweep_evm and sweep_solana jobs.
// Sweep is always gasless — the relayer pays fees so the full balance transfers.
func (w *Worker) signSweep(ctx context.Context, job *storage.SigningJob, wlt *storage.Wallet) (*signingResult, error) {
	switch job.Operation {
	case "sweep_evm":
		return w.signSweepEVM(ctx, job, wlt)
	case "sweep_solana":
		return w.signSweepSolana(ctx, job, wlt)
	default:
		return nil, fmt.Errorf("unknown sweep operation: %s", job.Operation)
	}
}

type sweepEVMPayload struct {
	To      string `json:"to"`
	Value   string `json:"value"`
	Data    string `json:"data"`
	ChainID string `json:"chain_id"`
	Sweep   bool   `json:"sweep"`
}

type sweepSolanaPayload struct {
	To     string `json:"to"`
	Amount uint64 `json:"amount"`
	Sweep  bool   `json:"sweep"`
}

func (w *Worker) signSweepEVM(ctx context.Context, job *storage.SigningJob, wlt *storage.Wallet) (*signingResult, error) {
	var p sweepEVMPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, fmt.Errorf("invalid sweep evm payload: %w", err)
	}

	// Re-fetch balance at signing time. The value in the payload was captured
	// at job creation. If another transaction changed the balance between
	// creation and now, we use the fresher value to avoid sending more than exists.
	currentBalance, err := w.rpcMgr.EVMBalance(ctx, p.ChainID, wlt.Address)
	if err != nil {
		// Fall back to the balance captured at job creation
		currentBalance = p.Value
	}

	chainIDInt, err := parseChainID(p.ChainID)
	if err != nil {
		return nil, fmt.Errorf("parse chain_id: %w", err)
	}

	relayPayload := relayer.EVMRelayPayload{
		To:      p.To,
		Value:   currentBalance,
		Data:    "0x",
		ChainID: chainIDInt,
	}

	result, err := w.relayerSvc.RelayEVM(ctx, job.ProjectID, wlt, relayPayload)
	if err != nil {
		return nil, fmt.Errorf("sweep relay evm: %w", err)
	}

	w.store.MarkWalletSwept(ctx, wlt.ID) //nolint:errcheck

	return &signingResult{TxHash: result.TxHash}, nil
}

func (w *Worker) signSweepSolana(ctx context.Context, job *storage.SigningJob, wlt *storage.Wallet) (*signingResult, error) {
	var p sweepSolanaPayload
	if err := json.Unmarshal(job.Payload, &p); err != nil {
		return nil, fmt.Errorf("invalid sweep solana payload: %w", err)
	}

	// Re-fetch balance at signing time for same reason as EVM
	currentBalance, err := w.rpcMgr.SolanaBalance(ctx, wlt.Address)
	if err != nil {
		currentBalance = p.Amount
	}

	relayPayload := relayer.SolanaRelayPayload{
		To:     p.To,
		Amount: currentBalance,
	}

	result, err := w.relayerSvc.RelaySolana(ctx, job.ProjectID, wlt, relayPayload)
	if err != nil {
		return nil, fmt.Errorf("sweep relay solana: %w", err)
	}

	w.store.MarkWalletSwept(ctx, wlt.ID) //nolint:errcheck

	return &signingResult{Signature: result.Signature}, nil
}

func (w *Worker) ensureNonce(ctx context.Context, chainID, address string) error {
	current, err := w.nonceMgr.Peek(ctx, chainID, address)
	if err != nil {
		return err
	}
	if current > 0 {
		return nil
	}
	return w.resyncNonce(ctx, chainID, address)
}

func (w *Worker) resyncNonce(ctx context.Context, chainID, address string) error {
	pendingNonce, err := w.rpcMgr.EVMPendingNonce(ctx, chainID, address)
	if err != nil {
		return fmt.Errorf("fetch pending nonce from chain: %w", err)
	}
	return w.nonceMgr.SyncFromChain(ctx, chainID, address, pendingNonce)
}

func (w *Worker) handleJobFailure(ctx context.Context, qJob *queue.Job, dbJob *storage.SigningJob, reason string) {
	log.Printf("job %s failed: %s (attempt %d)", dbJob.ID, reason, dbJob.Attempts)

	proj, _ := w.store.GetProjectByID(ctx, dbJob.ProjectID)
	maxRetries := 3
	if proj != nil {
		maxRetries = proj.MaxRetries
	}

	if dbJob.Attempts >= maxRetries {
		w.store.MarkJobDead(ctx, dbJob.ID, reason)  //nolint:errcheck
		w.queue.MoveToDLQ(ctx, *qJob, reason)        //nolint:errcheck
		w.queue.Acknowledge(ctx, *qJob)              //nolint:errcheck
		log.Printf("job %s moved to DLQ after %d attempts", dbJob.ID, dbJob.Attempts)
	} else {
		w.store.MarkJobFailed(ctx, dbJob.ID, reason) //nolint:errcheck
		w.queue.Requeue(ctx, *qJob)                  //nolint:errcheck
	}
}

// dispatchWebhook routes to the correct delivery path based on
// whether the project is cloud-managed.
//
// Self-hosted (cloud_managed=false): direct webhook delivery, unchanged.
// Cloud-managed (cloud_managed=true): settleAndDeliver handles post-job
//   work (usage recording, future: gas settlement) before relaying the
//   webhook to the merchant's configured URL.
func (w *Worker) dispatchWebhook(
	ctx context.Context,
	proj *storage.Project,
	job *storage.SigningJob,
	result *signingResult,
	errMsg string,
) {
	if proj.CloudManaged {
		w.settleAndDeliver(ctx, proj, job, result, errMsg)
	} else {
		w.deliverWebhook(ctx, proj, job, result, errMsg)
	}
}
 
// settleAndDeliver handles post-job processing for cloud-managed projects.
//
// Current responsibilities:
//   - Record usage in usage_daily_rollup (for stats endpoint)
//   - Relay webhook to merchant's configured URL
//
// Future responsibilities (when platform-managed relayer is built):
//   - Gas cost settlement
//   - Refund credits if job failed before broadcast
//
// Designed so each step is independent — a failure in one does not prevent
// the others from running.
func (w *Worker) settleAndDeliver(
	ctx context.Context,
	proj *storage.Project,
	job *storage.SigningJob,
	result *signingResult,
	errMsg string,
) {
	// ── Step 1: Record usage ──────────────────────────────────────────────
	// Only record for completed jobs — failed/dead jobs were already debited
	// at request time (the debit is the cost of attempting the operation).
	// We record usage here (not just in the handler) to catch async jobs
	// that were enqueued and completed later.
	//
	// For synchronous operations (create_wallet), the SDK handler already
	// called RecordUsage. For async signing/sweep jobs, this is the first
	// time we know the operation completed.
	if errMsg == "" && proj.OrgID != nil {
		// Determine operation cost for rollup.
		oc, err := w.creditsMgr.GetCost(ctx, job.Operation)
		if err == nil && oc != nil {
			if err := w.creditsMgr.RecordUsage(ctx, *proj.OrgID, job.Operation, oc.Cost); err != nil {
				log.Printf("worker: record usage failed for job %s: %v", job.ID, err)
				// Non-fatal — stats are best-effort. Don't block webhook delivery.
			}
		}
	}
 
	// ── Step 2: Relay webhook to merchant ─────────────────────────────────
	// Same logic as direct delivery. Cloud-managed projects can still
	// configure a webhook URL and receive events.
	w.deliverWebhook(ctx, proj, job, result, errMsg)
}

func (w *Worker) deliverWebhook(ctx context.Context, proj *storage.Project, job *storage.SigningJob, result *signingResult, errMsg string) {
	if proj.WebhookURL == nil || *proj.WebhookURL == "" {
		w.store.MarkWebhookFailed(ctx, job.ID) //nolint:errcheck
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

// parseChainID converts a string chain ID like "137" to int64.
func parseChainID(s string) (int64, error) {
	var id int64
	_, err := fmt.Sscanf(s, "%d", &id)
	if err != nil {
		return 0, fmt.Errorf("chain_id %q is not a valid integer: %w", s, err)
	}
	return id, nil
}