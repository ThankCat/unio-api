package main

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

func deref(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}

func main() {
	ctx := context.Background()
	pool, _ := pgxpool.New(ctx, "postgres://unio:951215chenhao@127.0.0.1:5432/unio?sslmode=disable")
	defer pool.Close()

	var id int64
	var status string
	var acct *int64
	var snapshot string
	var lastSuccess bool
	_ = pool.QueryRow(ctx, `SELECT id, status, final_account_id FROM request_records WHERE final_channel_id = 1559 ORDER BY id DESC LIMIT 1`).Scan(&id, &status, &acct)
	fmt.Printf("request: id=%d status=%s final_account_id=%v\n", id, status, deref(acct))
	_ = pool.QueryRow(ctx, `SELECT COALESCE(usage_snapshot,'{}'::jsonb)::text, last_success_at IS NOT NULL FROM subscription_accounts WHERE id = 169`).Scan(&snapshot, &lastSuccess)
	fmt.Printf("account 169: last_success=%v snapshot=%s\n", lastSuccess, snapshot)
	var sel *int64
	var attempted []int64
	_ = pool.QueryRow(ctx, `SELECT selected_account_id, attempted_account_ids FROM routing_decision_traces ORDER BY id DESC LIMIT 1`).Scan(&sel, &attempted)
	fmt.Printf("trace: selected_account=%v attempted=%v\n", deref(sel), attempted)
	var disabledReason *string
	var acctStatus string
	_ = pool.QueryRow(ctx, `SELECT status, disabled_reason FROM subscription_accounts WHERE id = 168`).Scan(&acctStatus, &disabledReason)
	reason := ""
	if disabledReason != nil {
		reason = *disabledReason
	}
	fmt.Printf("real account 168: status=%s reason=%s\n", acctStatus, reason)

	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6380"})
	store := breakerstore.NewStore(client, "unio:dev")
	runtimes, err := store.AccountRuntimeMany(ctx, []int64{169})
	if err != nil {
		fmt.Println("runtime read:", err)
		return
	}
	rt := runtimes[0]
	fmt.Printf("account 169 runtime: usage_pause_ms=%d window=%s cooldown_ms=%d inflight=%d\n",
		rt.UsagePauseRemainingMs, rt.UsagePauseWindow, rt.CooldownRemainingMs, rt.InFlight)
}
