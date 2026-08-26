// 价格侧供给联动的 DB 级测试（需 DATABASE_URL，缺省跳过）。
//
// 守的是同一条不变量的另一半：**enabled 的模型必定有可解析售价**。
// 渠道侧与价格侧失去支撑的性质不同，联动口径也因此不同：
//   - 渠道故障会自己恢复，所以那边允许管理员保留模型 enabled，等渠道回来自动可用；
//   - 撤掉售价是配置行为，不会自己恢复。保留 enabled 只能让模型一直停在
//     「列表里有、一调失败」的状态，所以这里确认后强制下架，不给「保留」选项。
package supply_test

import (
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/service/admin/modelprice"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
)

func newModelPriceService(f *fixture) *modelprice.Service {
	return modelprice.NewService(f.queries, f.pool, f.queries)
}

// currentPriceID 返回该模型此刻生效中的价格行 id。
func (f *fixture) currentPriceID(modelID int64) int64 {
	f.t.Helper()
	var id int64
	err := f.pool.QueryRow(f.ctx, `
		SELECT id FROM model_prices
		WHERE model_id = $1 AND status = 'enabled'
		  AND effective_from <= now()
		  AND (effective_to IS NULL OR effective_to > now())
		ORDER BY effective_from DESC, id DESC
		LIMIT 1
	`, modelID).Scan(&id)
	if err != nil {
		f.t.Fatalf("load current price for model %d: %v", modelID, err)
	}
	return id
}

func (f *fixture) priceStatus(priceID int64) string {
	f.t.Helper()
	var status string
	if err := f.pool.QueryRow(f.ctx, `SELECT status FROM model_prices WHERE id = $1`, priceID).Scan(&status); err != nil {
		f.t.Fatalf("load price %d: %v", priceID, err)
	}
	return status
}

func (f *fixture) modelDisabledReason(modelID int64) string {
	f.t.Helper()
	var reason *string
	if err := f.pool.QueryRow(f.ctx, `SELECT disabled_reason FROM models WHERE id = $1`, modelID).Scan(&reason); err != nil {
		f.t.Fatalf("load disabled_reason for model %d: %v", modelID, err)
	}
	if reason == nil {
		return ""
	}
	return *reason
}

// 禁用唯一生效价格就是撤掉该模型的售价：必须先确认，确认后在同一事务里下架模型。
//
// 确认时不携带任何 SelectedModels——价格侧的下架是强制的，管理员确认的是
// 「撤价并下架」这件事本身，而不是在保留与下架之间做选择。
func TestModelPriceDisableLastRequiresConfirmationAndDelists(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("price-last"), "enabled")
	channelID := f.channel(providerID, uniqueName("price-last-channel"), "openai", "enabled")
	modelID := f.model(uniqueName("openai/price-last"), "enabled")
	f.binding(channelID, modelID, "enabled")
	f.pricing(channelID, modelID)
	priceID := f.currentPriceID(modelID)

	service := newModelPriceService(f)
	_, err := service.Update(f.ctx, modelprice.UpdateInput{ID: priceID, Status: "disabled"})
	confirm := confirmationFrom(t, err)
	if len(confirm.Impact.AffectedModels) != 1 || confirm.Impact.AffectedModels[0].ModelID != modelID {
		t.Fatalf("impact must name the model losing its sale price: %+v", confirm.Impact.AffectedModels)
	}
	if got := f.priceStatus(priceID); got != "enabled" {
		t.Fatalf("rejected operation must not change the price row, got %q", got)
	}
	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("rejected operation must not change the model, got %q", got)
	}

	if _, err := service.Update(f.ctx, modelprice.UpdateInput{
		ID: priceID, Status: "disabled",
		Confirmation: supply.Confirmation{Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint()},
	}); err != nil {
		t.Fatalf("confirmed price disable: %v", err)
	}
	if got := f.priceStatus(priceID); got != "disabled" {
		t.Fatalf("price status = %q, want disabled", got)
	}
	if got := f.modelStatus(modelID); got != "disabled" {
		t.Fatalf("model must be delisted in the same transaction, got %q", got)
	}
	if got := f.modelDisabledReason(modelID); got != supply.ReasonPriceDisabled {
		t.Fatalf("disabled_reason = %q, want %q", got, supply.ReasonPriceDisabled)
	}
}

// 关窗与禁用对客户完全等价：把 effective_to 改到当下，模型立刻失去售价，
// 所以走同一道门。只堵禁用不堵关窗等于没堵。
func TestModelPriceCloseWindowRequiresConfirmation(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("price-close"), "enabled")
	channelID := f.channel(providerID, uniqueName("price-close-channel"), "openai", "enabled")
	modelID := f.model(uniqueName("openai/price-close"), "enabled")
	f.binding(channelID, modelID, "enabled")
	f.pricing(channelID, modelID)
	priceID := f.currentPriceID(modelID)

	// 窗口起点是一小时前，把终点挪到半小时前即「此刻起不再供价」，与停用等价。
	closeAt := time.Now().Add(-30 * time.Minute)
	service := newModelPriceService(f)
	_, err := service.Update(f.ctx, modelprice.UpdateInput{
		ID: priceID, Status: "enabled", EffectiveTo: &closeAt,
	})
	confirm := confirmationFrom(t, err)
	if len(confirm.Impact.AffectedModels) != 1 || confirm.Impact.AffectedModels[0].ModelID != modelID {
		t.Fatalf("closing the only window must name the affected model: %+v", confirm.Impact.AffectedModels)
	}

	if _, err := service.Update(f.ctx, modelprice.UpdateInput{
		ID: priceID, Status: "enabled", EffectiveTo: &closeAt,
		Confirmation: supply.Confirmation{Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint()},
	}); err != nil {
		t.Fatalf("confirmed window close: %v", err)
	}
	if got := f.modelStatus(modelID); got != "disabled" {
		t.Fatalf("model must be delisted when its last window closes, got %q", got)
	}
}

// 用一条同样可售的新窗口替换旧窗口，模型全程有售价，不该打扰管理员。
func TestModelPriceReplaceWithSellableNeedsNoConfirmation(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("price-replace"), "enabled")
	channelID := f.channel(providerID, uniqueName("price-replace-channel"), "openai", "enabled")
	modelID := f.model(uniqueName("openai/price-replace"), "enabled")
	f.binding(channelID, modelID, "enabled")
	f.pricing(channelID, modelID)

	if _, err := newModelPriceService(f).Create(f.ctx, modelprice.CreateInput{
		ModelID:                   modelID,
		Intent:                    modelprice.IntentBase,
		Currency:                  "USD",
		PricingUnit:               modelprice.PricingUnitPer1MTokens,
		UncachedInputPrice:        "120",
		OutputPrice:               "240",
		ReplaceOverlappingEnabled: true,
		Status:                    "enabled",
		EffectiveFrom:             time.Now(),
	}); err != nil {
		t.Fatalf("replace with a sellable window: %v", err)
	}
	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("model must stay enabled across a sellable replacement, got %q", got)
	}
}

// 模型本来就没在售，撤价对客户没有任何新影响，不需要确认。
func TestModelPriceDisableOnDisabledModelNeedsNoConfirmation(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("price-off"), "enabled")
	channelID := f.channel(providerID, uniqueName("price-off-channel"), "openai", "enabled")
	modelID := f.model(uniqueName("openai/price-off"), "disabled")
	f.binding(channelID, modelID, "enabled")
	f.pricing(channelID, modelID)
	priceID := f.currentPriceID(modelID)

	if _, err := newModelPriceService(f).Update(f.ctx, modelprice.UpdateInput{
		ID: priceID, Status: "disabled",
	}); err != nil {
		t.Fatalf("disabling a price of an already delisted model: %v", err)
	}
	if got := f.priceStatus(priceID); got != "disabled" {
		t.Fatalf("price status = %q, want disabled", got)
	}
}

// 创建一条有到期时间、之后没有接续窗口的价格：到期那刻模型会静默失去售价，
// 而那条路上没有任何管理员操作可以拦截，所以创建时就要把话说在前面。
func TestModelPriceCreateWarnsOnExpiryWithoutSuccessor(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("price-expiry"), "enabled")
	channelID := f.channel(providerID, uniqueName("price-expiry-channel"), "openai", "enabled")
	modelID := f.model(uniqueName("openai/price-expiry"), "enabled")
	f.binding(channelID, modelID, "enabled")
	f.pricing(channelID, modelID)

	expiresAt := time.Now().Add(24 * time.Hour)
	created, err := newModelPriceService(f).Create(f.ctx, modelprice.CreateInput{
		ModelID:                   modelID,
		Intent:                    modelprice.IntentBase,
		Currency:                  "USD",
		PricingUnit:               modelprice.PricingUnitPer1MTokens,
		UncachedInputPrice:        "120",
		OutputPrice:               "240",
		ReplaceOverlappingEnabled: true,
		Status:                    "enabled",
		EffectiveFrom:             time.Now(),
		EffectiveTo:               &expiresAt,
	})
	if err != nil {
		t.Fatalf("create expiring window: %v", err)
	}
	if len(created.Warnings) == 0 {
		t.Fatal("creating a window that expires with no successor must warn the operator")
	}
}
