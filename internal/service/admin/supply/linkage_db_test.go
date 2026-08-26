// 供给状态归属与显式联合操作的 DB 级测试（需 DATABASE_URL，缺省跳过）。
//
// 这里守的是一条不变量：**enabled 的模型必定至少有一条可用渠道**。
// 它有两个方向都会被打破，所以两侧都要测：
//   - 减法侧：停用/解除绑定、停用渠道，可能让某个模型失去最后一条支撑；
//   - 加法侧：启用模型时如果还没有可用渠道，就会造出「列表里有、一调 503」。
//
// 另外测影响指纹漂移与 Model 锁串行化——它们保证管理员确认的就是即将发生的事。
package supply_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodel"
	adminmodel "github.com/ThankCat/unio-gateway/internal/service/admin/model"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
)

// fixture 是一组提交到真实数据库的供给事实，测试结束按依赖顺序清理。
type fixture struct {
	t       *testing.T
	ctx     context.Context
	pool    *pgxpool.Pool
	queries *sqlc.Queries

	providerIDs []int64
	channelIDs  []int64
	modelIDs    []int64
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		cancel()
		t.Fatalf("create postgres pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		cancel()
		t.Fatalf("ping postgres: %v", err)
	}
	f := &fixture{t: t, ctx: ctx, pool: pool, queries: sqlc.New(pool)}
	t.Cleanup(func() {
		f.cleanup()
		pool.Close()
		cancel()
	})
	return f
}

func (f *fixture) cleanup() {
	// 依赖顺序：价格 → 绑定 → 渠道 → 服务商 → 模型。
	for _, id := range f.channelIDs {
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM channel_prices WHERE channel_id = $1`, id)
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM channel_cost_multipliers WHERE channel_id = $1`, id)
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM channel_models WHERE channel_id = $1`, id)
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM channels WHERE id = $1`, id)
	}
	for _, id := range f.providerIDs {
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM providers WHERE id = $1`, id)
	}
	for _, id := range f.modelIDs {
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM model_prices WHERE model_id = $1`, id)
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM models WHERE id = $1`, id)
	}
}

func (f *fixture) provider(slug, status string) int64 {
	f.t.Helper()
	var id int64
	err := f.pool.QueryRow(f.ctx, `
		INSERT INTO providers (slug, name, origin, status)
		VALUES ($1, $2, 'https://' || $1 || '.example.test', $3)
		RETURNING id
	`, slug, slug, status).Scan(&id)
	if err != nil {
		f.t.Fatalf("insert provider %q: %v", slug, err)
	}
	f.providerIDs = append(f.providerIDs, id)
	return id
}

func (f *fixture) channel(providerID int64, name, protocol, status string) int64 {
	f.t.Helper()
	var id int64
	err := f.pool.QueryRow(f.ctx, `
		INSERT INTO channels (provider_id, name, protocols, adapter_key, credential, status, priority)
		VALUES ($1, $2, ARRAY[$3]::text[], ARRAY[$3]::text[], 'sk-test-' || $2, $4, 10)
		RETURNING id
	`, providerID, name, protocol, status).Scan(&id)
	if err != nil {
		f.t.Fatalf("insert channel %q: %v", name, err)
	}
	f.channelIDs = append(f.channelIDs, id)
	return id
}

func (f *fixture) model(publicID, status string) int64 {
	f.t.Helper()
	var id int64
	err := f.pool.QueryRow(f.ctx, `
		INSERT INTO models (model_id, display_name, owned_by, status)
		VALUES ($1, $1, 'test', $2)
		RETURNING id
	`, publicID, status).Scan(&id)
	if err != nil {
		f.t.Fatalf("insert model %q: %v", publicID, err)
	}
	f.modelIDs = append(f.modelIDs, id)
	return id
}

func (f *fixture) binding(channelID, modelID int64, status string) {
	f.t.Helper()
	_, err := f.pool.Exec(f.ctx, `
		INSERT INTO channel_models (channel_id, model_id, upstream_model, status)
		VALUES ($1, $2, 'upstream-model', $3)
	`, channelID, modelID, status)
	if err != nil {
		f.t.Fatalf("insert binding channel=%d model=%d: %v", channelID, modelID, err)
	}
}

// pricing 给「模型基准价 + 渠道绝对成本」配一对安全价格：
// 售价高于成本，既满足毛利守卫，也让该绑定具备可解析成本、能进候选。
func (f *fixture) pricing(channelID, modelID int64) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO model_prices (
			model_id, currency, pricing_unit, uncached_input_price, output_price,
			sale_price_ratio, status, effective_from
		)
		VALUES ($1, 'USD', 'per_1m_tokens', 100, 200, 1, 'enabled', now() - interval '1 hour')
		ON CONFLICT DO NOTHING
	`, modelID); err != nil {
		f.t.Fatalf("insert model price: %v", err)
	}
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO channel_prices (channel_id, model_id, currency, pricing_unit, uncached_input_cost, output_cost, status, effective_from)
		VALUES ($1, $2, 'USD', 'per_1m_tokens', 1, 2, 'enabled', now() - interval '1 hour')
		ON CONFLICT DO NOTHING
	`, channelID, modelID); err != nil {
		f.t.Fatalf("insert channel price: %v", err)
	}
}

// channelCostOnly 只配渠道成本，故意不给模型基准价：
// 用于验证「成本可解析但模型没定价」时不允许启用。
func (f *fixture) channelCostOnly(channelID, modelID int64) {
	f.t.Helper()
	if _, err := f.pool.Exec(f.ctx, `
		INSERT INTO channel_prices (channel_id, model_id, currency, pricing_unit, uncached_input_cost, output_cost, status, effective_from)
		VALUES ($1, $2, 'USD', 'per_1m_tokens', 1, 2, 'enabled', now() - interval '1 hour')
		ON CONFLICT DO NOTHING
	`, channelID, modelID); err != nil {
		f.t.Fatalf("insert channel price: %v", err)
	}
}

func (f *fixture) bindingStatus(channelID, modelID int64) string {
	f.t.Helper()
	row, err := f.queries.GetChannelModel(f.ctx, sqlc.GetChannelModelParams{ChannelID: channelID, ModelID: modelID})
	if err != nil {
		f.t.Fatalf("get binding: %v", err)
	}
	return row.Status
}

func (f *fixture) modelStatus(modelID int64) string {
	f.t.Helper()
	row, err := f.queries.LookupModelByID(f.ctx, modelID)
	if err != nil {
		f.t.Fatalf("lookup model: %v", err)
	}
	return row.Status
}

func uniqueName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}

// confirmationFrom 断言错误是二次确认要求，并返回最新指纹。
func confirmationFrom(t *testing.T, err error) *supply.ConfirmationRequired {
	t.Helper()
	var confirm *supply.ConfirmationRequired
	if !errors.As(err, &confirm) {
		t.Fatalf("expected ConfirmationRequired, got %v", err)
	}
	return confirm
}

// registryStub 满足 channel.AdapterRegistry（状态归属测试不校验 adapter 组合）。
type registryStub struct{}

func (registryStub) HasAny(string, string) bool  { return true }
func (registryStub) AdapterKeys(string) []string { return nil }

func newChannelModelService(f *fixture) *channelmodel.Service {
	return channelmodel.NewService(f.queries, f.pool, f.queries)
}

func newModelService(f *fixture) *adminmodel.Service {
	return adminmodel.NewService(f.queries, f.pool, f.queries)
}

func newChannelService(f *fixture) *adminchannel.Service {
	return adminchannel.NewService(f.queries, registryStub{}).WithSupplyLinkage(f.pool, f.queries)
}

// 还有替代绑定时，停用一条绑定不影响供给，直接执行、无需确认。
func TestBindingDisableWithAlternativeNeedsNoConfirmation(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("supply-alt"), "enabled")
	primary := f.channel(providerID, uniqueName("supply-alt-primary"), "openai", "enabled")
	backup := f.channel(providerID, uniqueName("supply-alt-backup"), "openai", "enabled")
	modelID := f.model(uniqueName("openai/supply-alt"), "enabled")
	f.binding(primary, modelID, "enabled")
	f.binding(backup, modelID, "enabled")
	f.pricing(primary, modelID)
	f.pricing(backup, modelID)

	if _, err := newChannelModelService(f).Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: primary, ModelID: modelID, UpstreamModel: "upstream-model", Status: "disabled",
	}); err != nil {
		t.Fatalf("disable binding with alternative: %v", err)
	}
	if got := f.bindingStatus(primary, modelID); got != "disabled" {
		t.Fatalf("binding status = %q, want disabled", got)
	}
	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("model must stay enabled while another channel supplies it, got %q", got)
	}
}

// 停用最后一条绑定会让模型失去全部供给，必须先确认；
// 勾选该模型则连带停用，避免它停在「列表里有、一调 503」的状态。
func TestBindingDisableLastSupplyRequiresConfirmation(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("supply-last"), "enabled")
	channelID := f.channel(providerID, uniqueName("supply-last-channel"), "openai", "enabled")
	modelID := f.model(uniqueName("openai/supply-last"), "enabled")
	f.binding(channelID, modelID, "enabled")
	f.pricing(channelID, modelID)

	service := newChannelModelService(f)
	_, err := service.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: channelID, ModelID: modelID, UpstreamModel: "upstream-model", Status: "disabled",
	})
	confirm := confirmationFrom(t, err)
	if len(confirm.Impact.AffectedModels) != 1 || confirm.Impact.AffectedModels[0].ModelID != modelID {
		t.Fatalf("impact must name the model losing supply: %+v", confirm.Impact.AffectedModels)
	}
	if got := f.bindingStatus(channelID, modelID); got != "enabled" {
		t.Fatalf("rejected operation must not change the binding, got %q", got)
	}

	if _, err := service.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: channelID, ModelID: modelID, UpstreamModel: "upstream-model", Status: "disabled",
		Confirmation: supply.Confirmation{
			Confirm:             true,
			ExpectedFingerprint: confirm.Impact.Fingerprint(),
			SelectedModels:      []supply.ModelSelection{{ModelID: modelID}},
		},
	}); err != nil {
		t.Fatalf("confirmed disable: %v", err)
	}
	if got := f.bindingStatus(channelID, modelID); got != "disabled" {
		t.Fatalf("binding status = %q, want disabled", got)
	}
	if got := f.modelStatus(modelID); got != "disabled" {
		t.Fatalf("selected model must be disabled together, got %q", got)
	}
}

// 不勾选也合法：管理员接受该模型短期返回 503，等渠道恢复。
func TestBindingDisableWithoutSelectionKeepsModelEnabled(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("supply-keep"), "enabled")
	channelID := f.channel(providerID, uniqueName("supply-keep-channel"), "openai", "enabled")
	modelID := f.model(uniqueName("openai/supply-keep"), "enabled")
	f.binding(channelID, modelID, "enabled")
	f.pricing(channelID, modelID)

	service := newChannelModelService(f)
	_, err := service.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: channelID, ModelID: modelID, UpstreamModel: "upstream-model", Status: "disabled",
	})
	confirm := confirmationFrom(t, err)

	if _, err := service.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: channelID, ModelID: modelID, UpstreamModel: "upstream-model", Status: "disabled",
		Confirmation: supply.Confirmation{Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint()},
	}); err != nil {
		t.Fatalf("confirmed disable without selection: %v", err)
	}
	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("unselected model must keep its status, got %q", got)
	}
}

// 加法侧守卫：没有可用渠道时不允许启用模型，否则当场破坏不变量。
func TestModelEnableRequiresRuntimeSupply(t *testing.T) {
	f := newFixture(t)
	modelID := f.model(uniqueName("openai/supply-no-channel"), "disabled")

	_, err := newModelService(f).Update(f.ctx, adminmodel.UpdateInput{ID: modelID, DisplayName: "supply-test-model", OwnedBy: "test", Status: "enabled"})
	if err == nil {
		t.Fatal("enabling a model without any usable channel must be rejected")
	}
	if got := f.modelStatus(modelID); got != "disabled" {
		t.Fatalf("rejected enable must not change status, got %q", got)
	}

	providerID := f.provider(uniqueName("supply-enable"), "enabled")
	channelID := f.channel(providerID, uniqueName("supply-enable-channel"), "openai", "enabled")
	f.binding(channelID, modelID, "enabled")
	f.pricing(channelID, modelID)

	if _, err := newModelService(f).Update(f.ctx, adminmodel.UpdateInput{ID: modelID, DisplayName: "supply-test-model", OwnedBy: "test", Status: "enabled"}); err != nil {
		t.Fatalf("enable model with a usable channel: %v", err)
	}
	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("model status = %q, want enabled", got)
	}
}

// 加法侧守卫（定价）：渠道齐备但模型没有生效基准价时同样不允许启用。
//
// 候选查询取基准价用的是 INNER JOIN LATERAL，没有价格行就没有候选。少了这条检查，
// 模型会停在「已启用、出现在 /v1/models、每次调用都 404」的状态上，破坏「列出即可调用」。
func TestModelEnableRequiresModelPrice(t *testing.T) {
	f := newFixture(t)
	modelID := f.model(uniqueName("openai/supply-no-price"), "disabled")
	providerID := f.provider(uniqueName("supply-no-price"), "enabled")
	channelID := f.channel(providerID, uniqueName("supply-no-price-channel"), "openai", "enabled")
	f.binding(channelID, modelID, "enabled")
	f.channelCostOnly(channelID, modelID)

	_, err := newModelService(f).Update(f.ctx, adminmodel.UpdateInput{
		ID: modelID, DisplayName: "supply-test-model", OwnedBy: "test", Status: "enabled"})
	if err == nil {
		t.Fatal("enabling a model without an effective base price must be rejected")
	}
	if got := f.modelStatus(modelID); got != "disabled" {
		t.Fatalf("rejected enable must not change status, got %q", got)
	}

	// 补上基准价后同一次调用应当通过，确认拦截原因确实是缺价而非别的条件。
	f.pricing(channelID, modelID)
	if _, err := newModelService(f).Update(f.ctx, adminmodel.UpdateInput{
		ID: modelID, DisplayName: "supply-test-model", OwnedBy: "test", Status: "enabled"}); err != nil {
		t.Fatalf("enable model after pricing it: %v", err)
	}
	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("model status = %q, want enabled", got)
	}
}

// 停用渠道会让模型失去运行候选，同样要确认；勾选则连带停用模型。
func TestChannelDisableConfirmsAffectedModels(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("supply-chdis"), "enabled")
	channelID := f.channel(providerID, uniqueName("supply-chdis-channel"), "openai", "enabled")
	modelID := f.model(uniqueName("openai/supply-chdis"), "enabled")
	f.binding(channelID, modelID, "enabled")
	f.pricing(channelID, modelID)

	service := newChannelService(f)
	fast := false
	channelName := uniqueName("supply-chdis-channel-renamed")
	_, err := service.Update(f.ctx, adminchannel.UpdateInput{
		ID: channelID, ProviderID: providerID, Name: channelName,
		Status: "disabled", Priority: 10, SupportsOpenAIFast: &fast,
	})
	confirm := confirmationFrom(t, err)
	if len(confirm.Impact.AffectedModels) != 1 || confirm.Impact.AffectedModels[0].ModelID != modelID {
		t.Fatalf("impact must name the model losing its last runtime candidate: %+v", confirm.Impact.AffectedModels)
	}

	if _, err := service.Update(f.ctx, adminchannel.UpdateInput{
		ID: channelID, ProviderID: providerID, Name: channelName,
		Status: "disabled", Priority: 10, SupportsOpenAIFast: &fast,
		Confirmation: supply.Confirmation{
			Confirm:             true,
			ExpectedFingerprint: confirm.Impact.Fingerprint(),
			SelectedModels:      []supply.ModelSelection{{ModelID: modelID}},
		},
	}); err != nil {
		t.Fatalf("confirmed channel disable: %v", err)
	}
	if got := f.modelStatus(modelID); got != "disabled" {
		t.Fatalf("selected model must be disabled with the channel, got %q", got)
	}
}

// 指纹漂移必须重新确认：管理员看到预览后事实发生变化，旧确认一律作废。
func TestFingerprintDriftRequiresReconfirmation(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("supply-drift"), "enabled")
	channelID := f.channel(providerID, uniqueName("supply-drift-channel"), "openai", "enabled")
	modelID := f.model(uniqueName("openai/supply-drift"), "enabled")
	f.binding(channelID, modelID, "enabled")
	f.pricing(channelID, modelID)

	service := newChannelModelService(f)
	_, err := service.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: channelID, ModelID: modelID, UpstreamModel: "upstream-model", Status: "disabled",
	})
	stale := confirmationFrom(t, err).Impact.Fingerprint()

	_, err = service.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: channelID, ModelID: modelID, UpstreamModel: "upstream-model", Status: "disabled",
		Confirmation: supply.Confirmation{
			Confirm:             true,
			ExpectedFingerprint: stale + "-drifted",
			SelectedModels:      []supply.ModelSelection{{ModelID: modelID}},
		},
	})
	if confirm := confirmationFrom(t, err); confirm.Impact.Fingerprint() == stale+"-drifted" {
		t.Fatal("mismatched fingerprint must be rejected with a fresh preview")
	}
	if got := f.bindingStatus(channelID, modelID); got != "enabled" {
		t.Fatalf("rejected operation must not change the binding, got %q", got)
	}
}

// Model 行锁把并发的供给写事务串起来：两个事务同时改同一模型时，一个必须等另一个提交。
func TestModelLockSerializesConcurrentWriters(t *testing.T) {
	f := newFixture(t)
	modelID := f.model(uniqueName("openai/supply-lock"), "enabled")

	first, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatalf("begin first tx: %v", err)
	}
	defer func() { _ = first.Rollback(f.ctx) }()
	if err := supply.LockModels(f.ctx, f.queries.WithTx(first), []int64{modelID}); err != nil {
		t.Fatalf("first lock: %v", err)
	}

	var wg sync.WaitGroup
	acquired := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		second, err := f.pool.Begin(f.ctx)
		if err != nil {
			return
		}
		defer func() { _ = second.Rollback(f.ctx) }()
		if err := supply.LockModels(f.ctx, f.queries.WithTx(second), []int64{modelID}); err == nil {
			close(acquired)
		}
	}()

	select {
	case <-acquired:
		t.Fatal("second writer must block until the first transaction commits")
	case <-time.After(300 * time.Millisecond):
	}

	if err := first.Commit(f.ctx); err != nil {
		t.Fatalf("commit first tx: %v", err)
	}
	wg.Wait()
	select {
	case <-acquired:
	default:
		t.Fatal("second writer must acquire the lock after the first commit")
	}
}
