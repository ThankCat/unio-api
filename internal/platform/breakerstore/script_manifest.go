package breakerstore

const (
	luaRedisInstancePath      = "lua/helpers/redis_instance.lua"
	luaAuthoritativePath      = "lua/helpers/authoritative_control.lua"
	luaAttemptPermitGuardPath = "lua/helpers/attempt_permit_guard.lua"
	luaOriginFencePath        = "lua/helpers/origin_fence.lua"
)

var luaScriptManifest = []luaScriptSpec{
	{name: "attempt.acquire", helpers: []string{luaRedisInstancePath, luaAuthoritativePath}, main: "lua/ops/gate_and_acquire.lua"},
	{name: "attempt.finish", helpers: []string{luaAuthoritativePath, luaAttemptPermitGuardPath}, main: "lua/ops/finish.lua"},
	{name: "attempt.abort", helpers: []string{luaAttemptPermitGuardPath}, main: "lua/ops/abort.lua"},
	{name: "attempt.renew", helpers: []string{luaAttemptPermitGuardPath}, main: "lua/ops/renew.lua"},
	{name: "attempt.reset", main: "lua/ops/reset.lua"},
	{name: "attempt.snapshot", main: "lua/ops/snapshot.lua"},
	{name: "attempt.snapshot_many", helpers: []string{luaRedisInstancePath, luaAuthoritativePath}, main: "lua/ops/snapshot_many.lua"},
	{name: "attempt.set_cooldown", main: "lua/ops/set_cooldown.lua"},
	{name: "attempt.cooldown_remaining", main: "lua/ops/cooldown_remaining.lua"},
	{name: "attempt.record_sample", main: "lua/ops/record_sample.lua"},
	{name: "attempt.pause_permission", main: "lua/ops/pause_permission.lua"},
	{name: "attempt.clear_permission", main: "lua/ops/clear_permission.lua"},
	{name: "permission.recheck_claim", main: "lua/ops/permission_recheck_claim.lua"},
	{name: "permission.recheck_complete", main: "lua/ops/permission_recheck_complete.lua"},

	{name: "observation.record_tpm", main: "lua/ops/record_tpm_observation.lua"},
	{name: "observation.correct_tpm", main: "lua/ops/correct_tpm_observation.lua"},

	{name: "request.acquire", helpers: []string{luaRedisInstancePath, luaAuthoritativePath}, main: "lua/ops/acquire_request_admission.lua"},
	{name: "request.renew", main: "lua/ops/renew_request_admission.lua"},
	{name: "request.finish", main: "lua/ops/finish_request_admission.lua"},

	{name: "runtime.control_prepare", main: "lua/ops/control_prepare.lua"},
	{name: "runtime.control_commit", main: "lua/ops/control_commit.lua"},
	{name: "runtime.control_abort", main: "lua/ops/control_abort.lua"},
	{name: "runtime.control_read", main: "lua/ops/control_read.lua"},
	{name: "runtime.control_restore", main: "lua/ops/control_restore_missing.lua"},
	{name: "runtime.control_reconcile", main: "lua/ops/control_reconcile.lua"},
	{name: "runtime.control_recover_committed", main: "lua/ops/control_recover_committed.lua"},
	{name: "runtime.control_recover_aborted", main: "lua/ops/control_recover_aborted.lua"},

	{name: "integrity.read", main: "lua/ops/state_integrity_read.lua"},
	{name: "integrity.prepare", main: "lua/ops/epoch_prepare.lua"},
	{name: "integrity.commit", main: "lua/ops/epoch_commit.lua"},
	{name: "integrity.reconcile", main: "lua/ops/epoch_reconcile_ready.lua"},
	{name: "integrity.runtime_ready", helpers: []string{luaRedisInstancePath}, main: "lua/ops/runtime_readiness.lua"},
	{name: "integrity.fault_proof", helpers: []string{luaRedisInstancePath}, main: "lua/ops/runtime_fault_clear_proof.lua"},
	{name: "integrity.fault_clear", helpers: []string{luaRedisInstancePath}, main: "lua/ops/runtime_fault_clear_commit.lua"},
	{name: "integrity.fault_delete", helpers: []string{luaRedisInstancePath}, main: "lua/ops/runtime_fault_latch_delete.lua"},
	{name: "integrity.reconciliation_begin", helpers: []string{luaRedisInstancePath}, main: "lua/ops/begin_runtime_reconciliation.lua"},
	{name: "integrity.server_identity", helpers: []string{luaRedisInstancePath}, main: "lua/ops/redis_server_identity.lua"},

	{name: "origin.init", helpers: []string{luaOriginFencePath}, main: "lua/ops/init_provider_control.lua"},
	{name: "origin.restore", helpers: []string{luaOriginFencePath}, main: "lua/ops/restore_missing_provider_control.lua"},
	{name: "origin.reconcile", helpers: []string{luaOriginFencePath}, main: "lua/ops/reconcile_provider_control.lua"},
	{name: "origin.prepare_status", helpers: []string{luaOriginFencePath}, main: "lua/ops/prepare_origin_status.lua"},
	{name: "origin.commit_status", helpers: []string{luaOriginFencePath}, main: "lua/ops/commit_origin_status.lua"},
	{name: "origin.abort_status", helpers: []string{luaOriginFencePath}, main: "lua/ops/abort_origin_status.lua"},
	{name: "origin.prepare", helpers: []string{luaOriginFencePath}, main: "lua/ops/prepare_origin.lua"},
	{name: "origin.commit", helpers: []string{luaOriginFencePath}, main: "lua/ops/commit_origin.lua"},
	{name: "origin.abort", helpers: []string{luaOriginFencePath}, main: "lua/ops/abort_origin.lua"},
}
