// ADR-0019 状态归属与显式联合操作 DB 级测试矩阵（需 DATABASE_URL，缺省跳过）。
//
// 覆盖：Binding 停用（有替代 / Route 内最后 / 全局最后）、解除绑定、Channel 实体停用、
// Model 全局暂停/下架、启用护栏、Route 保存差异更新（取消勾选 / 无关保存 / 原子换渠道 /
// 保留无支撑意图）、批量恢复、影响指纹漂移和 Model 锁串行化。
package supply_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	adminchannel "github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodel"
	adminmodel "github.com/ThankCat/unio-gateway/internal/service/admin/model"
	adminroute "github.com/ThankCat/unio-gateway/internal/service/admin/route"
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
	routeIDs    []int64
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
	// 依赖顺序：验证条目/批次 → 线路（级联 route_channels 与 offerings）→ 绑定 → 渠道 → 服务商 → 模型。
	for _, id := range f.channelIDs {
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM channel_model_verification_items WHERE run_id IN (SELECT id FROM channel_model_verification_runs WHERE channel_id = $1)`, id)
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM channel_model_verification_runs WHERE channel_id = $1`, id)
	}
	for _, id := range f.routeIDs {
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM routes WHERE id = $1`, id)
	}
	for _, id := range f.channelIDs {
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM channel_models WHERE channel_id = $1`, id)
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM channels WHERE id = $1`, id)
	}
	for _, id := range f.providerIDs {
		_, _ = f.pool.Exec(f.ctx, `DELETE FROM providers WHERE id = $1`, id)
	}
	for _, id := range f.modelIDs {
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
		INSERT INTO channels (provider_id, name, protocol, adapter_key, credential, status, priority)
		VALUES ($1, $2, $3, $3, 'sk-test-' || $2, $4, 10)
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

func (f *fixture) route(name string, channelIDs ...int64) int64 {
	f.t.Helper()
	var id int64
	err := f.pool.QueryRow(f.ctx, `
		INSERT INTO routes (name, mode, status, price_ratio)
		VALUES ($1, 'balanced', 'enabled', 1)
		RETURNING id
	`, name).Scan(&id)
	if err != nil {
		f.t.Fatalf("insert route %q: %v", name, err)
	}
	f.routeIDs = append(f.routeIDs, id)
	for _, channelID := range channelIDs {
		if _, err := f.pool.Exec(f.ctx, `INSERT INTO route_channels (route_id, channel_id) VALUES ($1, $2)`, id, channelID); err != nil {
			f.t.Fatalf("bind channel %d to route %d: %v", channelID, id, err)
		}
	}
	return id
}

func (f *fixture) offering(routeID, modelID int64, protocol string) {
	f.t.Helper()
	if err := f.queries.EnableRouteModelOffering(f.ctx, sqlc.EnableRouteModelOfferingParams{
		RouteID: routeID, ModelID: modelID, IngressProtocol: protocol,
	}); err != nil {
		f.t.Fatalf("enable offering route=%d model=%d: %v", routeID, modelID, err)
	}
}

// verificationEvidence 为绑定制造一条当前 revision 下 succeeded 的验证证据，返回 item id。
func (f *fixture) verificationEvidence(channelID, modelID int64) int64 {
	f.t.Helper()
	var runID int64
	err := f.pool.QueryRow(f.ctx, `
		INSERT INTO channel_model_verification_runs
			(channel_id, source, status, channel_config_revision, provider_origin_revision, provider_status_revision, total_count, succeeded_count)
		SELECT c.id, 'manual', 'succeeded', c.config_revision, p.origin_revision, p.status_revision, 1, 1
		FROM channels c JOIN providers p ON p.id = c.provider_id
		WHERE c.id = $1
		RETURNING id
	`, channelID).Scan(&runID)
	if err != nil {
		f.t.Fatalf("insert verification run: %v", err)
	}
	var itemID int64
	err = f.pool.QueryRow(f.ctx, `
		INSERT INTO channel_model_verification_items (run_id, model_id, upstream_model, status, success, http_status)
		VALUES ($1, $2, 'upstream-model', 'succeeded', true, 200)
		RETURNING id
	`, runID, modelID).Scan(&itemID)
	if err != nil {
		f.t.Fatalf("insert verification item: %v", err)
	}
	return itemID
}

func (f *fixture) offeringState(routeID, modelID int64, protocol string) (status string, reason *string) {
	f.t.Helper()
	rows, err := f.queries.ListRouteOfferingDetails(f.ctx, routeID)
	if err != nil {
		f.t.Fatalf("list offering details: %v", err)
	}
	for _, row := range rows {
		if row.ModelID == modelID && row.IngressProtocol == protocol {
			if row.DisabledReason.Valid {
				r := row.DisabledReason.String
				return row.Status, &r
			}
			return row.Status, nil
		}
	}
	f.t.Fatalf("offering route=%d model=%d protocol=%s not found", routeID, modelID, protocol)
	return "", nil
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

func newRouteService(f *fixture) *adminroute.Service {
	return adminroute.NewService(f.pool, f.queries)
}

// TestBindingDisableKeepsOfferingWithAlternative：Route 内仍有同协议替代 Binding 时，
// 停用一条 Binding 直接执行且 Offering 保持 enabled（必测矩阵第 1 行）。
func TestBindingDisableKeepsOfferingWithAlternative(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-alt"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-alt-a"), "openai", "enabled")
	chB := f.channel(providerID, uniqueName("sup-alt-b"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-alt-model"), "enabled")
	f.binding(chA, modelID, "enabled")
	f.binding(chB, modelID, "enabled")
	routeID := f.route(uniqueName("sup-alt-route"), chA, chB)
	f.offering(routeID, modelID, "openai")

	if _, err := newChannelModelService(f).Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusDisabled,
	}); err != nil {
		t.Fatalf("disable binding with alternative: %v", err)
	}

	if status, _ := f.offeringState(routeID, modelID, "openai"); status != "enabled" {
		t.Fatalf("offering status = %s, want enabled", status)
	}
	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("model status = %s, want enabled", got)
	}
}

// TestBindingDisableLastRouteSupport：Route 内最后一条同协议支撑停用需要指纹确认；
// 空 Offering 选择只停用 Binding，Offering 与 Model 均保持 enabled。
func TestBindingDisableLastRouteSupport(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-last"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-last-a"), "openai", "enabled")
	chOther := f.channel(providerID, uniqueName("sup-last-other"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-last-model"), "enabled")
	f.binding(chA, modelID, "enabled")
	f.binding(chOther, modelID, "enabled") // 全局仍有供给，但不在 route 池内。
	routeID := f.route(uniqueName("sup-last-route"), chA)
	f.offering(routeID, modelID, "openai")

	svc := newChannelModelService(f)
	_, err := svc.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusDisabled,
	})
	confirm := confirmationFrom(t, err)
	if confirm.Code != "channel_model_disable_confirmation_required" {
		t.Fatalf("confirmation code = %s", confirm.Code)
	}
	if confirm.Impact.RemainingEnabledBindings != 1 {
		t.Fatalf("remaining bindings = %d, want 1", confirm.Impact.RemainingEnabledBindings)
	}
	if len(confirm.Impact.AffectedOfferings) != 1 || confirm.Impact.AffectedOfferings[0].RouteID != routeID {
		t.Fatalf("affected offerings = %+v", confirm.Impact.AffectedOfferings)
	}

	if _, err := svc.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusDisabled,
		Confirmation: supply.Confirmation{Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint()},
	}); err != nil {
		t.Fatalf("confirmed disable: %v", err)
	}

	status, reason := f.offeringState(routeID, modelID, "openai")
	if status != "enabled" || reason != nil {
		t.Fatalf("offering = %s/%v, want enabled/nil", status, reason)
	}
	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("model status = %s, want enabled", got)
	}
}

// TestBindingDisableGlobalLastKeepsModel：全局最后一条 enabled Binding 停用经确认后，
// 只停用 Binding；Model 与未选择的 Offering 保持 enabled。
func TestBindingDisableGlobalLastKeepsModel(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-glast"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-glast-a"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-glast-model"), "enabled")
	f.binding(chA, modelID, "enabled")
	routeID := f.route(uniqueName("sup-glast-route"), chA)
	f.offering(routeID, modelID, "openai")

	svc := newChannelModelService(f)
	_, err := svc.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusDisabled,
	})
	confirm := confirmationFrom(t, err)
	if confirm.Impact.RemainingEnabledBindings != 0 {
		t.Fatalf("remaining bindings = %d, want 0", confirm.Impact.RemainingEnabledBindings)
	}

	if _, err := svc.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusDisabled,
		Confirmation: supply.Confirmation{Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint()},
	}); err != nil {
		t.Fatalf("confirmed disable: %v", err)
	}

	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("model status = %s, want enabled", got)
	}
	if got := f.bindingStatus(chA, modelID); got != "disabled" {
		t.Fatalf("binding status = %s, want disabled", got)
	}
	status, reason := f.offeringState(routeID, modelID, "openai")
	if status != "enabled" || reason != nil {
		t.Fatalf("offering = %s/%v, want enabled/nil", status, reason)
	}
}

// TestUnbindUsesSameLinkage：解除 enabled Binding 与停用使用相同影响预览和显式 Offering 选择。
func TestUnbindUsesSameLinkage(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-unbind"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-unbind-a"), "openai", "enabled")
	chOther := f.channel(providerID, uniqueName("sup-unbind-o"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-unbind-model"), "enabled")
	f.binding(chA, modelID, "enabled")
	f.binding(chOther, modelID, "enabled")
	routeID := f.route(uniqueName("sup-unbind-route"), chA)
	f.offering(routeID, modelID, "openai")

	svc := newChannelModelService(f)
	err := svc.Delete(f.ctx, chA, modelID, supply.Confirmation{})
	confirm := confirmationFrom(t, err)

	if err := svc.Delete(f.ctx, chA, modelID, supply.Confirmation{
		Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint(),
		SelectedOfferings: []supply.OfferingSelection{{
			RouteID: routeID, ModelID: modelID, IngressProtocol: "openai",
		}},
	}); err != nil {
		t.Fatalf("confirmed unbind: %v", err)
	}
	status, reason := f.offeringState(routeID, modelID, "openai")
	if status != "disabled" || reason == nil || *reason != supply.ReasonBindingDisabled {
		t.Fatalf("offering = %s/%v, want disabled/binding_disabled", status, reason)
	}
}

// TestChannelDisableKeepsSupplyIntent：Channel 暂停经影响确认后只修改 Channel；
// Binding、Model 与 Offering 均保留配置意图，恢复流量后无需逐层恢复。
func TestChannelDisableKeepsSupplyIntent(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-chd"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-chd-a"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-chd-model"), "enabled")
	f.binding(chA, modelID, "enabled")
	routeID := f.route(uniqueName("sup-chd-route"), chA)
	f.offering(routeID, modelID, "openai")

	svc := newChannelService(f)
	update := adminchannel.UpdateInput{
		ID: chA, Name: uniqueName("sup-chd-a"), ProviderID: providerID, Status: adminchannel.StatusDisabled, Priority: 10,
	}
	_, err := svc.Update(f.ctx, update)
	confirm := confirmationFrom(t, err)
	if confirm.Code != "channel_disable_confirmation_required" {
		t.Fatalf("confirmation code = %s", confirm.Code)
	}

	update.Confirmation = supply.Confirmation{Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint()}
	if _, err := svc.Update(f.ctx, update); err != nil {
		t.Fatalf("confirmed channel disable: %v", err)
	}

	status, reason := f.offeringState(routeID, modelID, "openai")
	if status != "enabled" || reason != nil {
		t.Fatalf("offering = %s/%v, want enabled/nil", status, reason)
	}
	if got := f.bindingStatus(chA, modelID); got != "enabled" {
		t.Fatalf("binding status = %s, want enabled (channel disable must not rewrite binding rows)", got)
	}
	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("model status = %s, want enabled (channel disable must not cascade the model)", got)
	}

	// 重新启用 Channel：恢复运行候选资格，其他层配置状态始终未变。
	update.Status = adminchannel.StatusEnabled
	update.Confirmation = supply.Confirmation{}
	if _, err := svc.Update(f.ctx, update); err != nil {
		t.Fatalf("re-enable channel: %v", err)
	}
	if status, _ := f.offeringState(routeID, modelID, "openai"); status != "enabled" {
		t.Fatalf("offering status after channel re-enable = %s, want enabled", status)
	}
}

// TestModelPausePreservesIntent：Model 全局暂停经确认后只修改 Model；
// 重新启用后既有 enabled Binding 与 Offering 重新生效。
func TestModelPausePreservesIntent(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-mdl"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-mdl-a"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-mdl-model"), "enabled")
	f.binding(chA, modelID, "enabled")
	routeID := f.route(uniqueName("sup-mdl-route"), chA)
	f.offering(routeID, modelID, "openai")

	svc := newModelService(f)
	update := adminmodel.UpdateInput{
		ID: modelID, DisplayName: "m", OwnedBy: "test", Status: adminmodel.StatusDisabled,
	}
	_, err := svc.Update(f.ctx, update)
	confirm := confirmationFrom(t, err)
	if confirm.Code != "model_disable_confirmation_required" {
		t.Fatalf("confirmation code = %s", confirm.Code)
	}
	if confirm.Impact.EnabledBindings != 1 {
		t.Fatalf("enabled bindings = %d, want 1", confirm.Impact.EnabledBindings)
	}

	update.Confirmation = supply.Confirmation{Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint()}
	if _, err := svc.Update(f.ctx, update); err != nil {
		t.Fatalf("confirmed model disable: %v", err)
	}
	if got := f.bindingStatus(chA, modelID); got != "enabled" {
		t.Fatalf("binding status = %s, want enabled", got)
	}
	status, reason := f.offeringState(routeID, modelID, "openai")
	if status != "enabled" || reason != nil {
		t.Fatalf("offering = %s/%v, want enabled/nil", status, reason)
	}

	// 重新启用 Model：保留的 Binding 与 Offering 配置意图重新生效。
	update.Status = adminmodel.StatusEnabled
	update.Confirmation = supply.Confirmation{}
	if _, err := svc.Update(f.ctx, update); err != nil {
		t.Fatalf("re-enable model: %v", err)
	}
	if got := f.bindingStatus(chA, modelID); got != "enabled" {
		t.Fatalf("binding status after model re-enable = %s, want enabled", got)
	}
	if status, _ := f.offeringState(routeID, modelID, "openai"); status != "enabled" {
		t.Fatalf("offering status after model re-enable = %s, want enabled", status)
	}
}

// TestModelDelistChangesOnlySelectedOfferings 验证全局下架是显式联合操作：Model 暂停，
// 只停止管理员选择的 Offering，Binding 与未选择 Offering 保持原配置；非法选择整笔回滚。
func TestModelDelistChangesOnlySelectedOfferings(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-delist"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-delist-a"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-delist-model"), "enabled")
	f.binding(chA, modelID, "enabled")
	routeA := f.route(uniqueName("sup-delist-r1"), chA)
	routeB := f.route(uniqueName("sup-delist-r2"), chA)
	f.offering(routeA, modelID, "openai")
	f.offering(routeB, modelID, "openai")

	svc := newModelService(f)
	_, err := svc.Delist(f.ctx, modelID, supply.Confirmation{})
	confirm := confirmationFrom(t, err)
	if confirm.Code != "model_delist_confirmation_required" {
		t.Fatalf("confirmation code = %s", confirm.Code)
	}
	if len(confirm.Impact.AffectedOfferings) != 2 {
		t.Fatalf("affected offerings = %d, want 2", len(confirm.Impact.AffectedOfferings))
	}

	// 选择不在影响范围内的 Offering 必须拒绝，且 Model/Offering 不发生部分提交。
	_, err = svc.Delist(f.ctx, modelID, supply.Confirmation{
		Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint(),
		SelectedOfferings: []supply.OfferingSelection{{
			RouteID: routeA + routeB + 1000, ModelID: modelID, IngressProtocol: "openai",
		}},
	})
	confirmationFrom(t, err)
	if got := f.modelStatus(modelID); got != "enabled" {
		t.Fatalf("model status after rejected delist = %s, want enabled", got)
	}
	if status, _ := f.offeringState(routeA, modelID, "openai"); status != "enabled" {
		t.Fatalf("route A offering after rejected delist = %s, want enabled", status)
	}
	if status, _ := f.offeringState(routeB, modelID, "openai"); status != "enabled" {
		t.Fatalf("route B offering after rejected delist = %s, want enabled", status)
	}

	disabled, err := svc.Delist(f.ctx, modelID, supply.Confirmation{
		Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint(),
		SelectedOfferings: []supply.OfferingSelection{{
			RouteID: routeA, ModelID: modelID, IngressProtocol: "openai",
		}},
	})
	if err != nil {
		t.Fatalf("delist selected offering: %v", err)
	}
	if disabled != 1 {
		t.Fatalf("disabled offerings = %d, want 1", disabled)
	}
	if got := f.modelStatus(modelID); got != "disabled" {
		t.Fatalf("model status = %s, want disabled", got)
	}
	if got := f.bindingStatus(chA, modelID); got != "enabled" {
		t.Fatalf("binding status = %s, want enabled", got)
	}
	status, reason := f.offeringState(routeA, modelID, "openai")
	if status != "disabled" || reason == nil || *reason != supply.ReasonModelDelisted {
		t.Fatalf("route A offering = %s/%v, want disabled/model_delisted", status, reason)
	}
	status, reason = f.offeringState(routeB, modelID, "openai")
	if status != "enabled" || reason != nil {
		t.Fatalf("route B offering = %s/%v, want enabled/nil", status, reason)
	}
}

// TestBindingEnableUnderPausedModel：Model 暂停时仍允许保存 enabled Binding 配置意图。
func TestBindingEnableUnderPausedModel(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-guard"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-guard-a"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-guard-model"), "disabled")
	f.binding(chA, modelID, "disabled")
	itemID := f.verificationEvidence(chA, modelID)

	_, err := newChannelModelService(f).Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusEnabled,
		VerificationItemID: &itemID,
	})
	if err != nil {
		t.Fatalf("enable binding under paused model: %v", err)
	}
	if got := f.bindingStatus(chA, modelID); got != "enabled" {
		t.Fatalf("binding status = %s, want enabled", got)
	}
}

// TestRouteSaveDiffUpdate：Route 保存按组合差异更新——取消勾选记 manual_unselected、
// 无关字段保存保留 disabled 关系、重新勾选恢复 enabled、原子换渠道不误停，
// 保持勾选失去支撑时仍保留 Route 售卖意图。
func TestRouteSaveDiffUpdate(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-rsv"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-rsv-a"), "openai", "enabled")
	chB := f.channel(providerID, uniqueName("sup-rsv-b"), "openai", "enabled")
	modelX := f.model(uniqueName("sup-rsv-x"), "enabled")
	modelY := f.model(uniqueName("sup-rsv-y"), "enabled")
	f.binding(chA, modelX, "enabled")
	f.binding(chB, modelX, "enabled")
	f.binding(chA, modelY, "enabled")

	svc := newRouteService(f)
	routeName := uniqueName("sup-rsv-route")
	created, err := svc.Create(f.ctx, adminroute.CreateInput{
		Name: routeName, Mode: adminroute.ModeBalanced, Status: adminroute.StatusEnabled,
		ChannelIDs: []int64{chA},
		Offerings: []adminroute.OfferingSelection{
			{ModelID: modelX, IngressProtocol: "openai"},
			{ModelID: modelY, IngressProtocol: "openai"},
		},
	})
	if err != nil {
		t.Fatalf("create route: %v", err)
	}
	f.routeIDs = append(f.routeIDs, created.ID)

	baseUpdate := adminroute.UpdateInput{
		ID: created.ID, Name: routeName, Mode: adminroute.ModeBalanced, Status: adminroute.StatusEnabled,
		ChannelIDs: []int64{chA},
	}

	// 取消勾选 Y → manual_unselected（显式动作，无需确认）。
	update := baseUpdate
	update.Offerings = []adminroute.OfferingSelection{{ModelID: modelX, IngressProtocol: "openai"}}
	if _, err := svc.Update(f.ctx, update); err != nil {
		t.Fatalf("unselect offering: %v", err)
	}
	status, reason := f.offeringState(created.ID, modelY, "openai")
	if status != "disabled" || reason == nil || *reason != supply.ReasonManualUnselected {
		t.Fatalf("offering Y = %s/%v, want disabled/manual_unselected", status, reason)
	}

	// 无关字段保存：disabled Offering 保留、不恢复、不失败。
	update.Description = ptr("just a description change")
	if _, err := svc.Update(f.ctx, update); err != nil {
		t.Fatalf("save unrelated fields: %v", err)
	}
	if status, _ := f.offeringState(created.ID, modelY, "openai"); status != "disabled" {
		t.Fatalf("offering Y after unrelated save = %s, want disabled", status)
	}

	// 原子换渠道（A→B，B 支撑 X）：保持勾选的 X 按最终池评估，不被误停。
	update.ChannelIDs = []int64{chB}
	if _, err := svc.Update(f.ctx, update); err != nil {
		t.Fatalf("swap channel: %v", err)
	}
	if status, _ := f.offeringState(created.ID, modelX, "openai"); status != "enabled" {
		t.Fatalf("offering X after swap = %s, want enabled", status)
	}

	// 换回含 Y 支撑的池后重新勾选 Y → 恢复 enabled 并清空原因。
	update.ChannelIDs = []int64{chA}
	update.Offerings = []adminroute.OfferingSelection{
		{ModelID: modelX, IngressProtocol: "openai"},
		{ModelID: modelY, IngressProtocol: "openai"},
	}
	if _, err := svc.Update(f.ctx, update); err != nil {
		t.Fatalf("re-select offering: %v", err)
	}
	status, reason = f.offeringState(created.ID, modelY, "openai")
	if status != "enabled" || reason != nil {
		t.Fatalf("offering Y after re-select = %s/%v, want enabled/nil", status, reason)
	}

	// 移除 Y 的唯一支撑（池只剩 B）但保持勾选：Offering 仍保持 enabled。
	update.ChannelIDs = []int64{chB}
	if _, err := svc.Update(f.ctx, update); err != nil {
		t.Fatalf("save route without configured support: %v", err)
	}
	status, reason = f.offeringState(created.ID, modelY, "openai")
	if status != "enabled" || reason != nil {
		t.Fatalf("offering Y = %s/%v, want enabled/nil", status, reason)
	}
}

// TestRouteCreateAllowsUnsupportedSelection：创建仍必须至少勾选一个组合，但允许保存
// 暂无配置支撑的 Offering 意图。
func TestRouteCreateAllowsUnsupportedSelection(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-rcr"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-rcr-a"), "openai", "enabled")
	modelX := f.model(uniqueName("sup-rcr-x"), "enabled")
	// 绑定停用：无结构支撑。
	f.binding(chA, modelX, "disabled")

	svc := newRouteService(f)
	if _, err := svc.Create(f.ctx, adminroute.CreateInput{
		Name: uniqueName("sup-rcr-route"), Mode: adminroute.ModeBalanced, Status: adminroute.StatusEnabled,
		ChannelIDs: []int64{chA},
	}); failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected invalid argument for empty selection, got %v", err)
	}

	created, err := svc.Create(f.ctx, adminroute.CreateInput{
		Name: uniqueName("sup-rcr-route"), Mode: adminroute.ModeBalanced, Status: adminroute.StatusEnabled,
		ChannelIDs: []int64{chA},
		Offerings:  []adminroute.OfferingSelection{{ModelID: modelX, IngressProtocol: "openai"}},
	})
	if err != nil {
		t.Fatalf("create route with unsupported offering intent: %v", err)
	}
	f.routeIDs = append(f.routeIDs, created.ID)
}

// TestOfferingRestoreBatch：批量恢复需指纹确认并整批原子提交；无支撑或 Model 暂停
// 是恢复警告，不阻止保存 Offering 意图。
func TestOfferingRestoreBatch(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-rst"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-rst-a"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-rst-model"), "enabled")
	f.binding(chA, modelID, "enabled")
	routeID := f.route(uniqueName("sup-rst-route"), chA)
	f.offering(routeID, modelID, "openai")
	// 制造 disabled Offering（manual_unselected 同样可经批量恢复重新售卖）。
	if _, err := f.queries.DisableRouteModelOffering(f.ctx, sqlc.DisableRouteModelOfferingParams{
		Reason:  textParam(supply.ReasonManualUnselected),
		RouteID: routeID, ModelID: modelID, IngressProtocol: "openai",
	}); err != nil {
		t.Fatalf("disable offering: %v", err)
	}

	svc := newModelService(f)
	items := []adminmodel.OfferingRestoreItem{{RouteID: routeID, IngressProtocol: "openai"}}

	_, err := svc.RestoreOfferings(f.ctx, modelID, items, supply.Confirmation{})
	confirm := confirmationFrom(t, err)
	if confirm.Code != "offering_restore_confirmation_required" {
		t.Fatalf("confirmation code = %s", confirm.Code)
	}

	restored, err := svc.RestoreOfferings(f.ctx, modelID, items, supply.Confirmation{
		Confirm: true, ExpectedFingerprint: confirm.Impact.Fingerprint(),
	})
	if err != nil {
		t.Fatalf("confirmed restore: %v", err)
	}
	if restored != 1 {
		t.Fatalf("restored = %d, want 1", restored)
	}
	status, reason := f.offeringState(routeID, modelID, "openai")
	if status != "enabled" || reason != nil {
		t.Fatalf("offering = %s/%v, want enabled/nil", status, reason)
	}

	// 无支撑条目：停用 Binding 只改 Binding；随后手工停止并恢复 Offering 仍可成功。
	cmSvc := newChannelModelService(f)
	_, err = cmSvc.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusDisabled,
	})
	bindingConfirm := confirmationFrom(t, err)
	if _, err := cmSvc.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusDisabled,
		Confirmation: supply.Confirmation{Confirm: true, ExpectedFingerprint: bindingConfirm.Impact.Fingerprint()},
	}); err != nil {
		t.Fatalf("disable binding: %v", err)
	}
	if _, err := f.queries.DisableRouteModelOffering(f.ctx, sqlc.DisableRouteModelOfferingParams{
		Reason: textParam(supply.ReasonManualUnselected), RouteID: routeID, ModelID: modelID, IngressProtocol: "openai",
	}); err != nil {
		t.Fatalf("disable unsupported offering: %v", err)
	}
	_, err = svc.RestoreOfferings(f.ctx, modelID, items, supply.Confirmation{})
	restoreWarning := confirmationFrom(t, err)
	if _, err := svc.RestoreOfferings(f.ctx, modelID, items, supply.Confirmation{
		Confirm: true, ExpectedFingerprint: restoreWarning.Impact.Fingerprint(),
	}); err != nil {
		t.Fatalf("restore offering without configured support: %v", err)
	}
}

// TestFingerprintDriftRequiresReconfirmation：确认前影响范围变化时旧指纹失效并返回新预览。
func TestFingerprintDriftRequiresReconfirmation(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-fpd"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-fpd-a"), "openai", "enabled")
	chOther := f.channel(providerID, uniqueName("sup-fpd-o"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-fpd-model"), "enabled")
	f.binding(chA, modelID, "enabled")
	f.binding(chOther, modelID, "enabled")
	route1 := f.route(uniqueName("sup-fpd-r1"), chA)
	f.offering(route1, modelID, "openai")

	svc := newChannelModelService(f)
	_, err := svc.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusDisabled,
	})
	staleConfirm := confirmationFrom(t, err)

	// 影响范围变化：另一条 Route 也开始依赖 chA 售卖该模型。
	route2 := f.route(uniqueName("sup-fpd-r2"), chA)
	f.offering(route2, modelID, "openai")

	_, err = svc.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusDisabled,
		Confirmation: supply.Confirmation{Confirm: true, ExpectedFingerprint: staleConfirm.Impact.Fingerprint()},
	})
	fresh := confirmationFrom(t, err)
	if len(fresh.Impact.AffectedOfferings) != 2 {
		t.Fatalf("fresh impact offerings = %d, want 2", len(fresh.Impact.AffectedOfferings))
	}
	if fresh.Impact.Fingerprint() == staleConfirm.Impact.Fingerprint() {
		t.Fatal("fingerprint must change when the impact set changes")
	}

	// 携带最新指纹成功执行。
	if _, err := svc.Update(f.ctx, channelmodel.UpdateInput{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: channelmodel.StatusDisabled,
		Confirmation: supply.Confirmation{Confirm: true, ExpectedFingerprint: fresh.Impact.Fingerprint()},
	}); err != nil {
		t.Fatalf("confirmed disable with fresh fingerprint: %v", err)
	}
}

// TestModelLockSerializesConcurrentWriters：Model 行锁串行化收缩与扩张事务——
// T1（binding 停用路径）锁内计算时，T2（Route 保存启用 Offering 路径）必须等待 T1 提交，
// 且在 T1 提交后按新事实校验失败，不产生失去结构支撑的 enabled Offering。
func TestModelLockSerializesConcurrentWriters(t *testing.T) {
	f := newFixture(t)
	providerID := f.provider(uniqueName("sup-lock"), "enabled")
	chA := f.channel(providerID, uniqueName("sup-lock-a"), "openai", "enabled")
	modelID := f.model(uniqueName("sup-lock-model"), "enabled")
	f.binding(chA, modelID, "enabled")
	routeID := f.route(uniqueName("sup-lock-route"), chA)

	// T1：模拟收缩事务——锁 Model 后暂停，再停用 Binding 并提交。
	tx1, err := f.pool.Begin(f.ctx)
	if err != nil {
		t.Fatalf("begin tx1: %v", err)
	}
	q1 := f.queries.WithTx(tx1)
	if err := supply.LockModels(f.ctx, q1, []int64{modelID}); err != nil {
		t.Fatalf("tx1 lock: %v", err)
	}

	// T2：模拟扩张事务（Route 保存启用 Offering），锁请求应阻塞到 T1 提交。
	var wg sync.WaitGroup
	wg.Add(1)
	t2Blocked := make(chan struct{})
	var t2Supported bool
	var t2Err error
	go func() {
		defer wg.Done()
		tx2, err := f.pool.Begin(f.ctx)
		if err != nil {
			t2Err = err
			return
		}
		defer func() { _ = tx2.Rollback(f.ctx) }()
		q2 := f.queries.WithTx(tx2)
		close(t2Blocked)
		if err := supply.LockModels(f.ctx, q2, []int64{modelID}); err != nil {
			t2Err = err
			return
		}
		// 取得锁后按最新事实校验支撑（T1 已停用 Binding → 无支撑，保存必须拒绝）。
		t2Supported, t2Err = q2.OfferingComboSupportedByPool(f.ctx, sqlc.OfferingComboSupportedByPoolParams{
			ChannelIds: []int64{chA}, IngressProtocol: "openai", ModelID: modelID,
		})
	}()

	<-t2Blocked
	time.Sleep(200 * time.Millisecond) // 让 T2 进入锁等待。
	if _, err := q1.UpdateChannelModel(f.ctx, sqlc.UpdateChannelModelParams{
		ChannelID: chA, ModelID: modelID, UpstreamModel: "upstream-model", Status: "disabled",
	}); err != nil {
		_ = tx1.Rollback(f.ctx)
		t.Fatalf("tx1 disable binding: %v", err)
	}
	if err := tx1.Commit(f.ctx); err != nil {
		t.Fatalf("tx1 commit: %v", err)
	}
	wg.Wait()
	if t2Err != nil {
		t.Fatalf("tx2: %v", t2Err)
	}
	if t2Supported {
		t.Fatal("tx2 must observe the committed binding disable after acquiring the model lock")
	}
	_ = routeID
}

func ptr[T any](v T) *T { return &v }

func textParam(v string) pgtype.Text { return pgtype.Text{String: v, Valid: true} }
