// Package channelmodelinventory 编排渠道上游模型发现、清单对账和逐模型验证。
package channelmodelinventory

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	corechannel "github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/providerledger"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelcatalog"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/subscription"
)

const (
	DiscoverySourceManual    = "manual"
	DiscoverySourceSetup     = "setup"
	DiscoverySourceScheduled = "scheduled"
	VerificationSourceManual = "manual"
	VerificationSourceSetup  = "setup"
)

// TxBeginner 是清单服务原子写入所需的事务能力。
type TxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ModelLister 使用注册的 (protocol, adapter_key) 能力枚举上游模型。
type ModelLister interface {
	ListChannelModels(ctx context.Context, protocol, adapterKey string, runtime corechannel.Runtime) (adapter.ModelListResult, error)
}

// ModelProber 使用 Gateway 真实 adapter 发最小生成请求。
type ModelProber interface {
	ProbeChannel(ctx context.Context, protocol, adapterKey string, runtime corechannel.Runtime, upstreamModel string) (adapter.ProbeResult, error)
}

// ProbeAccountant 保存探测事实并处理可靠成本。
type ProbeAccountant interface {
	AccountProbe(ctx context.Context, params providerledger.ProbeParams) error
}

// AccountIdentityResolver 为池型渠道的发现/验证解析账号出站身份
// （生产实现 subscription.ProbeIdentityResolver）。
type AccountIdentityResolver interface {
	ResolveProbeIdentity(ctx context.Context, channelID, accountID int64) (subscription.ProbeIdentity, error)
}

// AccountHealthSink 消费验证探测成功后的账号观测（用量快照/LRU），与请求路径同一实现。
type AccountHealthSink interface {
	RecordAccountSuccess(ctx context.Context, accountID int64, usage *adapter.AccountUsageFacts)
}

// Service 是渠道模型清单的应用服务。
type Service struct {
	db            TxBeginner
	queries       *sqlc.Queries
	lister        ModelLister
	prober        ModelProber
	accountant    ProbeAccountant
	settings      *appsettings.SettingsStore
	catalog       CatalogAdopter
	accounts      AccountIdentityResolver
	accountHealth AccountHealthSink
}

// CatalogAdopter 原子完成参考目录采纳与渠道绑定。
type CatalogAdopter interface {
	AdoptAndBind(ctx context.Context, in modelcatalog.AdoptAndBindInput) (modelcatalog.AdoptAndBindResult, error)
}

func NewService(
	db TxBeginner,
	queries *sqlc.Queries,
	lister ModelLister,
	prober ModelProber,
	accountant ProbeAccountant,
	settings *appsettings.SettingsStore,
) *Service {
	if db == nil || queries == nil {
		panic("channelmodelinventory: database is required")
	}
	return &Service{db: db, queries: queries, lister: lister, prober: prober, accountant: accountant, settings: settings}
}

// WithCatalogAdopter 为 Admin 写路径注入参考目录采纳能力；worker 进程无需注入。
func (s *Service) WithCatalogAdopter(adopter CatalogAdopter) *Service {
	s.catalog = adopter
	return s
}

// WithAccountResolver 接入池型渠道的账号身份解析（admin 与 worker 都要注入，
// 否则池型渠道的发现/验证会以空凭据出站而必然 401）。
func (s *Service) WithAccountResolver(resolver AccountIdentityResolver) *Service {
	s.accounts = resolver
	return s
}

// WithAccountHealth 接入账号观测回写（nil 表示验证不回填用量）。
func (s *Service) WithAccountHealth(sink AccountHealthSink) *Service {
	s.accountHealth = sink
	return s
}

// nullableProxyURL 取可空代理列（NULL/disabled → 空串直连）。
func nullableProxyURL(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

// poolRuntimeIdentity 为池型渠道解析账号并装配 Runtime 的账号身份；credential 型直接返回零值。
func (s *Service) poolRuntimeIdentity(ctx context.Context, supplyForm string, channelID int64) (subscription.ProbeIdentity, bool, error) {
	if !corechannel.SupplyForm(supplyForm).IsPool() {
		return subscription.ProbeIdentity{}, false, nil
	}
	if s.accounts == nil {
		return subscription.ProbeIdentity{}, false, failure.New(
			failure.CodeAdminStoreFailed,
			failure.WithMessage("account resolver is unavailable for pool channel"),
		)
	}
	identity, err := s.accounts.ResolveProbeIdentity(ctx, channelID, 0)
	if err != nil {
		return subscription.ProbeIdentity{}, false, err
	}
	return identity, true, nil
}

// Run 是发现或验证任务的公共运行事实。
type Run struct {
	ID                     int64
	ChannelID              int64
	Source                 string
	Status                 string
	ChannelConfigRevision  int64
	ProviderOriginRevision int64
	ProviderStatusRevision int64
	AttemptCount           int32
	TotalCount             int32
	SucceededCount         int32
	FailedCount            int32
	WarningCode            string
	ErrorCode              string
	Message                string
	CreatedAt              time.Time
	StartedAt              *time.Time
	CompletedAt            *time.Time
}

type RunPage struct {
	Items []Run
	Total int64
}

func discoveryRun(row sqlc.ChannelModelDiscoveryRun) Run {
	return Run{
		ID: row.ID, ChannelID: row.ChannelID, Source: row.Source, Status: row.Status,
		ChannelConfigRevision: row.ChannelConfigRevision, ProviderOriginRevision: row.ProviderOriginRevision,
		ProviderStatusRevision: row.ProviderStatusRevision, AttemptCount: row.AttemptCount,
		TotalCount: row.ModelCount, WarningCode: textValue(row.WarningCode), ErrorCode: textValue(row.ErrorCode),
		Message: textValue(row.Message), CreatedAt: row.CreatedAt.Time,
		StartedAt: timeValue(row.StartedAt), CompletedAt: timeValue(row.CompletedAt),
	}
}

func verificationRun(row sqlc.ChannelModelVerificationRun) Run {
	return Run{
		ID: row.ID, ChannelID: row.ChannelID, Source: row.Source, Status: row.Status,
		ChannelConfigRevision: row.ChannelConfigRevision, ProviderOriginRevision: row.ProviderOriginRevision,
		ProviderStatusRevision: row.ProviderStatusRevision, TotalCount: row.TotalCount,
		SucceededCount: row.SucceededCount, FailedCount: row.FailedCount,
		ErrorCode: textValue(row.ErrorCode), Message: textValue(row.Message), CreatedAt: row.CreatedAt.Time,
		StartedAt: timeValue(row.StartedAt), CompletedAt: timeValue(row.CompletedAt),
	}
}

func textValue(value pgtype.Text) string {
	if !value.Valid {
		return ""
	}
	return value.String
}

func textParam(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	return pgtype.Text{String: value, Valid: value != ""}
}

func timeValue(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}
	t := value.Time
	return &t
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

func storeFailed(err error, operation string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(operation))
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
