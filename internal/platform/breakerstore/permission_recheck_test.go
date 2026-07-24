package breakerstore

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestPermissionRecheckQueueClaimBackoffAndClear(t *testing.T) {
	s, client, _ := newTestStore(t)
	ctx := context.Background()
	const channelID, modelID = int64(71), int64(701)

	if err := s.PauseChannelModelPermission(ctx, channelID, modelID, 5, 3, 4); err != nil {
		t.Fatalf("pause permission: %v", err)
	}
	// 同一绑定重复 403 仍只有一个有界队列 member。
	if err := s.PauseChannelModelPermission(ctx, channelID, modelID, 5, 3, 4); err != nil {
		t.Fatalf("repeat pause permission: %v", err)
	}
	if got := client.ZCard(ctx, s.keys.permissionRecheckQueue()).Val(); got != 1 {
		t.Fatalf("permission queue must deduplicate binding, got %d", got)
	}
	fields := client.HGetAll(ctx, s.keys.channelModelPermission(channelID, modelID)).Val()
	for field, want := range map[string]string{
		"channel_id": "71", "model_id": "701", "channel_config_revision": "5",
		"origin_base_url_revision": "3", "origin_status_revision": "4",
		"recheck_state": "queued", "last_rechecked_at_ms": "0",
	} {
		if fields[field] != want {
			t.Fatalf("permission field %s want %q got %q: %+v", field, want, fields[field], fields)
		}
	}

	task, err := s.ClaimPermissionRecheck(ctx, "worker-a", time.Second)
	if err != nil || task == nil {
		t.Fatalf("claim permission recheck: task=%+v err=%v", task, err)
	}
	if task.ChannelID != channelID || task.ModelID != modelID || task.Attempt != 1 {
		t.Fatalf("unexpected claimed task: %+v", task)
	}
	if second, err := s.ClaimPermissionRecheck(ctx, "worker-b", time.Second); err != nil || second != nil {
		t.Fatalf("leased task must not be double-claimed: task=%+v err=%v", second, err)
	}

	disposition, err := s.CompletePermissionRecheck(ctx, *task, PermissionRecheckFailed, 25*time.Millisecond)
	if err != nil || disposition != PermissionRecheckRescheduled {
		t.Fatalf("reschedule failed probe: disposition=%s err=%v", disposition, err)
	}
	if state := client.HGet(ctx, s.keys.channelModelPermission(channelID, modelID), "recheck_state").Val(); state != "retry_wait" {
		t.Fatalf("failed recheck must keep pause in retry_wait, got %q", state)
	}
	if immediate, err := s.ClaimPermissionRecheck(ctx, "worker-b", time.Second); err != nil || immediate != nil {
		t.Fatalf("retry backoff must prevent immediate claim: task=%+v err=%v", immediate, err)
	}
	time.Sleep(40 * time.Millisecond)
	retry, err := s.ClaimPermissionRecheck(ctx, "worker-b", time.Second)
	if err != nil || retry == nil || retry.Attempt != 2 {
		t.Fatalf("claim after retry backoff: task=%+v err=%v", retry, err)
	}
	disposition, err = s.CompletePermissionRecheck(ctx, *retry, PermissionRecheckSucceeded, 0)
	if err != nil || disposition != PermissionRecheckCleared {
		t.Fatalf("clear successful recheck: disposition=%s err=%v", disposition, err)
	}
	fields = client.HGetAll(ctx, s.keys.channelModelPermission(channelID, modelID)).Val()
	if fields["recheck_state"] != "cleared" || fields["last_rechecked_at_ms"] == "0" || fields["last_rechecked_at_ms"] == "" {
		t.Fatalf("successful recheck facts not retained: %+v", fields)
	}
	if got := client.ZCard(ctx, s.keys.permissionRecheckQueue()).Val(); got != 0 {
		t.Fatalf("cleared recheck must leave queue, got %d", got)
	}
}

func TestPermissionRecheckClaimIsAtomicAcrossWorkersAndLeaseRecovers(t *testing.T) {
	s, client, namespace := newTestStore(t)
	other := NewStore(client, namespace)
	ctx := context.Background()
	if err := s.PauseChannelModelPermission(ctx, 72, 702, 1, 1, 1); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	tasks := make(chan *PermissionRecheckTask, 2)
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for i, store := range []*Store{s, other} {
		wg.Add(1)
		go func(worker int, candidate *Store) {
			defer wg.Done()
			<-start
			task, err := candidate.ClaimPermissionRecheck(ctx, "worker-"+string(rune('a'+worker)), 30*time.Millisecond)
			tasks <- task
			errs <- err
		}(i, store)
	}
	close(start)
	wg.Wait()
	close(tasks)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent claim: %v", err)
		}
	}
	claimed := 0
	var first *PermissionRecheckTask
	for task := range tasks {
		if task != nil {
			claimed++
			first = task
		}
	}
	if claimed != 1 {
		t.Fatalf("exactly one worker may claim a lease, got %d", claimed)
	}

	time.Sleep(45 * time.Millisecond)
	recovered, err := other.ClaimPermissionRecheck(ctx, "worker-recovery", time.Second)
	if err != nil || recovered == nil {
		t.Fatalf("expired lease must be claimable: task=%+v err=%v", recovered, err)
	}
	if recovered.ClaimToken == first.ClaimToken || recovered.Attempt != 2 {
		t.Fatalf("recovered claim must have a new token/attempt: first=%+v recovered=%+v", first, recovered)
	}
	if disposition, err := s.CompletePermissionRecheck(ctx, *first, PermissionRecheckSucceeded, 0); err != nil || disposition != PermissionRecheckSuperseded {
		t.Fatalf("expired old claim must not clear newer claim: disposition=%s err=%v", disposition, err)
	}
}

func TestPermissionRecheckStaleRevisionAndBreakerResetStayIsolated(t *testing.T) {
	s, client, _ := newTestStore(t)
	ctx := context.Background()
	const channelID, modelID = int64(73), int64(703)
	if err := s.PauseChannelModelPermission(ctx, channelID, modelID, 2, 3, 4); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Reset(ctx, ScopeChannel, channelID); err != nil {
		t.Fatal(err)
	}
	if state := client.HGet(ctx, s.keys.channelModelPermission(channelID, modelID), "recheck_state").Val(); state != "queued" {
		t.Fatalf("breaker Reset must not clear permission pause, got %q", state)
	}

	oldTask, err := s.ClaimPermissionRecheck(ctx, "worker-old", time.Second)
	if err != nil || oldTask == nil {
		t.Fatalf("claim old task: task=%+v err=%v", oldTask, err)
	}
	// 新 revision 的 403 覆盖旧记录并立即形成新任务；旧 claim 的 CAS 不得触碰它。
	if err := s.PauseChannelModelPermission(ctx, channelID, modelID, 3, 3, 4); err != nil {
		t.Fatal(err)
	}
	// rev=2 的真实调用此时才返回 403：迟到反馈不得把 rev=3 的暂停覆盖回旧 revision。
	if err := s.PauseChannelModelPermission(ctx, channelID, modelID, 2, 3, 4); err != nil {
		t.Fatal(err)
	}
	if disposition, err := s.CompletePermissionRecheck(ctx, *oldTask, PermissionRecheckSucceeded, 0); err != nil || disposition != PermissionRecheckSuperseded {
		t.Fatalf("old revision completion must be superseded: disposition=%s err=%v", disposition, err)
	}
	current, err := s.ClaimPermissionRecheck(ctx, "worker-current", time.Second)
	if err != nil || current == nil || current.ChannelConfigRevision != 3 {
		t.Fatalf("claim current revision: task=%+v err=%v", current, err)
	}
	if disposition, err := s.CompletePermissionRecheck(ctx, *current, PermissionRecheckStale, 0); err != nil || disposition != PermissionRecheckMarkedStale {
		t.Fatalf("mark stale task: disposition=%s err=%v", disposition, err)
	}
	if got := client.ZCard(ctx, s.keys.permissionRecheckQueue()).Val(); got != 0 {
		t.Fatalf("stale old binding must leave queue, got %d", got)
	}
	if state := client.HGet(ctx, s.keys.channelModelPermission(channelID, modelID), "recheck_state").Val(); state != "stale" {
		t.Fatalf("stale audit state must be retained, got %q", state)
	}
}
