package sqlc_test

import (
	"context"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// EnableModelSupply 只匹配 status='disabled' 的行，所以「启用模型」这条路径必须先清原因、
// 再写其余字段。反过来先把状态改成 enabled，这条语句就再也匹配不到，模型会带着
// 「因渠道停用」的原因显示为已启用——状态和原因互相矛盾，排障时会指向不存在的渠道问题。
func TestEnableModelSupplyRequiresDisabledStatus(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	modelID := insertModel(t, ctx, tx, "enable-order-probe", "openai", "enabled")

	rows, err := queries.DisableModelSupply(ctx, sqlc.DisableModelSupplyParams{
		ID:     modelID,
		Reason: pgtype.Text{String: "channel_disabled", Valid: true},
	})
	if err != nil {
		t.Fatalf("disable model supply: %v", err)
	}
	if rows != 1 {
		t.Fatalf("disable affected %d rows, want 1", rows)
	}
	if reason := readDisabledReason(t, ctx, tx, modelID); reason != "channel_disabled" {
		t.Fatalf("disabled_reason = %q, want channel_disabled", reason)
	}

	// 模拟错误顺序：先把状态改回 enabled，再尝试清原因。
	if _, err := tx.Exec(ctx, `UPDATE models SET status = 'enabled' WHERE id = $1`, modelID); err != nil {
		t.Fatalf("force enable: %v", err)
	}
	rows, err = queries.EnableModelSupply(ctx, modelID)
	if err != nil {
		t.Fatalf("enable model supply: %v", err)
	}
	if rows != 0 {
		t.Fatalf("enable affected %d rows, want 0（状态已是 enabled，语句应当不匹配）", rows)
	}
	if reason := readDisabledReason(t, ctx, tx, modelID); reason != "channel_disabled" {
		t.Fatalf("错误顺序下 disabled_reason = %q，本用例正是要固化这个陷阱", reason)
	}

	// 正确顺序：状态仍为 disabled 时清原因。
	if _, err := tx.Exec(ctx, `UPDATE models SET status = 'disabled' WHERE id = $1`, modelID); err != nil {
		t.Fatalf("reset to disabled: %v", err)
	}
	rows, err = queries.EnableModelSupply(ctx, modelID)
	if err != nil {
		t.Fatalf("enable model supply: %v", err)
	}
	if rows != 1 {
		t.Fatalf("enable affected %d rows, want 1", rows)
	}
	if reason := readDisabledReason(t, ctx, tx, modelID); reason != "" {
		t.Fatalf("正确顺序下 disabled_reason = %q, want 空", reason)
	}
}

func readDisabledReason(t *testing.T, ctx context.Context, tx pgx.Tx, modelID int64) string {
	t.Helper()
	var reason *string
	if err := tx.QueryRow(ctx, `SELECT disabled_reason FROM models WHERE id = $1`, modelID).Scan(&reason); err != nil {
		t.Fatalf("read disabled_reason: %v", err)
	}
	if reason == nil {
		return ""
	}
	return *reason
}
