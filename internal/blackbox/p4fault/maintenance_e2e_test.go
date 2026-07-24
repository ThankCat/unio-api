package p4fault_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	redislib "github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const maintenanceOperatorRef = "p4-fault-e2e"

type maintenanceLossMode string

const (
	maintenanceMarkerEndpointLoss maintenanceLossMode = "marker_endpoint_loss"
	maintenanceFullRedisLoss       maintenanceLossMode = "full_redis_loss"
	maintenanceAOFRestoreLoss      maintenanceLossMode = "aof_restore_loss"
	maintenanceRDBRestoreLoss      maintenanceLossMode = "rdb_restore_loss"
)

// TestP4FullStateLossMaintenanceE2E is an opt-in acceptance drill for the complete
// FLUSHDB -> begin -> commit -> six-protocol smoke -> release recovery sequence,
// followed by a second confirmed state-loss recovery on the next exact revision.
// It remains opt-in because it builds a maintenance binary and creates isolated
// PostgreSQL/Redis/Gateway infrastructure through the shared fault harness.
func TestP4FullStateLossMaintenanceE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" || os.Getenv("P4_FULL_STATE_LOSS_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 and P4_FULL_STATE_LOSS_E2E=1 to run the full state-loss maintenance drill")
	}
	runStateLossMaintenanceE2E(t, setupFaultHarness(t), maintenanceFullRedisLoss)
}

func runStateLossMaintenanceE2E(t *testing.T, h *faultHarness, lossMode maintenanceLossMode) {
	t.Helper()

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
	oldReadyMarker := readRedisHash(t, h.redis, markerKey)
	if h.upstream.snapshot().total != 0 {
		t.Fatal("maintenance drill requires zero customer traffic before the declared restore")
	}

	pauseGatewayProcesses(t, h.gateways[:])
	gatewaysPaused := true
	defer func() {
		if gatewaysPaused {
			resumeGatewayProcesses(h.gateways[:])
		}
	}()

	var aofSnapshot redisAOFSnapshot
	var rdbSnapshot redisRDBSnapshot
	var oldCircuitBreakerControl map[string]string
	if lossMode == maintenanceAOFRestoreLoss || lossMode == maintenanceRDBRestoreLoss {
		controlKey := h.namespace + ":runtime-control:v1:setting:gateway.circuit_breaker"
		oldCircuitBreakerControl = readRedisHash(t, h.redis, controlKey)
		switch lossMode {
		case maintenanceAOFRestoreLoss:
			aofSnapshot = archiveRedisAOF(t, h.infra)
		case maintenanceRDBRestoreLoss:
			rdbSnapshot = archiveRedisRDB(t, h.infra)
		}
		assertRedisHashRestored(t, h.redis, markerKey, oldReadyMarker, "pre-recovery ready marker")
		assertRedisHashRestored(t, h.redis, controlKey, oldCircuitBreakerControl, "pre-recovery runtime control")
	}

	detectedAt := time.Now().UTC()
	recoveryID := "p4-fault-restore-" + randomSuffix(t)
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
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, beginArgs...), "awaiting_maintenance")

	endpoint := readNonterminalEpochEndpoint(t, queries)
	transition := decodeEpochTransition(t, endpoint)
	recoveringEpoch := readStateEpoch(t, queries)
	pendingMarker := readStateIntegrity(t, integrityStore)
	assertRecoveringTransition(
		t, initialEpoch, recoveringEpoch, transition, endpoint, pendingMarker,
		recoveryID, runtimecontrol.StateEpochReasonRestore,
	)

	// The same pending marker and immutable endpoint must make begin idempotent.
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, beginArgs...), "awaiting_maintenance")
	assertEndpointIdentity(t, endpoint, readNonterminalEpochEndpoint(t, queries))
	assertPendingMarker(t, readStateIntegrity(t, integrityStore), endpoint, transition)

	if lossMode == maintenanceAOFRestoreLoss || lossMode == maintenanceRDBRestoreLoss {
		controlKey := h.namespace + ":runtime-control:v1:setting:gateway.circuit_breaker"
		switch lossMode {
		case maintenanceAOFRestoreLoss:
			restoreRedisAOF(t, h.infra, aofSnapshot)
		case maintenanceRDBRestoreLoss:
			restoreRedisRDB(t, h.infra, rdbSnapshot)
		}
		assertRedisHashRestored(t, h.redis, markerKey, oldReadyMarker, "restored old ready marker")
		assertRedisHashRestored(t, h.redis, controlKey, oldCircuitBreakerControl, "restored old runtime control")
		if restoredMarker := readStateIntegrity(t, integrityStore); !restoredMarker.Ready(initialEpoch.Value.Epoch, initialEpoch.Revision) {
			t.Fatalf("%s did not bring back the old ready marker: %+v", lossMode, restoredMarker)
		}
		assertSameEpochRecord(t, recoveringEpoch, readStateEpoch(t, queries), string(lossMode))
		assertEndpointUnchanged(t, endpoint, readNonterminalEpochEndpoint(t, queries))

		// PostgreSQL is already on the new recovering epoch. Even though Redis now
		// contains the old ready files, neither Gateway may treat them as current.
		resumeGatewayProcesses(h.gateways[:])
		gatewaysPaused = false
		for _, gateway := range h.gateways {
			h.waitReadiness(t, gateway, http.StatusServiceUnavailable, 5*time.Second)
		}
		assertSixModesRejectedWithoutUpstream(t, h, nil)

		pauseGatewayProcesses(t, h.gateways[:])
		gatewaysPaused = true
		assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, beginArgs...), "awaiting_maintenance")
		assertEndpointUnchanged(t, endpoint, readNonterminalEpochEndpoint(t, queries))
		assertPendingMarker(t, readStateIntegrity(t, integrityStore), endpoint, transition)
	} else {
		// A declared RDB/AOF rollback can bring the durable old ready marker back.
		// The same endpoint must rebuild its pending fence before any Gateway runs.
		replaceRedisHash(t, h.redis, markerKey, oldReadyMarker)
		assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, beginArgs...), "awaiting_maintenance")
		assertEndpointIdentity(t, endpoint, readNonterminalEpochEndpoint(t, queries))
		assertPendingMarker(t, readStateIntegrity(t, integrityStore), endpoint, transition)

		// A restore can also lose the marker and Redis endpoint entirely. PostgreSQL's
		// immutable transition must reconstruct both without creating another epoch.
		// The standard suite removes only the marker and Redis endpoint; the opt-in
		// full-loss drill uses FLUSHDB. Both must complete the same maintenance lifecycle,
		// while public readiness remains closed until Release.
		switch lossMode {
		case maintenanceMarkerEndpointLoss:
			deleteRedisKeys(
				t,
				h.redis,
				markerKey,
				h.namespace+":runtime-control:v1:op:"+endpoint.Token,
			)
		case maintenanceFullRedisLoss:
			flushIsolatedRedis(t, h.redis)
		default:
			t.Fatalf("unsupported maintenance loss mode %q", lossMode)
		}
		assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, beginArgs...), "awaiting_maintenance")
		endpoint = readNonterminalEpochEndpoint(t, queries)
		if !endpoint.ExpectedMarkerHash.Valid || endpoint.ExpectedMarkerHash.String != breakerstore.StateEpochExpectedMarkerAbsent {
			t.Fatalf("absent marker was not classified durably: %+v", endpoint.ExpectedMarkerHash)
		}
		assertPendingMarker(t, readStateIntegrity(t, integrityStore), endpoint, transition)
		pendingAfterAbsent := readRedisHash(t, h.redis, markerKey)

		// An unrelated ready marker is never adopted or overwritten. Restore the
		// captured same-endpoint pending hash only after proving the conflict path.
		conflictEpoch := strings.Repeat("f", 32)
		conflictRevision := transition.NewRevision + 99
		replaceRedisHash(t, h.redis, markerKey, map[string]string{
			"state":       "ready",
			"epoch":       conflictEpoch,
			"revision":    strconv.FormatInt(conflictRevision, 10),
			"marker_hash": breakerstore.StateIntegrityReadyMarkerHash(conflictEpoch, conflictRevision),
		})
		runMaintenanceCommandExpectFailure(t, h, maintenanceBinary, beginArgs...)
		conflictingMarker := readStateIntegrity(t, integrityStore)
		if !conflictingMarker.Ready(conflictEpoch, conflictRevision) {
			t.Fatalf("conflicting marker was overwritten: %+v", conflictingMarker)
		}
		conflictEndpoint := readNonterminalEpochEndpoint(t, queries)
		assertEndpointIdentity(t, endpoint, conflictEndpoint)
		if !conflictEndpoint.ExpectedMarkerHash.Valid || conflictEndpoint.ExpectedMarkerHash.String != breakerstore.StateEpochExpectedMarkerAbsent {
			t.Fatalf("conflict changed durable expected marker: %+v", conflictEndpoint.ExpectedMarkerHash)
		}
		assertSameEpochRecord(t, recoveringEpoch, readStateEpoch(t, queries), "marker conflict")

		replaceRedisHash(t, h.redis, markerKey, pendingAfterAbsent)
		assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, beginArgs...), "awaiting_maintenance")
		assertPendingMarker(t, readStateIntegrity(t, integrityStore), endpoint, transition)
	}

	resumeGatewayProcesses(h.gateways[:])
	gatewaysPaused = false
	for _, gateway := range h.gateways {
		h.waitReadiness(t, gateway, http.StatusServiceUnavailable, 5*time.Second)
	}
	assertSixModesRejectedWithoutUpstream(t, h, nil)
	waitForRecoveredRuntimeControls(t, h, 15*time.Second)

	approvedAt := time.Now().UTC()
	recoveryEvidence := approvedMaintenanceEvidence(t, transition, approvedAt)
	recoveryEvidencePath := writeMaintenanceEvidence(t, "recovery", recoveryEvidence)
	commitArgs := []string{
		"commit",
		"--evidence-file", recoveryEvidencePath,
		"--recovery-id", recoveryID,
		"--revision", strconv.FormatInt(transition.NewRevision, 10),
	}
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, commitArgs...), "awaiting_release")

	readyLockedEpoch := readStateEpoch(t, queries)
	readyLockedMarker := readStateIntegrity(t, integrityStore)
	awaitingRelease := readNonterminalEpochEndpoint(t, queries)
	if readyLockedEpoch.Value.State != runtimecontrol.StateEpochReady || readyLockedEpoch.Value.ActivatedAt == nil ||
		readyLockedEpoch.Value.Epoch != transition.NewEpoch || readyLockedEpoch.Revision != transition.NewRevision {
		t.Fatalf("commit did not activate the bound ready epoch: %+v", readyLockedEpoch)
	}
	if !readyLockedMarker.Ready(transition.NewEpoch, transition.NewRevision) ||
		readyLockedMarker.LastOperationToken != endpoint.Token || awaitingRelease.State != "awaiting_release" {
		t.Fatalf("commit did not keep the maintenance lock: marker=%+v endpoint=%+v", readyLockedMarker, awaitingRelease)
	}
	for _, gateway := range h.gateways {
		h.assertReadiness(t, gateway, http.StatusServiceUnavailable)
	}

	// A lost Commit response retries against the same new-ready marker and never
	// rotates the epoch or creates a second endpoint.
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, commitArgs...), "awaiting_release")
	assertEndpointIdentity(t, awaitingRelease, readNonterminalEpochEndpoint(t, queries))
	assertSameEpochRecord(t, readyLockedEpoch, readStateEpoch(t, queries), "commit retry")
	waitForMaintenanceSmokeAdmission(t, h, integrityStore, queries, 15*time.Second)

	// A post-commit upstream failure keeps ingress closed. Once the upstream is
	// repaired, smoke is rerun on this exact ready epoch instead of starting a new recovery.
	h.upstream.setFailure(true)
	beforeFailure := h.upstream.snapshot()
	status, body := h.request(t, h.gateways[0], modeOpenAIChatNonStream)
	h.upstream.setFailure(false)
	if status < 500 {
		t.Fatalf("injected post-commit smoke status=%d want=5xx body=%s", status, body)
	}
	afterFailure := h.upstream.snapshot()
	if afterFailure.total != beforeFailure.total+1 {
		faultExists, proofExists := runtimeRecoveryFencePresence(t, h)
		readinessSnapshot, readinessSnapshotErr := queries.GetGatewayRuntimeReadinessSnapshot(context.Background())
		readyStatus, readyBody, readyErr := h.get(h.gateways[0].baseURL + "/readyz")
		t.Fatalf(
			"failed smoke did not reach upstream: status=%d body=%s upstream_delta=%d "+
				"ready_status=%d ready_body=%s ready_err=%v infrastructure_fault=%v reconciliation_proof=%v "+
				"loss_mode=%s epoch=%+v endpoint_state=%s marker=%+v readiness_snapshot=%+v readiness_snapshot_err=%v gateway_log=%s",
			status, body, afterFailure.total-beforeFailure.total,
			readyStatus, readyBody, readyErr, faultExists, proofExists,
			lossMode, readStateEpoch(t, queries), readNonterminalEpochEndpoint(t, queries).State,
			readStateIntegrity(t, integrityStore), readinessSnapshot, readinessSnapshotErr, h.gateways[0].logs(),
		)
	}
	assertSameEpochRecord(t, readyLockedEpoch, readStateEpoch(t, queries), "failed smoke")
	assertEndpointIdentity(t, awaitingRelease, readNonterminalEpochEndpoint(t, queries))
	if marker := readStateIntegrity(t, integrityStore); !marker.Ready(transition.NewEpoch, transition.NewRevision) {
		t.Fatalf("failed smoke changed ready marker: %+v", marker)
	}

	smokeCheckedAt, smokeSummary := runSixModeMaintenanceSmoke(t, h)

	// Evidence collected before Redis/PG activation must not unlock the endpoint.
	preCommitRelease := releaseMaintenanceEvidence(
		t, transition, readyLockedEpoch.Value.ActivatedAt.Add(-time.Nanosecond), "pre-commit-smoke",
	)
	preCommitReleasePath := writeMaintenanceEvidence(t, "pre-commit-release", preCommitRelease)
	invalidReleaseArgs := []string{
		"release",
		"--evidence-file", preCommitReleasePath,
		"--recovery-id", recoveryID,
		"--revision", strconv.FormatInt(transition.NewRevision, 10),
	}
	runMaintenanceCommandExpectFailure(t, h, maintenanceBinary, invalidReleaseArgs...)
	assertEndpointIdentity(t, awaitingRelease, readNonterminalEpochEndpoint(t, queries))
	assertSameEpochRecord(t, readyLockedEpoch, readStateEpoch(t, queries), "invalid release evidence")

	releaseEvidence := releaseMaintenanceEvidence(
		t,
		transition,
		smokeCheckedAt,
		smokeSummary,
	)
	releaseEvidencePath := writeMaintenanceEvidence(t, "release", releaseEvidence)
	releaseArgs := []string{
		"release",
		"--evidence-file", releaseEvidencePath,
		"--recovery-id", recoveryID,
		"--revision", strconv.FormatInt(transition.NewRevision, 10),
	}
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, releaseArgs...), "ready")
	assertSameEpochRecord(t, readyLockedEpoch, readStateEpoch(t, queries), "release")
	if _, err := queries.GetNonterminalRuntimeStateEpochOperation(context.Background()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("release left a nonterminal epoch endpoint: %v", err)
	}
	latest, err := queries.GetLatestCommittedRuntimeStateEpochOperation(context.Background())
	if err != nil || latest.Token != endpoint.Token || latest.State != "committed" || len(latest.ReleaseEvidence) == 0 {
		t.Fatalf("release did not durably finish the same endpoint: endpoint=%+v err=%v", latest, err)
	}
	for _, gateway := range h.gateways {
		h.waitReadiness(t, gateway, http.StatusOK, 5*time.Second)
	}

	// Release response loss is idempotent and cannot advance the revision again.
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, releaseArgs...), "ready")
	assertSameEpochRecord(t, readyLockedEpoch, readStateEpoch(t, queries), "release retry")

	runRepeatedStateLossMaintenanceCycle(
		t,
		h,
		maintenanceBinary,
		queries,
		integrityStore,
		readyLockedEpoch,
		latest,
		beginArgs,
		commitArgs,
		releaseArgs,
	)
}

func runRepeatedStateLossMaintenanceCycle(
	t *testing.T,
	h *faultHarness,
	maintenanceBinary string,
	queries *sqlc.Queries,
	integrityStore *breakerstore.Store,
	currentEpoch runtimecontrol.StateEpochRecord,
	previousEndpoint sqlc.RuntimeControlOperation,
	staleBeginArgs []string,
	staleCommitArgs []string,
	staleReleaseArgs []string,
) {
	t.Helper()

	pauseGatewayProcesses(t, h.gateways[:])
	gatewaysPaused := true
	defer func() {
		if gatewaysPaused {
			resumeGatewayProcesses(h.gateways[:])
		}
	}()

	detectedAt := time.Now().UTC()
	recoveryID := "p4-fault-repeat-state-loss-" + randomSuffix(t)
	beginArgs := []string{
		"begin",
		"--reason", "state_loss",
		"--detected-at", detectedAt.Format(time.RFC3339Nano),
		"--not-before", detectedAt.Format(time.RFC3339Nano),
		"--operator-ref", maintenanceOperatorRef,
		"--recovery-id", recoveryID,
		"--expected-current-revision", strconv.FormatInt(currentEpoch.Revision, 10),
		"--confirm-state-loss",
		"--confirm-external-ingress-blocked",
	}
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, beginArgs...), "awaiting_maintenance")

	endpoint := readNonterminalEpochEndpoint(t, queries)
	transition := decodeEpochTransition(t, endpoint)
	recoveringEpoch := readStateEpoch(t, queries)
	assertRecoveringTransition(
		t,
		currentEpoch,
		recoveringEpoch,
		transition,
		endpoint,
		readStateIntegrity(t, integrityStore),
		recoveryID,
		runtimecontrol.StateEpochReasonStateLoss,
	)
	if endpoint.Token == previousEndpoint.Token ||
		endpoint.CurrentRevision != currentEpoch.Revision || endpoint.NextRevision != currentEpoch.Revision+1 {
		t.Fatalf("repeated state loss reused the old endpoint: previous=%+v current=%+v", previousEndpoint, endpoint)
	}

	// A completed recovery cannot be reused once the next endpoint is active.
	runMaintenanceCommandExpectFailure(t, h, maintenanceBinary, staleBeginArgs...)
	assertEndpointUnchanged(t, endpoint, readNonterminalEpochEndpoint(t, queries))
	assertSameEpochRecord(t, recoveringEpoch, readStateEpoch(t, queries), "stale begin")

	resumeGatewayProcesses(h.gateways[:])
	gatewaysPaused = false
	for _, gateway := range h.gateways {
		h.waitReadiness(t, gateway, http.StatusServiceUnavailable, 5*time.Second)
	}

	approvedAt := time.Now().UTC()
	recoveryEvidence := approvedMaintenanceEvidence(t, transition, approvedAt)
	recoveryEvidencePath := writeMaintenanceEvidence(t, "repeat-recovery", recoveryEvidence)
	commitArgs := []string{
		"commit",
		"--evidence-file", recoveryEvidencePath,
		"--recovery-id", recoveryID,
		"--revision", strconv.FormatInt(transition.NewRevision, 10),
	}

	// The previous endpoint's identity and approved evidence cannot commit this one.
	runMaintenanceCommandExpectFailure(t, h, maintenanceBinary, staleCommitArgs...)
	assertEndpointUnchanged(t, endpoint, readNonterminalEpochEndpoint(t, queries))
	assertSameEpochRecord(t, recoveringEpoch, readStateEpoch(t, queries), "stale commit evidence")

	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, commitArgs...), "awaiting_release")
	readyLockedEpoch := readStateEpoch(t, queries)
	readyLockedMarker := readStateIntegrity(t, integrityStore)
	awaitingRelease := readNonterminalEpochEndpoint(t, queries)
	if readyLockedEpoch.Value.State != runtimecontrol.StateEpochReady || readyLockedEpoch.Value.ActivatedAt == nil ||
		readyLockedEpoch.Value.Epoch != transition.NewEpoch || readyLockedEpoch.Revision != currentEpoch.Revision+1 ||
		!readyLockedMarker.Ready(transition.NewEpoch, transition.NewRevision) ||
		readyLockedMarker.LastOperationToken != endpoint.Token || awaitingRelease.State != "awaiting_release" ||
		awaitingRelease.Token != endpoint.Token {
		t.Fatalf(
			"repeated state-loss commit did not keep the new maintenance lock: epoch=%+v marker=%+v endpoint=%+v",
			readyLockedEpoch,
			readyLockedMarker,
			awaitingRelease,
		)
	}
	for _, gateway := range h.gateways {
		h.assertReadiness(t, gateway, http.StatusServiceUnavailable)
	}

	// The previous endpoint's smoke evidence cannot release the new lock.
	runMaintenanceCommandExpectFailure(t, h, maintenanceBinary, staleReleaseArgs...)
	assertEndpointUnchanged(t, awaitingRelease, readNonterminalEpochEndpoint(t, queries))
	assertSameEpochRecord(t, readyLockedEpoch, readStateEpoch(t, queries), "stale release evidence")
	for _, gateway := range h.gateways {
		h.assertReadiness(t, gateway, http.StatusServiceUnavailable)
	}

	waitForMaintenanceSmokeAdmission(t, h, integrityStore, queries, 15*time.Second)
	smokeCheckedAt, smokeSummary := runSixModeMaintenanceSmoke(t, h)
	releaseEvidence := releaseMaintenanceEvidence(t, transition, smokeCheckedAt, smokeSummary)
	releaseEvidencePath := writeMaintenanceEvidence(t, "repeat-release", releaseEvidence)
	releaseArgs := []string{
		"release",
		"--evidence-file", releaseEvidencePath,
		"--recovery-id", recoveryID,
		"--revision", strconv.FormatInt(transition.NewRevision, 10),
	}
	assertMaintenanceState(t, runMaintenanceCommand(t, h, maintenanceBinary, releaseArgs...), "ready")
	assertSameEpochRecord(t, readyLockedEpoch, readStateEpoch(t, queries), "repeated state-loss release")
	if _, err := queries.GetNonterminalRuntimeStateEpochOperation(context.Background()); !errors.Is(err, pgx.ErrNoRows) {
		t.Fatalf("repeated state-loss release left a nonterminal epoch endpoint: %v", err)
	}
	latest, err := queries.GetLatestCommittedRuntimeStateEpochOperation(context.Background())
	if err != nil || latest.Token != endpoint.Token || latest.Token == previousEndpoint.Token ||
		latest.State != "committed" || len(latest.ReleaseEvidence) == 0 {
		t.Fatalf("repeated state-loss release did not finish the new endpoint: endpoint=%+v err=%v", latest, err)
	}
	for _, gateway := range h.gateways {
		h.waitReadiness(t, gateway, http.StatusOK, 5*time.Second)
	}
}

func runSixModeMaintenanceSmoke(t *testing.T, h *faultHarness) (time.Time, string) {
	t.Helper()
	before := h.upstream.snapshot()
	for index, mode := range allProtocolModes {
		status, body := h.request(t, h.gateways[index%len(h.gateways)], mode)
		if status != http.StatusOK {
			t.Fatalf("%s post-commit smoke status=%d want=200 body=%s", mode, status, body)
		}
	}
	after := h.upstream.snapshot()
	if after.total-before.total != int64(len(allProtocolModes)) {
		t.Fatalf("successful smoke upstream calls=%d want=%d", after.total-before.total, len(allProtocolModes))
	}
	return time.Now().UTC(), fmt.Sprintf("six-mode-smoke:%d:%d", before.total, after.total)
}

func buildRuntimeStateMaintenanceBinary(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "runtime-state-maintenance")
	cmd := exec.Command("go", "build", "-o", path, "./cmd/runtime-state-maintenance")
	cmd.Dir = root
	cmd.Env = withoutEnvironment(os.Environ(), "LOG_FORMAT")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build runtime-state-maintenance: %v\n%s", err, output)
	}
	return path
}

func openMaintenanceDatabase(t *testing.T, databaseURL string) *pgxpool.Pool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("open maintenance database: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping maintenance database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

func runMaintenanceCommand(t *testing.T, h *faultHarness, binary string, args ...string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = h.root
	cmd.Env = minimalGatewayEnvironment(
		h.infra.databaseURL, h.infra.redisAddr, h.namespace, "p4-fault-maintenance", reservePort(t),
	)
	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("runtime-state-maintenance timed out")
	}
	if err != nil {
		t.Fatalf("runtime-state-maintenance failed: %v output=%s", err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output))
}

func runMaintenanceCommandExpectFailure(t *testing.T, h *faultHarness, binary string, args ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = h.root
	cmd.Env = minimalGatewayEnvironment(
		h.infra.databaseURL, h.infra.redisAddr, h.namespace, "p4-fault-maintenance", reservePort(t),
	)
	output, err := cmd.CombinedOutput()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		t.Fatal("runtime-state-maintenance failure path timed out")
	}
	if err == nil {
		t.Fatalf("unsafe runtime-state-maintenance command succeeded: %s", strings.TrimSpace(string(output)))
	}
}

func assertMaintenanceState(t *testing.T, output, want string) {
	t.Helper()
	if output != want {
		t.Fatalf("runtime-state-maintenance output=%q want=%q", output, want)
	}
}

func pauseGatewayProcesses(t *testing.T, gateways []*gatewayProcess) {
	t.Helper()
	for _, gateway := range gateways {
		if gateway == nil || gateway.cmd == nil || gateway.cmd.Process == nil {
			t.Fatal("cannot pause missing Gateway process")
		}
		if err := gateway.cmd.Process.Signal(syscall.SIGSTOP); err != nil {
			t.Fatalf("pause %s: %v", gateway.name, err)
		}
	}
}

func resumeGatewayProcesses(gateways []*gatewayProcess) {
	for _, gateway := range gateways {
		if gateway != nil && gateway.cmd != nil && gateway.cmd.Process != nil {
			_ = gateway.cmd.Process.Signal(syscall.SIGCONT)
		}
	}
}

func readStateEpoch(t *testing.T, queries *sqlc.Queries) runtimecontrol.StateEpochRecord {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	row, err := queries.GetAppSettingRecord(ctx, runtimecontrol.RuntimeStateEpochKey)
	if err != nil {
		t.Fatalf("read runtime state epoch: %v", err)
	}
	epoch, err := runtimecontrol.DecodeStateEpoch(row.Value)
	if err != nil {
		t.Fatalf("decode runtime state epoch: %v", err)
	}
	return runtimecontrol.StateEpochRecord{Value: epoch, Revision: row.Revision}
}

func readNonterminalEpochEndpoint(t *testing.T, queries *sqlc.Queries) sqlc.RuntimeControlOperation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	endpoint, err := queries.GetNonterminalRuntimeStateEpochOperation(ctx)
	if err != nil {
		t.Fatalf("read nonterminal runtime state endpoint: %v", err)
	}
	return endpoint
}

func decodeEpochTransition(t *testing.T, endpoint sqlc.RuntimeControlOperation) runtimecontrol.StateEpochTransition {
	t.Helper()
	transition, err := runtimecontrol.DecodeStateEpochTransition(endpoint.EpochTransition)
	if err != nil {
		t.Fatalf("decode runtime state transition: %v", err)
	}
	return transition
}

func readStateIntegrity(t *testing.T, store *breakerstore.Store) breakerstore.StateIntegritySnapshot {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	marker, err := store.StateIntegrity(ctx)
	if err != nil {
		t.Fatalf("read runtime integrity marker: %v", err)
	}
	return marker
}

func assertRecoveringTransition(
	t *testing.T,
	initial, recovering runtimecontrol.StateEpochRecord,
	transition runtimecontrol.StateEpochTransition,
	endpoint sqlc.RuntimeControlOperation,
	marker breakerstore.StateIntegritySnapshot,
	recoveryID string,
	reason runtimecontrol.StateEpochReason,
) {
	t.Helper()
	oldEpoch, oldRevision := transition.OldIdentity()
	if transition.RecoveryID == nil || *transition.RecoveryID != recoveryID ||
		transition.Reason != reason ||
		oldEpoch != initial.Value.Epoch || oldRevision != initial.Revision ||
		transition.NewRevision != initial.Revision+1 || transition.NewEpoch == initial.Value.Epoch {
		t.Fatalf("invalid immutable recovery transition: %+v", transition)
	}
	if recovering.Value.State != runtimecontrol.StateEpochRecovering ||
		recovering.Value.Epoch != transition.NewEpoch || recovering.Revision != transition.NewRevision ||
		endpoint.State != "db_committed" {
		t.Fatalf("begin did not establish recovering/db_committed: epoch=%+v endpoint=%+v", recovering, endpoint)
	}
	assertPendingMarker(t, marker, endpoint, transition)
}

func assertPendingMarker(
	t *testing.T,
	marker breakerstore.StateIntegritySnapshot,
	endpoint sqlc.RuntimeControlOperation,
	transition runtimecontrol.StateEpochTransition,
) {
	t.Helper()
	if !marker.Exists || marker.State != "pending" ||
		marker.OperationToken != endpoint.Token || marker.TransitionHash != endpoint.PayloadHash ||
		marker.NewEpoch != transition.NewEpoch || marker.NewRevision != transition.NewRevision {
		t.Fatalf("runtime state marker is not the bound pending fence: marker=%+v endpoint=%+v", marker, endpoint)
	}
}

func assertEndpointIdentity(t *testing.T, want, got sqlc.RuntimeControlOperation) {
	t.Helper()
	if got.Token != want.Token || got.PayloadHash != want.PayloadHash ||
		got.CurrentRevision != want.CurrentRevision || got.NextRevision != want.NextRevision {
		t.Fatalf("runtime state endpoint identity changed: want=%+v got=%+v", want, got)
	}
}

func assertEndpointUnchanged(t *testing.T, want, got sqlc.RuntimeControlOperation) {
	t.Helper()
	assertEndpointIdentity(t, want, got)
	if got.State != want.State ||
		got.ExpectedMarkerHash.Valid != want.ExpectedMarkerHash.Valid ||
		got.ExpectedMarkerHash.String != want.ExpectedMarkerHash.String ||
		string(got.RecoveryEvidence) != string(want.RecoveryEvidence) ||
		string(got.ReleaseEvidence) != string(want.ReleaseEvidence) {
		t.Fatalf("runtime state endpoint changed after rejected command: want=%+v got=%+v", want, got)
	}
}

func assertSameEpochRecord(t *testing.T, want, got runtimecontrol.StateEpochRecord, stage string) {
	t.Helper()
	wantActivated := want.Value.ActivatedAt
	gotActivated := got.Value.ActivatedAt
	if got.Value.Epoch != want.Value.Epoch || got.Value.State != want.Value.State ||
		got.Value.Reason != want.Value.Reason || got.Revision != want.Revision ||
		(wantActivated == nil) != (gotActivated == nil) ||
		(wantActivated != nil && !wantActivated.Equal(*gotActivated)) {
		t.Fatalf("%s changed runtime epoch: want=%+v got=%+v", stage, want, got)
	}
}

func readRedisHash(t *testing.T, client *redislib.Client, key string) map[string]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	fields, err := client.HGetAll(ctx, key).Result()
	if err != nil || len(fields) == 0 {
		t.Fatalf("read Redis hash %s: fields=%d err=%v", key, len(fields), err)
	}
	return fields
}

func replaceRedisHash(t *testing.T, client *redislib.Client, key string, fields map[string]string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	pipe := client.TxPipeline()
	pipe.Del(ctx, key)
	if len(fields) > 0 {
		pipe.HSet(ctx, key, fields)
	}
	if _, err := pipe.Exec(ctx); err != nil {
		t.Fatalf("replace Redis hash %s: %v", key, err)
	}
}

func flushIsolatedRedis(t *testing.T, client *redislib.Client) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush isolated Redis: %v", err)
	}
}

func deleteRedisKeys(t *testing.T, client *redislib.Client, keys ...string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := client.Del(ctx, keys...).Err(); err != nil {
		t.Fatalf("delete isolated Redis keys: %v", err)
	}
}

func waitForRecoveredRuntimeControls(t *testing.T, h *faultHarness, timeout time.Duration) {
	t.Helper()
	keys := []string{
		h.namespace + ":admission:v1:route-rate-limits",
		h.namespace + ":admission:v1:channel-rate-limits",
		h.namespace + ":admission:v1:global-concurrency",
		h.namespace + ":runtime-control:v1:setting:gateway.circuit_breaker",
		h.namespace + ":runtime-control:v1:setting:gateway.routing_balance",
		h.namespace + ":admission:v1:channel:" + formatID(h.seed.openAIChannelID),
		h.namespace + ":admission:v1:channel:" + formatID(h.seed.anthropicChannelID),
		h.namespace + ":breaker:v2:origin:" + formatID(h.seed.originID),
	}
	deadline := time.Now().Add(timeout)
	var existing int64
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		existing, _ = h.redis.Exists(ctx, keys...).Result()
		cancel()
		if existing == int64(len(keys)) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("runtime controls recovered=%d want=%d", existing, len(keys))
}

func runtimeRecoveryFencePresence(t *testing.T, h *faultHarness) (fault, proof bool) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	values, err := h.redis.Exists(
		ctx,
		h.namespace+":runtime-control:v1:infrastructure-fault",
		h.namespace+":runtime-control:v1:reconciliation-proof",
	).Result()
	if err != nil {
		t.Fatalf("read runtime recovery fence presence: %v", err)
	}
	if values == 0 {
		return false, false
	}
	faultValue, faultErr := h.redis.Exists(ctx, h.namespace+":runtime-control:v1:infrastructure-fault").Result()
	proofValue, proofErr := h.redis.Exists(ctx, h.namespace+":runtime-control:v1:reconciliation-proof").Result()
	if faultErr != nil || proofErr != nil {
		t.Fatalf("read runtime recovery fence details: fault_err=%v proof_err=%v", faultErr, proofErr)
	}
	return faultValue == 1, proofValue == 1
}

func waitForMaintenanceSmokeAdmission(
	t *testing.T,
	h *faultHarness,
	store *breakerstore.Store,
	queries *sqlc.Queries,
	timeout time.Duration,
) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	snapshot, err := queries.GetGatewayRuntimeReadinessSnapshot(ctx)
	cancel()
	if err != nil {
		t.Fatalf("read maintenance smoke readiness snapshot: %v", err)
	}
	epoch, err := runtimecontrol.DecodeStateEpoch(snapshot.RuntimeStateEpochValue)
	if err != nil {
		t.Fatalf("decode maintenance smoke epoch: %v", err)
	}
	if !snapshot.RuntimeMaintenanceSmokeAllowed {
		t.Fatalf("durable maintenance endpoint does not allow smoke: %+v", snapshot)
	}
	readinessInput := breakerstore.RuntimeReadinessInput{
		Epoch:                    epoch.Epoch,
		EpochRevision:            snapshot.RuntimeStateEpochRevision,
		RouteRateLimitRevision:   snapshot.RouteRateLimitDefaultsRevision,
		ChannelRateLimitRevision: snapshot.ChannelRateLimitDefaultsRevision,
		ConcurrencyRevision:      snapshot.ConcurrencyDefaultsRevision,
		CircuitBreakerRevision:   snapshot.CircuitBreakerRevision,
		RoutingBalanceRevision:   snapshot.RoutingBalanceRevision,
	}

	deadline := time.Now().Add(timeout)
	const stableFor = 500 * time.Millisecond
	var readySince time.Time
	var lastFault bool
	var lastRunID, lastProof, lastReason string
	var lastErr error
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		faultCount, faultErr := h.redis.Exists(
			ctx,
			h.namespace+":runtime-control:v1:infrastructure-fault",
		).Result()
		lastFault = faultCount != 0
		lastProof, lastErr = h.redis.Get(
			ctx,
			h.namespace+":runtime-control:v1:reconciliation-proof",
		).Result()
		if errors.Is(lastErr, redislib.Nil) {
			lastProof, lastErr = "", nil
		}
		var info string
		if faultErr == nil && lastErr == nil {
			info, lastErr = h.redis.Info(ctx, "server").Result()
			lastRunID = redisInfoField(info, "run_id")
		}
		cancel()

		lastReason = ""
		if faultErr == nil && lastErr == nil && !lastFault && lastRunID != "" && lastProof == lastRunID {
			ctx, cancel = context.WithTimeout(context.Background(), time.Second)
			readiness, readinessErr := store.CheckRuntimeReadiness(ctx, readinessInput)
			cancel()
			lastErr = readinessErr
			lastReason = readiness.Reason
			if readinessErr == nil && readiness.Ready {
				if readySince.IsZero() {
					readySince = time.Now()
				}
				if time.Since(readySince) >= stableFor {
					for _, gateway := range h.gateways {
						h.assertReadiness(t, gateway, http.StatusServiceUnavailable)
					}
					return
				}
				time.Sleep(100 * time.Millisecond)
				continue
			}
		}
		readySince = time.Time{}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf(
		"maintenance smoke admission did not reconcile within %s: infrastructure_fault=%v redis_run_id=%q reconciliation_proof=%q readiness_reason=%q err=%v",
		timeout,
		lastFault,
		lastRunID,
		lastProof,
		lastReason,
		lastErr,
	)
}

func redisInfoField(info, name string) string {
	prefix := name + ":"
	for _, line := range strings.Split(info, "\n") {
		line = strings.TrimSuffix(line, "\r")
		if value, found := strings.CutPrefix(line, prefix); found {
			return value
		}
	}
	return ""
}

func approvedMaintenanceEvidence(
	t *testing.T,
	transition runtimecontrol.StateEpochTransition,
	checkedAt time.Time,
) []byte {
	t.Helper()
	gate := func(name string) runtimecontrol.StateEpochRecoveryGate {
		at := checkedAt.UTC()
		digest := maintenanceDigest("recovery", *transition.RecoveryID, name)
		return runtimecontrol.StateEpochRecoveryGate{
			Status: runtimecontrol.StateEpochRecoveryGatePassed, CheckedAt: &at, SummaryHash: &digest,
		}
	}
	evidence := runtimecontrol.StateEpochRecoveryEvidence{
		SchemaVersion:   runtimecontrol.StateEpochRecoveryEvidenceSchemaVersion,
		RecoveryID:      *transition.RecoveryID,
		CurrentRevision: *transition.OldRevision,
		Reason:          transition.Reason,
		DetectedAt:      transition.DetectedAt,
		NotBefore:       transition.NotBefore,
		OperatorRef:     maintenanceOperatorRef,
		Status:          runtimecontrol.StateEpochRecoveryEvidenceApproved,
		RecordedAt:      checkedAt.UTC(),
		Gates: runtimecontrol.StateEpochRecoveryGates{
			IngressClosed:    gate("ingress_closed"),
			Drain:            gate("drain"),
			Window:           gate("window"),
			BreakerCooldown:  gate("breaker_cooldown"),
			Permission:       gate("permission"),
			Control:          gate("control"),
			OfflineScripts:   gate("offline_scripts"),
			MaintenanceProbe: gate("maintenance_probe"),
		},
	}
	raw, err := evidence.Marshal()
	if err != nil {
		t.Fatalf("marshal recovery evidence: %v", err)
	}
	return raw
}

func releaseMaintenanceEvidence(
	t *testing.T,
	transition runtimecontrol.StateEpochTransition,
	checkedAt time.Time,
	summary string,
) []byte {
	t.Helper()
	evidence := runtimecontrol.StateEpochReleaseEvidence{
		SchemaVersion: runtimecontrol.StateEpochReleaseEvidenceSchemaVersion,
		RecoveryID:    *transition.RecoveryID,
		Revision:      transition.NewRevision,
		Status:        runtimecontrol.StateEpochReleaseEvidencePassed,
		CheckedAt:     checkedAt.UTC(),
		SummaryHash:   maintenanceDigest("release", *transition.RecoveryID, summary),
	}
	raw, err := evidence.Marshal()
	if err != nil {
		t.Fatalf("marshal release evidence: %v", err)
	}
	return raw
}

func writeMaintenanceEvidence(t *testing.T, name string, raw []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name+".json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("write %s evidence: %v", name, err)
	}
	return path
}

func maintenanceDigest(parts ...string) string {
	hash := sha256.New()
	for _, part := range parts {
		_, _ = hash.Write([]byte(part))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
