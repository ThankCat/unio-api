// export 把库里订阅账号的「当前活凭据」导出为 sub2api-data v1 文件（清库前保全会话）。
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, "postgres://unio:951215chenhao@127.0.0.1:5432/unio?sslmode=disable")
	if err != nil {
		panic(err)
	}
	defer pool.Close()
	rows, err := pool.Query(ctx, `SELECT upstream_account_id, display_name, COALESCE(plan_type,''), credentials, COALESCE(proxy_url,''), priority
		FROM subscription_accounts WHERE status <> 'archived' ORDER BY id`)
	if err != nil {
		panic(err)
	}
	type acct struct {
		Platform    string          `json:"platform"`
		Type        string          `json:"type"`
		Name        string          `json:"name"`
		Priority    int32           `json:"priority"`
		Credentials json.RawMessage `json:"credentials"`
		ProxyKey    string          `json:"proxy_key,omitempty"`
	}
	var accounts []acct
	for rows.Next() {
		var upstreamID, name, plan, proxy string
		var creds []byte
		var prio int32
		if err := rows.Scan(&upstreamID, &name, &plan, &creds, &proxy, &prio); err != nil {
			panic(err)
		}
		accounts = append(accounts, acct{Platform: "openai", Type: "oauth", Name: name, Priority: prio, Credentials: creds})
		fmt.Printf("exported: %s (%s) plan=%s proxy=%q\n", name, upstreamID, plan, proxy)
	}
	out := map[string]any{"type": "sub2api-data", "version": 1, "accounts": accounts, "proxies": []any{}}
	f, err := os.Create("/tmp/relaunch/live-accounts.json")
	if err != nil {
		panic(err)
	}
	defer f.Close()
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	if err := enc.Encode(out); err != nil {
		panic(err)
	}
	fmt.Println("written /tmp/relaunch/live-accounts.json, accounts =", len(accounts))
}
