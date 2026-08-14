// Package route 编排 admin 管理端的线路（routes / 渠道商品）读写（阶段 15）。
//
// 线路决定「显式渠道池 + 调度策略」：balanced 在线路池内均衡调度，fixed 锁定恰好一条渠道。
// 渠道数量约束在 service 层强校验，给出可读错误。
package route

import (
	"context"
	"errors"
	"math/big"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
)

const (
	// ModeBalanced 在线路渠道池内按容量和健康度负载均衡。
	ModeBalanced = "balanced"
	// ModeFixed 锁定单条渠道。
	ModeFixed = "fixed"

	// ProtocolOpenAI / ProtocolAnthropic 是 Offering 的 ingress protocol 取值，
	// 与 route_model_offerings.ingress_protocol 的 DB 约束一致。
	ProtocolOpenAI    = "openai"
	ProtocolAnthropic = "anthropic"

	// StatusEnabled 线路启用。
	StatusEnabled = "enabled"
	// StatusDisabled 线路停用。
	StatusDisabled = "disabled"
	// StatusArchived 线路已归档（默认隐藏、不可绑定新 key；可恢复）。
	StatusArchived = "archived"
)

// TxBeginner 提供事务能力（由 pgxpool 满足），用于线路 + 渠道池的原子写入。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Service 编排线路读写。
type Service struct {
	db      TxBeginner
	queries *sqlc.Queries
}

// NewService 创建线路管理服务。
func NewService(db TxBeginner, queries *sqlc.Queries) *Service {
	return &Service{db: db, queries: queries}
}

// Route 是 admin 视角的线路事实（含渠道池）。
type Route struct {
	ID     int64
	Name   string
	Mode   string
	Status string
	// PriceRatio 是客户售价倍率（DEC-026：客户售价 = 模型基准价 × 倍率）；十进制字符串承载，避免精度丢失。
	PriceRatio string
	// RPMLimit/RPDLimit/ConcurrencyLimit 是线路级限流上限（按 (线路,用户) 计数）：
	// nil=继承线路默认限流，0=显式不限，>0=具体上限。
	RPMLimit         *int64
	RPDLimit         *int64
	ConcurrencyLimit *int64
	Description      *string
	Channels         []RouteChannel
	Offerings        []RouteOffering
	CreatedAt        time.Time
	UpdatedAt        time.Time
	ArchivedAt       *time.Time
}

// EmptyRouteWarning 是归档导致「候选池空但仍有绑定 key」的断供预警项。
type EmptyRouteWarning struct {
	RouteID  int64
	Name     string
	KeyCount int64
}

// RouteChannel 是线路渠道池成员视图。
type RouteChannel struct {
	ChannelID    int64
	ChannelName  string
	ProviderID   int64
	ProviderSlug string
}

// RouteOffering 是线路的一条 Model+协议售卖组合（ADR-0019 Offering 口径）：
// enabled 表示当前售卖；disabled 保留历史关系、停用原因与时间。
type RouteOffering struct {
	ModelID                int64
	PublicModelID          string
	DisplayName            string
	ModelStatus            string
	IngressProtocol        string
	Status                 string
	DisabledReason         *string
	DisabledAt             *time.Time
	ConfiguredSupportCount int64
	RuntimeCandidateCount  int64
	EffectiveBlockers      []string
	Restorable             bool
	RestoreBlockers        []string
	RestoreWarnings        []string
}

// OfferingSelection 是管理员在 Route 表单勾选的一条售卖组合（Model + ingress protocol）。
type OfferingSelection struct {
	ModelID         int64
	IngressProtocol string
}

// OfferingCandidate 是按渠道池计算出的可勾选售卖组合候选。
type OfferingCandidate struct {
	ModelID            int64
	PublicModelID      string
	DisplayName        string
	ModelStatus        string
	IngressProtocol    string
	SupportingChannels int64
}

// CreateInput 创建线路入参。PriceRatio 为客户售价倍率（十进制字符串，空=默认 1.0）。
// RPM/TPM/RPD/ConcurrencyLimit 为线路级限流上限（nil=继承默认，0=不限，>0=上限）。
// Offerings 是显式勾选的售卖组合（ADR-0019）：创建必须至少勾选一个。
type CreateInput struct {
	Name             string
	Mode             string
	Status           string
	PriceRatio       string
	RPMLimit         *int64
	RPDLimit         *int64
	ConcurrencyLimit *int64
	Description      *string
	ChannelIDs       []int64
	Offerings        []OfferingSelection
	// Confirmation 为兼容旧 Admin 请求保留；Route 保存只按 Offerings 显式选择修改售卖状态。
	Confirmation supply.Confirmation
}

// UpdateInput 更新线路入参（含渠道池整体替换）。PriceRatio 为客户售价倍率（十进制字符串，空=默认 1.0）。
// RPM/TPM/RPD/ConcurrencyLimit 为线路级限流上限（nil=继承默认，0=不限，>0=上限）。
// Offerings 是显式勾选的售卖组合；编辑允许勾选为空（线路暂无售卖由 Offering 状态表达）。
type UpdateInput struct {
	ID               int64
	Name             string
	Mode             string
	Status           string
	PriceRatio       string
	RPMLimit         *int64
	RPDLimit         *int64
	ConcurrencyLimit *int64
	Description      *string
	ChannelIDs       []int64
	Offerings        []OfferingSelection
	// Confirmation 为兼容旧 Admin 请求保留；Route 保存只按 Offerings 显式选择修改售卖状态。
	Confirmation supply.Confirmation
}

// List 列出全部线路及各自渠道池。
func (s *Service) List(ctx context.Context) ([]Route, error) {
	rows, err := s.queries.ListRoutes(ctx)
	if err != nil {
		return nil, storeFailed(err, "list routes")
	}
	out := make([]Route, 0, len(rows))
	for _, row := range rows {
		r := toRoute(row)
		channels, err := s.listChannels(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		r.Channels = channels
		offerings, err := s.listOfferings(ctx, row.ID)
		if err != nil {
			return nil, err
		}
		r.Offerings = offerings
		out = append(out, r)
	}
	return out, nil
}

// Get 读取单条线路（含渠道池）。
func (s *Service) Get(ctx context.Context, id int64) (Route, error) {
	if id <= 0 {
		return Route{}, invalidArgument("id", "id must be positive")
	}
	row, err := s.queries.GetRouteByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Route{}, notFound("route not found")
		}
		return Route{}, storeFailed(err, "get route")
	}
	r := toRoute(row)
	channels, err := s.listChannels(ctx, id)
	if err != nil {
		return Route{}, err
	}
	r.Channels = channels
	offerings, err := s.listOfferings(ctx, id)
	if err != nil {
		return Route{}, err
	}
	r.Offerings = offerings
	return r, nil
}

// OfferingCandidates 按给定渠道池计算当前配置支撑组合。Channel/Provider/Model 当前状态
// 不参与候选集合，ModelStatus 单独返回供管理端展示阻断警告。
func (s *Service) OfferingCandidates(ctx context.Context, channelIDs []int64) ([]OfferingCandidate, error) {
	unique := make(map[int64]struct{}, len(channelIDs))
	ids := make([]int64, 0, len(channelIDs))
	for _, id := range channelIDs {
		if id <= 0 {
			return nil, invalidArgument("channel_ids", "channel id must be positive")
		}
		if _, dup := unique[id]; dup {
			continue
		}
		unique[id] = struct{}{}
		ids = append(ids, id)
	}
	if len(ids) == 0 {
		return []OfferingCandidate{}, nil
	}
	rows, err := s.queries.ListOfferingCandidatesForChannels(ctx, ids)
	if err != nil {
		return nil, storeFailed(err, "list offering candidates")
	}
	out := make([]OfferingCandidate, 0, len(rows))
	for _, row := range rows {
		out = append(out, OfferingCandidate{
			ModelID:            row.ModelID,
			PublicModelID:      row.PublicModelID,
			DisplayName:        row.DisplayName,
			ModelStatus:        row.ModelStatus,
			IngressProtocol:    row.IngressProtocol,
			SupportingChannels: row.SupportingChannels,
		})
	}
	return out, nil
}

// Create 创建自定义线路（事务内建线路 + 渠道池 + 显式售卖组合）。
func (s *Service) Create(ctx context.Context, in CreateInput) (Route, error) {
	name := strings.TrimSpace(in.Name)
	if err := validateRouteShape(name, in.Mode, in.Status, in.ChannelIDs); err != nil {
		return Route{}, err
	}
	// 创建必须至少勾选一个售卖组合；编辑允许为空（ADR-0019）。
	if len(in.Offerings) == 0 {
		return Route{}, invalidArgument("offerings", "route must offer at least one model+protocol combination")
	}
	priceRatio, err := parsePriceRatio(in.PriceRatio)
	if err != nil {
		return Route{}, err
	}
	if err := validateRateLimits(in.RPMLimit, in.RPDLimit, in.ConcurrencyLimit); err != nil {
		return Route{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Route{}, storeFailed(err, "begin create route transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	row, err := q.CreateRoute(ctx, sqlc.CreateRouteParams{
		Name:             name,
		Mode:             in.Mode,
		Status:           in.Status,
		PriceRatio:       priceRatio,
		RpmLimit:         int4Narg(in.RPMLimit),
		RpdLimit:         int4Narg(in.RPDLimit),
		ConcurrencyLimit: int4Narg(in.ConcurrencyLimit),
		Description:      textParam(in.Description),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Route{}, conflict("route name already exists")
		}
		return Route{}, storeFailed(err, "create route")
	}

	if err := addRouteChannels(ctx, q, row.ID, in.ChannelIDs); err != nil {
		return Route{}, err
	}
	if err := applyOfferingSelections(ctx, q, row, in.Offerings, in.ChannelIDs, in.Confirmation); err != nil {
		return Route{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Route{}, storeFailed(err, "commit create route transaction")
	}

	return s.Get(ctx, row.ID)
}

// Update 更新线路（事务内改线路 + 整体替换渠道池 + 按组合差异更新售卖）。
func (s *Service) Update(ctx context.Context, in UpdateInput) (Route, error) {
	if in.ID <= 0 {
		return Route{}, invalidArgument("id", "id must be positive")
	}
	name := strings.TrimSpace(in.Name)
	if err := validateRouteShape(name, in.Mode, in.Status, in.ChannelIDs); err != nil {
		return Route{}, err
	}
	priceRatio, err := parsePriceRatio(in.PriceRatio)
	if err != nil {
		return Route{}, err
	}
	if err := validateRateLimits(in.RPMLimit, in.RPDLimit, in.ConcurrencyLimit); err != nil {
		return Route{}, err
	}

	if _, err := s.queries.GetRouteByID(ctx, in.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Route{}, notFound("route not found")
		}
		return Route{}, storeFailed(err, "load route")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Route{}, storeFailed(err, "begin update route transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	row, err := q.UpdateRoute(ctx, sqlc.UpdateRouteParams{
		ID:               in.ID,
		Name:             name,
		Mode:             in.Mode,
		Status:           in.Status,
		PriceRatio:       priceRatio,
		RpmLimit:         int4Narg(in.RPMLimit),
		RpdLimit:         int4Narg(in.RPDLimit),
		ConcurrencyLimit: int4Narg(in.ConcurrencyLimit),
		Description:      textParam(in.Description),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Route{}, conflict("route name already exists")
		}
		return Route{}, storeFailed(err, "update route")
	}

	if err := q.DeleteRouteChannels(ctx, in.ID); err != nil {
		return Route{}, storeFailed(err, "reset route channels")
	}
	if err := addRouteChannels(ctx, q, in.ID, in.ChannelIDs); err != nil {
		return Route{}, err
	}
	if err := applyOfferingSelections(ctx, q, row, in.Offerings, in.ChannelIDs, in.Confirmation); err != nil {
		return Route{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Route{}, storeFailed(err, "commit update route transaction")
	}

	return s.Get(ctx, in.ID)
}

// SetChannels 整体替换线路渠道池（事务内 delete + insert）。既有 enabled Offering 保持不变；
// 换池只重算配置支撑和运行候选，不自动修改售卖意图。
func (s *Service) SetChannels(ctx context.Context, id int64, channelIDs []int64, confirmation supply.Confirmation) (Route, error) {
	if id <= 0 {
		return Route{}, invalidArgument("id", "id must be positive")
	}

	existing, err := s.queries.GetRouteByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Route{}, notFound("route not found")
		}
		return Route{}, storeFailed(err, "load route")
	}
	if err := validatePoolCount(existing.Mode, channelIDs); err != nil {
		return Route{}, err
	}
	current, err := s.listOfferings(ctx, id)
	if err != nil {
		return Route{}, err
	}
	selections := make([]OfferingSelection, 0, len(current))
	for _, offering := range current {
		if offering.Status == supply.OfferingEnabled {
			selections = append(selections, OfferingSelection{
				ModelID:         offering.ModelID,
				IngressProtocol: offering.IngressProtocol,
			})
		}
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Route{}, storeFailed(err, "begin set route channels transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	if err := q.DeleteRouteChannels(ctx, id); err != nil {
		return Route{}, storeFailed(err, "reset route channels")
	}
	if err := addRouteChannels(ctx, q, id, channelIDs); err != nil {
		return Route{}, err
	}
	if err := applyOfferingSelections(ctx, q, existing, selections, channelIDs, confirmation); err != nil {
		return Route{}, err
	}

	if err := tx.Commit(ctx); err != nil {
		return Route{}, storeFailed(err, "commit set route channels transaction")
	}

	return s.Get(ctx, id)
}

// Delete 删除线路；被 api_keys/users 引用时返回 conflict。
func (s *Service) Delete(ctx context.Context, id int64) error {
	if id <= 0 {
		return invalidArgument("id", "id must be positive")
	}

	// 硬删闸门（D-4）：只允许删除已归档线路。
	cur, err := s.queries.GetRouteByID(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return notFound("route not found")
		}
		return storeFailed(err, "get route")
	}
	if cur.Status != StatusArchived {
		return conflict("route must be archived before deletion")
	}

	rows, err := s.queries.DeleteRoute(ctx, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			return conflict("route is still referenced by api keys or users")
		}
		return storeFailed(err, "delete route")
	}
	if rows == 0 {
		return notFound("route not found")
	}

	return nil
}

// Archive 归档线路。护栏：线路仍绑定 api_key 时必须先迁移——migrateKeysTo 为 nil 则拦截（返回
// conflict），非 nil 则单事务内先把全部 key 迁到目标线路再归档（§4B 入口②）。无绑定 key 直接归档。
// 返回归档后「候选池空但仍有 key」的断供预警。
func (s *Service) Archive(ctx context.Context, id int64, migrateKeysTo *int64) ([]EmptyRouteWarning, error) {
	if id <= 0 {
		return nil, invalidArgument("id", "id must be positive")
	}

	keyCount, err := s.queries.CountApiKeysByRoute(ctx, id)
	if err != nil {
		return nil, storeFailed(err, "count route api keys")
	}

	var affected int64
	if keyCount > 0 {
		if migrateKeysTo == nil {
			return nil, conflict("route has bound api keys; migrate them to another route before archiving")
		}
		if err := s.ensureMigrationTarget(ctx, id, *migrateKeysTo); err != nil {
			return nil, err
		}
		affected, err = s.queries.ArchiveRouteWithKeyMigration(ctx, sqlc.ArchiveRouteWithKeyMigrationParams{
			ID:            id,
			TargetRouteID: *migrateKeysTo,
		})
		if err != nil {
			return nil, storeFailed(err, "archive route with key migration")
		}
	} else {
		affected, err = s.queries.ArchiveRoute(ctx, id)
		if err != nil {
			return nil, storeFailed(err, "archive route")
		}
	}
	if affected == 0 {
		return nil, notFound("route not found or already archived")
	}

	return s.emptyRouteWarnings(ctx)
}

// Restore 取消归档线路：archived → disabled。route_channels 原样保留；归档前已无 key，恢复后需手动绑定。
func (s *Service) Restore(ctx context.Context, id int64) error {
	if id <= 0 {
		return invalidArgument("id", "id must be positive")
	}
	affected, err := s.queries.RestoreRoute(ctx, id)
	if err != nil {
		return storeFailed(err, "restore route")
	}
	if affected == 0 {
		return notFound("route not found or not archived")
	}
	return nil
}

// ensureMigrationTarget 校验迁移目标线路：非自身、存在、且为 enabled（不能迁到停用/归档线路）。
func (s *Service) ensureMigrationTarget(ctx context.Context, sourceID, targetID int64) error {
	if targetID <= 0 {
		return invalidArgument("target_route_id", "target route id must be positive")
	}
	if targetID == sourceID {
		return invalidArgument("target_route_id", "target route must differ from source")
	}
	target, err := s.queries.GetRouteByID(ctx, targetID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return invalidArgument("target_route_id", "target route not found")
		}
		return storeFailed(err, "get target route")
	}
	if target.Status != StatusEnabled {
		return invalidArgument("target_route_id", "target route must be enabled")
	}
	return nil
}

// emptyRouteWarnings 列出「候选池空但仍有绑定 key」的非归档线路，作为归档后的断供预警。
func (s *Service) emptyRouteWarnings(ctx context.Context) ([]EmptyRouteWarning, error) {
	rows, err := s.queries.ListEmptyRoutesWithKeys(ctx)
	if err != nil {
		return nil, storeFailed(err, "list empty routes with keys")
	}
	out := make([]EmptyRouteWarning, 0, len(rows))
	for _, r := range rows {
		out = append(out, EmptyRouteWarning{RouteID: r.ID, Name: r.Name, KeyCount: r.KeyCount})
	}
	return out, nil
}

func (s *Service) listChannels(ctx context.Context, routeID int64) ([]RouteChannel, error) {
	rows, err := s.queries.ListRouteChannelsDetailed(ctx, routeID)
	if err != nil {
		return nil, storeFailed(err, "list route channels")
	}
	out := make([]RouteChannel, 0, len(rows))
	for _, row := range rows {
		out = append(out, RouteChannel{
			ChannelID:    row.ChannelID,
			ChannelName:  row.ChannelName,
			ProviderID:   row.ProviderID,
			ProviderSlug: row.ProviderSlug,
		})
	}
	return out, nil
}

func (s *Service) listOfferings(ctx context.Context, routeID int64) ([]RouteOffering, error) {
	rows, err := s.queries.ListRouteOfferingDetails(ctx, routeID)
	if err != nil {
		return nil, storeFailed(err, "list route offerings")
	}
	out := make([]RouteOffering, 0, len(rows))
	for _, row := range rows {
		offering := RouteOffering{
			ModelID:                row.ModelID,
			PublicModelID:          row.PublicModelID,
			DisplayName:            row.DisplayName,
			ModelStatus:            row.ModelStatus,
			IngressProtocol:        row.IngressProtocol,
			Status:                 row.Status,
			ConfiguredSupportCount: row.ConfiguredSupportCount,
			RuntimeCandidateCount:  row.RuntimeCandidateCount,
			Restorable:             row.RouteStatus != StatusArchived,
		}
		if row.RouteStatus == StatusArchived {
			offering.EffectiveBlockers = append(offering.EffectiveBlockers, "route_archived")
			offering.RestoreBlockers = append(offering.RestoreBlockers, "route_archived")
		} else if row.RouteStatus != StatusEnabled {
			offering.EffectiveBlockers = append(offering.EffectiveBlockers, "route_disabled")
		}
		if row.ModelStatus != StatusEnabled {
			offering.EffectiveBlockers = append(offering.EffectiveBlockers, "model_disabled")
			offering.RestoreWarnings = append(offering.RestoreWarnings, "model_disabled")
		}
		if row.Status != supply.OfferingEnabled {
			offering.EffectiveBlockers = append(offering.EffectiveBlockers, "offering_disabled")
		}
		if row.ConfiguredSupportCount == 0 {
			offering.EffectiveBlockers = append(offering.EffectiveBlockers, "no_configured_support")
			offering.RestoreWarnings = append(offering.RestoreWarnings, "no_configured_support")
		}
		if row.RuntimeCandidateCount == 0 {
			offering.EffectiveBlockers = append(offering.EffectiveBlockers, "no_runtime_candidate")
			offering.RestoreWarnings = append(offering.RestoreWarnings, "no_runtime_candidate")
		}
		if row.DisabledReason.Valid {
			reason := row.DisabledReason.String
			offering.DisabledReason = &reason
		}
		if row.DisabledAt.Valid {
			at := row.DisabledAt.Time
			offering.DisabledAt = &at
		}
		out = append(out, offering)
	}
	return out, nil
}

func addRouteChannels(ctx context.Context, q *sqlc.Queries, routeID int64, channelIDs []int64) error {
	seen := make(map[int64]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID <= 0 {
			return invalidArgument("channel_ids", "channel id must be positive")
		}
		if _, dup := seen[channelID]; dup {
			continue
		}
		seen[channelID] = struct{}{}
		if err := q.AddRouteChannel(ctx, sqlc.AddRouteChannelParams{RouteID: routeID, ChannelID: channelID}); err != nil {
			if isForeignKeyViolation(err) {
				return invalidArgument("channel_ids", "channel does not exist")
			}
			return storeFailed(err, "add route channel")
		}
	}
	return nil
}

// comboKey 是 Offering 差异计算的 Model+协议组合键。
type comboKey struct {
	modelID  int64
	protocol string
}

// applyOfferingSelections 在保存事务内按 Model+协议组合差异更新 Offering（ADR-0019）：
//
//   - 管理员勾选直接表达 Route 售卖意图；Model 暂停、零配置支撑或零运行候选只产生警告，
//     不阻止保存也不自动停止 Offering；
//   - 未勾选的 enabled 组合是显式取消，置 disabled(manual_unselected)，不需要确认；
//   - 其余 disabled 组合保持不动（不删除、不恢复、不导致保存失败）。
func applyOfferingSelections(
	ctx context.Context,
	q *sqlc.Queries,
	routeRow sqlc.Route,
	selections []OfferingSelection,
	_ []int64,
	_ supply.Confirmation,
) error {
	selected := make(map[comboKey]struct{}, len(selections))
	orderedKeys := make([]comboKey, 0, len(selections))
	modelIDs := make([]int64, 0, len(selections))
	for _, sel := range selections {
		if sel.ModelID <= 0 {
			return invalidArgument("offerings", "model id must be positive")
		}
		if sel.IngressProtocol != ProtocolOpenAI && sel.IngressProtocol != ProtocolAnthropic {
			return invalidArgument("offerings", "ingress_protocol must be openai or anthropic")
		}
		key := comboKey{modelID: sel.ModelID, protocol: sel.IngressProtocol}
		if _, dup := selected[key]; dup {
			continue
		}
		selected[key] = struct{}{}
		orderedKeys = append(orderedKeys, key)
		modelIDs = append(modelIDs, sel.ModelID)
	}

	// 锁定本次勾选与既有 Offering 涉及的全部 Model，
	// 锁内重读既有 Offering 作为差异计算的权威事实，不复用锁外读数。
	existingPre, err := q.ListRouteOfferingDetails(ctx, routeRow.ID)
	if err != nil {
		return storeFailed(err, "list route offerings for lock set")
	}
	for _, row := range existingPre {
		modelIDs = append(modelIDs, row.ModelID)
	}
	if err := supply.LockModels(ctx, q, modelIDs); err != nil {
		return storeFailed(err, "lock models for route save")
	}
	existing, err := q.ListRouteOfferingDetails(ctx, routeRow.ID)
	if err != nil {
		return storeFailed(err, "list route offerings in lock")
	}
	existingByKey := make(map[comboKey]sqlc.ListRouteOfferingDetailsRow, len(existing))
	for _, row := range existing {
		existingByKey[comboKey{modelID: row.ModelID, protocol: row.IngressProtocol}] = row
	}

	toEnable := make([]comboKey, 0)
	for _, key := range orderedKeys {
		row, exists := existingByKey[key]
		if exists && row.Status == supply.OfferingEnabled {
			continue
		}
		if _, err := q.LookupModelByID(ctx, key.modelID); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return invalidArgument("offerings", "selected model does not exist")
			}
			return storeFailed(err, "load selected offering model")
		}
		toEnable = append(toEnable, key)
	}

	for _, key := range toEnable {
		if err := q.EnableRouteModelOffering(ctx, sqlc.EnableRouteModelOfferingParams{
			RouteID:         routeRow.ID,
			ModelID:         key.modelID,
			IngressProtocol: key.protocol,
		}); err != nil {
			return storeFailed(err, "enable route model offering")
		}
	}

	// 未勾选的 enabled 组合 → 主动取消售卖。
	for _, row := range existing {
		key := comboKey{modelID: row.ModelID, protocol: row.IngressProtocol}
		if _, keep := selected[key]; keep {
			continue
		}
		if row.Status != supply.OfferingEnabled {
			continue
		}
		unselected := []supply.AffectedOffering{{
			RouteID:         routeRow.ID,
			ModelID:         row.ModelID,
			IngressProtocol: row.IngressProtocol,
		}}
		impact := supply.Impact{Kind: "route_manual_unselected", AffectedOfferings: unselected}
		confirmation := supply.Confirmation{SelectedOfferings: []supply.OfferingSelection{{
			RouteID: routeRow.ID, ModelID: row.ModelID, IngressProtocol: row.IngressProtocol,
		}}}
		if err := supply.DisableSelectedOfferings(ctx, q, impact, confirmation, supply.ReasonManualUnselected); err != nil {
			return storeFailed(err, "unselect route model offering")
		}
	}

	return nil
}

func validateRouteShape(name, mode, status string, channelIDs []int64) error {
	if name == "" {
		return invalidArgument("name", "name is required")
	}
	switch mode {
	case ModeBalanced, ModeFixed:
	default:
		return invalidArgument("mode", "mode must be balanced or fixed")
	}
	switch status {
	case StatusEnabled, StatusDisabled:
	default:
		return invalidArgument("status", "status must be enabled or disabled")
	}
	return validatePoolCount(mode, channelIDs)
}

func validatePoolCount(mode string, channelIDs []int64) error {
	unique := make(map[int64]struct{}, len(channelIDs))
	for _, channelID := range channelIDs {
		if channelID <= 0 {
			return invalidArgument("channel_ids", "channel id must be positive")
		}
		unique[channelID] = struct{}{}
	}
	if mode == ModeFixed {
		if len(unique) != 1 {
			return invalidArgument("channel_ids", "fixed route must list exactly one channel")
		}
	} else if len(unique) == 0 {
		return invalidArgument("channel_ids", "balanced route must list at least one channel")
	}
	return nil
}

func toRoute(r sqlc.Route) Route {
	out := Route{
		ID:               r.ID,
		Name:             r.Name,
		Mode:             r.Mode,
		Status:           r.Status,
		PriceRatio:       numericString(r.PriceRatio),
		RPMLimit:         int4ToPtr(r.RpmLimit),
		RPDLimit:         int4ToPtr(r.RpdLimit),
		ConcurrencyLimit: int4ToPtr(r.ConcurrencyLimit),
		CreatedAt:        r.CreatedAt.Time,
		UpdatedAt:        r.UpdatedAt.Time,
	}
	if r.Description.Valid {
		desc := r.Description.String
		out.Description = &desc
	}
	if r.ArchivedAt.Valid {
		t := r.ArchivedAt.Time
		out.ArchivedAt = &t
	}
	return out
}

// validateRateLimits 校验线路级限流四维：nil（继承默认）放行；否则须为 >=0 整数。
func validateRateLimits(rpm, rpd, concurrency *int64) error {
	for _, p := range []struct {
		field string
		val   *int64
	}{
		{"rpm_limit", rpm},
		{"rpd_limit", rpd},
		{"concurrency_limit", concurrency},
	} {
		if p.val != nil && *p.val < 0 {
			return invalidArgument(p.field, "must be zero or a positive integer (0=unlimited, empty=inherit default)")
		}
	}
	return nil
}

// int4Narg 把 *int64 转成可空 pgtype.Int4（nil=NULL 继承线路默认限流；含 0=显式不限）。
func int4Narg(v *int64) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{Valid: false}
	}
	return pgtype.Int4{Int32: int32(*v), Valid: true}
}

// int4ToPtr 把可空 pgtype.Int4 转成 *int64（nil=继承线路默认限流）。
func int4ToPtr(v pgtype.Int4) *int64 {
	if !v.Valid {
		return nil
	}
	out := int64(v.Int32)
	return &out
}

func textParam(s *string) pgtype.Text {
	if s == nil || strings.TrimSpace(*s) == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: strings.TrimSpace(*s), Valid: true}
}

// parsePriceRatio 解析客户售价倍率：空=默认 "1"（1.0×=基准价）；否则非负十进制（0=免费，>1=加价，<1=折扣）。
func parsePriceRatio(raw string) (pgtype.Numeric, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		s = "1"
	}
	r, ok := new(big.Rat).SetString(s)
	if !ok || strings.ContainsAny(s, "eE") || r.Sign() < 0 {
		return pgtype.Numeric{}, invalidArgument("price_ratio", "must be a non-negative decimal multiplier")
	}
	var n pgtype.Numeric
	if err := n.Scan(s); err != nil {
		return pgtype.Numeric{}, invalidArgument("price_ratio", "invalid decimal multiplier")
	}
	return n, nil
}

// numericString 把 NUMERIC 精确格式化为十进制字符串（不用 float）；NULL/NaN/Inf → "1"（默认倍率）。
func numericString(n pgtype.Numeric) string {
	if !n.Valid || n.NaN || n.InfinityModifier != pgtype.Finite {
		return "1"
	}
	if n.Int == nil {
		return "0"
	}
	negative := n.Int.Sign() < 0
	digits := new(big.Int).Abs(n.Int).String()
	exp := int(n.Exp)

	var formatted string
	switch {
	case exp == 0:
		formatted = digits
	case exp > 0:
		formatted = digits + strings.Repeat("0", exp)
	default:
		scale := -exp
		if len(digits) <= scale {
			digits = strings.Repeat("0", scale-len(digits)+1) + digits
		}
		point := len(digits) - scale
		formatted = digits[:point] + "." + digits[point:]
	}
	if negative {
		formatted = "-" + formatted
	}
	return formatted
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func conflict(message string) error {
	return failure.New(failure.CodeAdminConflict, failure.WithMessage(message))
}

func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
