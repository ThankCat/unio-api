// Package route 编排 admin 管理端的线路（routes / 渠道商品）读写（阶段 15）。
//
// 线路决定「候选池 + 排序策略」：自定义线路可手挑渠道池（explicit），fixed 模式锁定恰好一条渠道。
// 约束在 service 层强校验（DB 仅有 fixed 的弱约束），给出可读错误。
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

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

const (
	// ModeCheapest 按售价升序选路。
	ModeCheapest = "cheapest"
	// ModeStable 按渠道健康选路。
	ModeStable = "stable"
	// ModeFixed 锁定单条渠道。
	ModeFixed = "fixed"

	// PoolAll 动态全量候选池。
	PoolAll = "all"
	// PoolExplicit 运营手挑渠道池。
	PoolExplicit = "explicit"

	// StatusEnabled 线路启用。
	StatusEnabled = "enabled"
	// StatusDisabled 线路停用。
	StatusDisabled = "disabled"
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
	ID       int64
	Name     string
	Mode     string
	PoolKind string
	Status   string
	// PriceRatio 是客户售价倍率（DEC-026：客户售价 = 模型基准价 × 倍率）；十进制字符串承载，避免精度丢失。
	PriceRatio  string
	Description *string
	Channels    []RouteChannel
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// RouteChannel 是线路渠道池成员视图。
type RouteChannel struct {
	ChannelID    int64
	ChannelName  string
	ProviderID   int64
	ProviderSlug string
}

// CreateInput 创建线路入参。PriceRatio 为客户售价倍率（十进制字符串，空=默认 1.0）。
type CreateInput struct {
	Name        string
	Mode        string
	PoolKind    string
	Status      string
	PriceRatio  string
	Description *string
	ChannelIDs  []int64
}

// UpdateInput 更新线路入参（含渠道池整体替换）。PriceRatio 为客户售价倍率（十进制字符串，空=默认 1.0）。
type UpdateInput struct {
	ID          int64
	Name        string
	Mode        string
	PoolKind    string
	Status      string
	PriceRatio  string
	Description *string
	ChannelIDs  []int64
}

// List 列出全部线路，含 explicit 线路的渠道池。
func (s *Service) List(ctx context.Context) ([]Route, error) {
	rows, err := s.queries.ListRoutes(ctx)
	if err != nil {
		return nil, storeFailed(err, "list routes")
	}
	out := make([]Route, 0, len(rows))
	for _, row := range rows {
		r := toRoute(row)
		if row.PoolKind == PoolExplicit {
			channels, err := s.listChannels(ctx, row.ID)
			if err != nil {
				return nil, err
			}
			r.Channels = channels
		}
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
	if row.PoolKind == PoolExplicit {
		channels, err := s.listChannels(ctx, id)
		if err != nil {
			return Route{}, err
		}
		r.Channels = channels
	}
	return r, nil
}

// Create 创建自定义线路（事务内建线路 + 渠道池）。
func (s *Service) Create(ctx context.Context, in CreateInput) (Route, error) {
	name := strings.TrimSpace(in.Name)
	if err := validateRouteShape(name, in.Mode, in.PoolKind, in.Status, in.ChannelIDs); err != nil {
		return Route{}, err
	}
	priceRatio, err := parsePriceRatio(in.PriceRatio)
	if err != nil {
		return Route{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return Route{}, storeFailed(err, "begin create route transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := s.queries.WithTx(tx)

	row, err := q.CreateRoute(ctx, sqlc.CreateRouteParams{
		Name:        name,
		Mode:        in.Mode,
		PoolKind:    in.PoolKind,
		Status:      in.Status,
		PriceRatio:  priceRatio,
		Description: textParam(in.Description),
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

	if err := tx.Commit(ctx); err != nil {
		return Route{}, storeFailed(err, "commit create route transaction")
	}

	return s.Get(ctx, row.ID)
}

// Update 更新线路（事务内改线路 + 整体替换渠道池）。
func (s *Service) Update(ctx context.Context, in UpdateInput) (Route, error) {
	if in.ID <= 0 {
		return Route{}, invalidArgument("id", "id must be positive")
	}
	name := strings.TrimSpace(in.Name)
	if err := validateRouteShape(name, in.Mode, in.PoolKind, in.Status, in.ChannelIDs); err != nil {
		return Route{}, err
	}
	priceRatio, err := parsePriceRatio(in.PriceRatio)
	if err != nil {
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

	if _, err := q.UpdateRoute(ctx, sqlc.UpdateRouteParams{
		ID:          in.ID,
		Name:        name,
		Mode:        in.Mode,
		PoolKind:    in.PoolKind,
		Status:      in.Status,
		PriceRatio:  priceRatio,
		Description: textParam(in.Description),
	}); err != nil {
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

	if err := tx.Commit(ctx); err != nil {
		return Route{}, storeFailed(err, "commit update route transaction")
	}

	return s.Get(ctx, in.ID)
}

// SetChannels 整体替换 explicit 线路的渠道池（事务内 delete + insert）。
func (s *Service) SetChannels(ctx context.Context, id int64, channelIDs []int64) (Route, error) {
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
	if existing.PoolKind != PoolExplicit {
		return Route{}, invalidArgument("pool_kind", "only explicit-pool routes can set channels")
	}
	if err := validatePoolCount(existing.Mode, existing.PoolKind, channelIDs); err != nil {
		return Route{}, err
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

func validateRouteShape(name, mode, poolKind, status string, channelIDs []int64) error {
	if name == "" {
		return invalidArgument("name", "name is required")
	}
	switch mode {
	case ModeCheapest, ModeStable, ModeFixed:
	default:
		return invalidArgument("mode", "mode must be cheapest, stable or fixed")
	}
	switch poolKind {
	case PoolAll, PoolExplicit:
	default:
		return invalidArgument("pool_kind", "pool_kind must be all or explicit")
	}
	switch status {
	case StatusEnabled, StatusDisabled:
	default:
		return invalidArgument("status", "status must be enabled or disabled")
	}
	if mode == ModeFixed && poolKind != PoolExplicit {
		return invalidArgument("pool_kind", "fixed route must use an explicit pool")
	}
	return validatePoolCount(mode, poolKind, channelIDs)
}

func validatePoolCount(mode, poolKind string, channelIDs []int64) error {
	switch poolKind {
	case PoolAll:
		if len(channelIDs) > 0 {
			return invalidArgument("channel_ids", "all-pool route must not list channels")
		}
	case PoolExplicit:
		if mode == ModeFixed {
			if len(channelIDs) != 1 {
				return invalidArgument("channel_ids", "fixed route must list exactly one channel")
			}
		} else if len(channelIDs) == 0 {
			return invalidArgument("channel_ids", "explicit-pool route must list at least one channel")
		}
	}
	return nil
}

func toRoute(r sqlc.Route) Route {
	out := Route{
		ID:         r.ID,
		Name:       r.Name,
		Mode:       r.Mode,
		PoolKind:   r.PoolKind,
		Status:     r.Status,
		PriceRatio: numericString(r.PriceRatio),
		CreatedAt:  r.CreatedAt.Time,
		UpdatedAt:  r.UpdatedAt.Time,
	}
	if r.Description.Valid {
		desc := r.Description.String
		out.Description = &desc
	}
	return out
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
