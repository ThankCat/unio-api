package p4fault_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const (
	prepareCrashAdvisoryLock int64 = 734806498223
	prepareCrashFunction           = "p4_test_block_epoch_prepared"
	prepareCrashTrigger            = "p4_test_block_epoch_prepared"
)

// TestP4StateEpochPrepareCrashE2E kills the maintenance process after Redis
// established its pending fence but before PostgreSQL can persist prepared.
func TestP4StateEpochPrepareCrashE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" || os.Getenv("P4_PREPARE_CRASH_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 and P4_PREPARE_CRASH_E2E=1 to run the epoch prepare-crash drill")
	}

	h := setupFaultHarness(t)
	maintenanceBinary := buildRuntimeStateMaintenanceBinary(t, h.root)
	pool := openMaintenanceDatabase(t, h.infra.databaseURL)
	queries := sqlc.New(pool)
	integrityStore := breakerstore.NewStore(h.redis, h.namespace)
	markerKey := h.namespace + ":runtime-control:v1:state-integrity-marker"

	initialEpoch := readStateEpoch(t, queries)
	initialMarker := readStateIntegrity(t, integrityStore)
	if initialEpoch.Value.State != runtimecontrol.StateEpochReady ||
		!initialMarker.Ready(initialEpoch.Value.Epoch, initialEpoch.Revision) {
		t.Fatalf("initial runtime state is not ready: epoch=%+v marker=%+v", initialEpoch, initialMarker)
	}

	pauseGatewayProcesses(t, h.gateways[:])
	gatewaysPaused := true
	defer func() {
		if gatewaysPaused {
			resumeGatewayProcesses(h.gateways[:])
		}
	}()

	blocker := installEpochPreparedCASBlocker(t, pool)
	detectedAt := time.Now().UTC()
	recoveryID := "p4-fault-prepare-crash-" + randomSuffix(t)
	beginArgs := []string{
		"begin",
		"--reason", "restore",
		"--detected-at", detectedAt.Format(time.RFC3339Nano),
		"--not-before", detectedAt.Format(time.RFC3339Nano),
		"--operator-ref", maintenanceOperatorRef,
		"--recovery-id", recoveryID,
		"--expected-current-revision", strconv.FormatInt(initialEpoch.Revision, 10),
		"--confirm-state-loss",
		"--confirm-external-ingress-blocked",
	}
	applicationName := "p4_prepare_crash_" + randomSuffix(t)
	running := startPrepareCrashMaintenanceCommand(t, h, maintenanceBinary, applicationName, beginArgs...)

	operation, transition, pendingMarker := waitForBlockedEpochPreparedCAS(
		t, pool, queries, integrityStore, running, applicationName, 10*time.Second,
	)
	if operation.State != "preparing" {
		t.Fatalf("blocked epoch operation state=%q want=preparing", operation.State)
	}
	assertPendingMarker(t, pendingMarker, operation, transition)
	assertSameEpochRecord(t, initialEpoch, readStateEpoch(t, queries), "blocked redis prepare")
	operationCount := countRuntimeStateEpochOperations(t, pool)

	running.killAndWait(t)
	terminateMaintenanceApplicationBackends(t, pool, applicationName)
	waitForMaintenanceApplicationExit(t, pool, applicationName, 5*time.Second)
	afterCrash := readNonterminalEpochOperation(t, queries)
	assertOperationUnchanged(t, operation, afterCrash)
	assertSameEpochRecord(t, initialEpoch, readStateEpoch(t, queries), "prepare crash rollback")
	assertPendingMarker(t, readStateIntegrity(t, integrityStore), operation, transition)

	// The killed transaction is gone before this unlock, so it cannot advance after
	// the test releases the blocker. The next command must perform the recovery.
	blocker.release(t)
	deleteRedisKeys(
		t,
		h.redis,
		markerKey,
		h.namespace+":runtime-control:v1:op:"+operation.Token,
	)
	if marker := readStateIntegrity(t, integrityStore); marker.Exists {
		t.Fatalf("prepare-crash pending marker was not removed: %+v", marker)
	}

	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, beginArgs...), "awaiting_maintenance")
	recoveredOperation := readNonterminalEpochOperation(t, queries)
	recoveredTransition := decodeEpochTransition(t, recoveredOperation)
	recoveringEpoch := readStateEpoch(t, queries)
	assertOperationIdentity(t, operation, recoveredOperation)
	if recoveredOperation.State != "db_committed" ||
		string(recoveredOperation.EpochTransition) != string(operation.EpochTransition) ||
		countRuntimeStateEpochOperations(t, pool) != operationCount {
		t.Fatalf(
			"prepare-crash recovery replaced or aborted the immutable operation: before=%+v after=%+v",
			operation,
			recoveredOperation,
		)
	}
	assertRecoveringTransition(
		t,
		initialEpoch,
		recoveringEpoch,
		recoveredTransition,
		recoveredOperation,
		readStateIntegrity(t, integrityStore),
		recoveryID,
		runtimecontrol.StateEpochReasonRestore,
	)

	resumeGatewayProcesses(h.gateways[:])
	gatewaysPaused = false
	for _, gateway := range h.gateways {
		h.waitReadiness(t, gateway, http.StatusServiceUnavailable, 5*time.Second)
	}
	assertSixModesRejectedWithoutUpstream(t, h, nil)

	approvedAt := time.Now().UTC()
	recoveryEvidence := approvedMaintenanceEvidence(t, recoveredTransition, approvedAt)
	recoveryEvidencePath := writeMaintenanceEvidence(t, "prepare-crash-recovery", recoveryEvidence)
	commitArgs := []string{
		"commit",
		"--evidence-file", recoveryEvidencePath,
		"--recovery-id", recoveryID,
		"--revision", strconv.FormatInt(recoveredTransition.NewRevision, 10),
	}
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, commitArgs...), "awaiting_release")

	readyLockedEpoch := readStateEpoch(t, queries)
	readyLockedMarker := readStateIntegrity(t, integrityStore)
	awaitingRelease := readNonterminalEpochOperation(t, queries)
	if readyLockedEpoch.Value.State != runtimecontrol.StateEpochReady || readyLockedEpoch.Value.ActivatedAt == nil ||
		readyLockedEpoch.Value.Epoch != recoveredTransition.NewEpoch ||
		readyLockedEpoch.Revision != recoveredTransition.NewRevision ||
		!readyLockedMarker.Ready(recoveredTransition.NewEpoch, recoveredTransition.NewRevision) ||
		readyLockedMarker.LastOperationToken != operation.Token ||
		awaitingRelease.Token != operation.Token || awaitingRelease.State != "awaiting_release" {
		t.Fatalf(
			"prepare-crash commit did not preserve the recovered operation: epoch=%+v marker=%+v operation=%+v",
			readyLockedEpoch,
			readyLockedMarker,
			awaitingRelease,
		)
	}
	for _, gateway := range h.gateways {
		h.assertReadiness(t, gateway, http.StatusServiceUnavailable)
	}

	waitForMaintenanceSmokeAdmission(t, h, integrityStore, queries, 15*time.Second)
	smokeCheckedAt, smokeSummary := runSixModeMaintenanceSmoke(t, h)
	releaseEvidence := releaseMaintenanceEvidence(t, recoveredTransition, smokeCheckedAt, smokeSummary)
	releaseEvidencePath := writeMaintenanceEvidence(t, "prepare-crash-release", releaseEvidence)
	releaseArgs := []string{
		"release",
		"--evidence-file", releaseEvidencePath,
		"--recovery-id", recoveryID,
		"--revision", strconv.FormatInt(recoveredTransition.NewRevision, 10),
	}
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, releaseArgs...), "ready")
	assertSameEpochRecord(t, readyLockedEpoch, readStateEpoch(t, queries), "prepare-crash release")
	if _, err := queries.GetNonterminalRuntimeStateEpochOperation(context.Background()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("prepare-crash release left a nonterminal epoch operation: %v", err)
	}
	latest, err := queries.GetLatestCommittedRuntimeStateEpochOperation(context.Background())
	if err != nil || latest.Token != operation.Token || latest.State != "committed" || len(latest.ReleaseEvidence) == 0 {
		t.Fatalf("prepare-crash release did not commit the recovered operation: operation=%+v err=%v", latest, err)
	}
	for _, gateway := range h.gateways {
		h.waitReadiness(t, gateway, http.StatusOK, 5*time.Second)
	}
}

type epochPreparedCASBlocker struct {
	pool           *pgxpool.Pool
	lockConnection *pgxpool.Conn
	triggerPresent bool
}

func installEpochPreparedCASBlocker(t *testing.T, pool *pgxpool.Pool) *epochPreparedCASBlocker {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	functionDDL := fmt.Sprintf(`
CREATE OR REPLACE FUNCTION public.%s()
RETURNS trigger
LANGUAGE plpgsql
AS $function$
BEGIN
    IF OLD.kind = 'runtime_state_epoch'
       AND OLD.state = 'preparing'
       AND NEW.state = 'prepared' THEN
        PERFORM pg_advisory_xact_lock(%d);
    END IF;
    RETURN NEW;
END
$function$`, prepareCrashFunction, prepareCrashAdvisoryLock)
	if _, err := pool.Exec(ctx, functionDDL); err != nil {
		t.Fatalf("create prepare-crash trigger function: %v", err)
	}
	triggerDDL := fmt.Sprintf(`
CREATE TRIGGER %s
BEFORE UPDATE OF state ON public.runtime_control_operations
FOR EACH ROW EXECUTE FUNCTION public.%s()`, prepareCrashTrigger, prepareCrashFunction)
	if _, err := pool.Exec(ctx, triggerDDL); err != nil {
		_, _ = pool.Exec(context.Background(), "DROP FUNCTION IF EXISTS public."+prepareCrashFunction+"()")
		t.Fatalf("create prepare-crash trigger: %v", err)
	}

	lockConnection, err := pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquire prepare-crash advisory lock connection: %v", err)
	}
	if _, err := lockConnection.Exec(ctx, "SELECT pg_advisory_lock($1)", prepareCrashAdvisoryLock); err != nil {
		lockConnection.Release()
		t.Fatalf("acquire prepare-crash advisory lock: %v", err)
	}
	blocker := &epochPreparedCASBlocker{
		pool: pool, lockConnection: lockConnection, triggerPresent: true,
	}
	t.Cleanup(blocker.cleanup)
	return blocker
}

func (b *epochPreparedCASBlocker) release(t *testing.T) {
	t.Helper()
	if b.lockConnection != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		_, err := b.lockConnection.Exec(ctx, "SELECT pg_advisory_unlock($1)", prepareCrashAdvisoryLock)
		cancel()
		b.lockConnection.Release()
		b.lockConnection = nil
		if err != nil {
			t.Fatalf("release prepare-crash advisory lock: %v", err)
		}
	}
	if b.triggerPresent {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if _, err := b.pool.Exec(ctx, "DROP TRIGGER IF EXISTS "+prepareCrashTrigger+" ON public.runtime_control_operations"); err != nil {
			t.Fatalf("drop prepare-crash trigger: %v", err)
		}
		if _, err := b.pool.Exec(ctx, "DROP FUNCTION IF EXISTS public."+prepareCrashFunction+"()"); err != nil {
			t.Fatalf("drop prepare-crash trigger function: %v", err)
		}
		b.triggerPresent = false
	}
}

func (b *epochPreparedCASBlocker) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if b.lockConnection != nil {
		_, _ = b.lockConnection.Exec(ctx, "SELECT pg_advisory_unlock($1)", prepareCrashAdvisoryLock)
		b.lockConnection.Release()
		b.lockConnection = nil
	}
	if b.triggerPresent {
		_, _ = b.pool.Exec(ctx, "DROP TRIGGER IF EXISTS "+prepareCrashTrigger+" ON public.runtime_control_operations")
		_, _ = b.pool.Exec(ctx, "DROP FUNCTION IF EXISTS public."+prepareCrashFunction+"()")
		b.triggerPresent = false
	}
}

type prepareCrashMaintenanceCommand struct {
	command  *exec.Cmd
	done     chan error
	output   bytes.Buffer
	finished bool
}

func startPrepareCrashMaintenanceCommand(
	t *testing.T,
	h *faultHarness,
	binary string,
	applicationName string,
	args ...string,
) *prepareCrashMaintenanceCommand {
	t.Helper()
	command := exec.Command(binary, args...)
	command.Dir = h.root
	command.Env = minimalGatewayEnvironment(
		h.infra.databaseURL+"&application_name="+applicationName,
		h.infra.redisAddr,
		h.namespace,
		"p4-fault-prepare-crash",
		reservePort(t),
	)
	running := &prepareCrashMaintenanceCommand{command: command, done: make(chan error, 1)}
	command.Stdout = &running.output
	command.Stderr = &running.output
	if err := command.Start(); err != nil {
		t.Fatalf("start blocked runtime-state-maintenance: %v", err)
	}
	go func() {
		running.done <- command.Wait()
	}()
	t.Cleanup(running.cleanup)
	return running
}

func (c *prepareCrashMaintenanceCommand) killAndWait(t *testing.T) {
	t.Helper()
	if c.finished {
		t.Fatal("blocked runtime-state-maintenance exited before kill")
	}
	if err := c.command.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
		t.Fatalf("kill blocked runtime-state-maintenance: %v", err)
	}
	select {
	case err := <-c.done:
		c.finished = true
		if err == nil {
			t.Fatalf("killed runtime-state-maintenance exited successfully: %s", strings.TrimSpace(c.output.String()))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("killed runtime-state-maintenance did not exit")
	}
}

func (c *prepareCrashMaintenanceCommand) cleanup() {
	if c.finished || c.command.Process == nil {
		return
	}
	_ = c.command.Process.Kill()
	select {
	case <-c.done:
		c.finished = true
	case <-time.After(5 * time.Second):
	}
}

func waitForBlockedEpochPreparedCAS(
	t *testing.T,
	pool *pgxpool.Pool,
	queries *sqlc.Queries,
	store *breakerstore.Store,
	running *prepareCrashMaintenanceCommand,
	applicationName string,
	timeout time.Duration,
) (sqlc.RuntimeControlOperation, runtimecontrol.StateEpochTransition, breakerstore.StateIntegritySnapshot) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var lastState string
	var lastMarker breakerstore.StateIntegritySnapshot
	var lastWaiters int
	for time.Now().Before(deadline) {
		select {
		case err := <-running.done:
			running.finished = true
			t.Fatalf(
				"runtime-state-maintenance exited before prepared CAS was blocked: err=%v output=%s",
				err,
				strings.TrimSpace(running.output.String()),
			)
		default:
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		operation, operationErr := queries.GetNonterminalRuntimeStateEpochOperation(ctx)
		if operationErr == nil {
			lastState = operation.State
			transition, transitionErr := runtimecontrol.DecodeStateEpochTransition(operation.EpochTransition)
			marker, markerErr := store.StateIntegrity(ctx)
			lastMarker = marker
			waiters, waiterErr := prepareCrashApplicationCount(ctx, pool, applicationName, true)
			lastWaiters = waiters
			if operation.State == "preparing" && transitionErr == nil && markerErr == nil && waiterErr == nil &&
				marker.Exists && marker.State == "pending" && marker.OperationToken == operation.Token &&
				marker.TransitionHash == operation.PayloadHash && waiters == 1 {
				cancel()
				return operation, transition, marker
			}
		}
		cancel()
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf(
		"prepared CAS did not block within %s: operation_state=%q marker=%+v waiters=%d",
		timeout,
		lastState,
		lastMarker,
		lastWaiters,
	)
	return sqlc.RuntimeControlOperation{}, runtimecontrol.StateEpochTransition{}, breakerstore.StateIntegritySnapshot{}
}

func waitForMaintenanceApplicationExit(
	t *testing.T,
	pool *pgxpool.Pool,
	applicationName string,
	timeout time.Duration,
) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	var active int
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		var err error
		active, err = prepareCrashApplicationCount(ctx, pool, applicationName, false)
		cancel()
		if err == nil && active == 0 {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("maintenance PostgreSQL sessions still active after kill: application=%s count=%d", applicationName, active)
}

func terminateMaintenanceApplicationBackends(
	t *testing.T,
	pool *pgxpool.Pool,
	applicationName string,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	rows, err := pool.Query(ctx, `
SELECT pid
FROM pg_stat_activity
WHERE datname = current_database()
  AND application_name = $1
  AND pid <> pg_backend_pid()`, applicationName)
	if err != nil {
		t.Fatalf("list maintenance PostgreSQL sessions after process kill: %v", err)
	}
	var pids []int32
	for rows.Next() {
		var pid int32
		if err := rows.Scan(&pid); err != nil {
			rows.Close()
			t.Fatalf("scan maintenance PostgreSQL backend pid: %v", err)
		}
		pids = append(pids, pid)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		t.Fatalf("iterate maintenance PostgreSQL backend pids: %v", err)
	}
	rows.Close()

	for _, pid := range pids {
		var terminated bool
		if err := pool.QueryRow(ctx, "SELECT pg_terminate_backend($1)", pid).Scan(&terminated); err != nil || !terminated {
			t.Fatalf("terminate maintenance PostgreSQL backend pid=%d: terminated=%v err=%v", pid, terminated, err)
		}
	}
}

func prepareCrashApplicationCount(
	ctx context.Context,
	pool *pgxpool.Pool,
	applicationName string,
	onlyAdvisoryWaiters bool,
) (int, error) {
	query := `
SELECT count(*)
FROM pg_stat_activity
WHERE datname = current_database()
  AND application_name = $1`
	if onlyAdvisoryWaiters {
		query += `
  AND wait_event_type = 'Lock'
  AND wait_event = 'advisory'
  AND query LIKE '%UPDATE runtime_control_operations%'`
	}
	var count int
	err := pool.QueryRow(ctx, query, applicationName).Scan(&count)
	return count, err
}

func countRuntimeStateEpochOperations(t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM runtime_control_operations WHERE kind = 'runtime_state_epoch'`).Scan(&count); err != nil {
		t.Fatalf("count runtime state epoch operations: %v", err)
	}
	return count
}
