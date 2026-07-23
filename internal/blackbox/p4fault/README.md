# P4 Fault E2E

This package runs destructive infrastructure drills only against resources it creates itself:

- a random PostgreSQL container, database, and volume;
- a random Redis container, namespace, and volume;
- two real `gateway-server` processes;
- an in-process mock upstream with atomic per-mode call counters.

The suite is off by default. Run it explicitly from the repository root:

```bash
P4_FAULT_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault
```

The base run covers six ingress modes (OpenAI Chat, OpenAI Responses, and Anthropic Messages, each streaming and non-streaming), shared breaker state across two Gateway instances, one missing critical control, Redis stop/restart with epoch preservation, undeclared `FLUSHDB` state loss, maintenance recovery after a lost marker/operation, and the full Compact Native 404/405 to Synthetic deployment path. Fault-path batches assert that rejected work does not increase the mock upstream call count.

The longer destructive drills have a second explicit gate and should be run independently:

```bash
P4_FAULT_E2E=1 P4_FULL_STATE_LOSS_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault -run '^TestP4FullStateLossMaintenanceE2E$'
P4_FAULT_E2E=1 P4_LONG_STREAM_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault -run '^TestP4InFlightLongStreamRedisFaultE2E$'
P4_FAULT_E2E=1 P4_LONG_STREAM_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault -run '^TestP4LongStreamRevisionFencesE2E$'
P4_FAULT_E2E=1 P4_PREPARE_CRASH_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault -run '^TestP4StateEpochPrepareCrashE2E$'
P4_FAULT_E2E=1 P4_AOF_RESTORE_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault -run '^TestP4AOFRestoreMaintenanceE2E$'
P4_FAULT_E2E=1 P4_RDB_RESTORE_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault -run '^TestP4RDBRestoreMaintenanceE2E$'
P4_FAULT_E2E=1 P4_ACTIVE_EPOCH_ROLLBACK_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault -run '^TestP4ActiveOwnersAOFEpochRollbackSafetyBoundaryE2E$'
P4_FAULT_E2E=1 P4_HALF_OPEN_LEASE_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault -run '^TestP4HalfOpenLeaseRenewalAndGatewayTakeoverE2E$'
P4_FAULT_E2E=1 P4_RESET_STALE_GENERATION_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault -run '^TestP4ResetStaleGenerationLongStreamE2E$'
```

Together these drills cover the full `FLUSHDB` maintenance lifecycle, a data-preserving Redis outage while a customer stream is in flight, BaseURL and credential revision fences across two in-flight streams, the Redis-Prepare/PostgreSQL-prepared crash window, marker truth-table and same-epoch retry behavior, revision+1 after repeated state loss, restoration of real Redis 7 AOF and RDB files, and the fail-closed safety boundary when an AOF rollback restores active owners from an old epoch. They also prove permit/request/concurrency lease renewal across multiple TTLs, Channel half-open recovery after one Gateway is killed, and rejection of an old stream's stale Finish after Reset and a complete new half-open round.

A BaseURL-stale Finish changes neither current Endpoint nor Channel runtime state. A credential-stale Finish may still apply to the current Endpoint when its Endpoint revision remains current, but it cannot change the current Channel breaker, TTFT, credential, or config revision. Customer delivery and billing still settle from the frozen attempt facts.

The package does not yet cover the 24-hour actual-time RPD/permission gate, an active-owner drain followed by commit/smoke/release, an active-owner RDB rollback, undeclared same-epoch rollback permission enforcement, an old-credential 401 tail, the Endpoint half-open deployment variant, external StarAPI Compact proxy cases, or the remaining plan matrix.

The test never reads the developer `.env`, existing `DATABASE_URL`, or existing Redis configuration. Containers and volumes use random names and are removed during test cleanup.

Redis Cluster/CROSSSLOT remains a separate negative deployment drill; this suite uses the supported single-node Redis topology.
