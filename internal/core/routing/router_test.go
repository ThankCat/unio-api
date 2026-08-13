package routing

import (
	"context"
	"errors"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// testPriceRatio 返回测试用的有效线路价格倍率（1.0）；不设倍率会让 ScaleCustomerPrice 因无效倍率报错。
func testPriceRatio() pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(1), Exp: 0, Valid: true}
}

// fakeStore 是 routing 测试使用的候选 channel 存储替身。
type fakeStore struct {
	params            sqlc.FindRouteCandidatesParams
	rows              []sqlc.FindRouteCandidatesRow
	err               error
	modelExistsID     string
	modelExists       bool
	modelExistsErr    error
	routeOffersParams sqlc.RouteOffersModelParams
	routeOffers       bool
	routeOffersErr    error
	userCanUseParams  sqlc.UserCanUseModelParams
	userCanUse        bool
	userCanUseErr     error
	routeMode         string
	routeStatus       string
	routeErr          error
	routeChannelCount int64
}

// FindRouteCandidates 记录查询参数，并返回测试预设候选结果。
func (s *fakeStore) FindRouteCandidates(ctx context.Context, arg sqlc.FindRouteCandidatesParams) ([]sqlc.FindRouteCandidatesRow, error) {
	s.params = arg
	// DEC-027：候选成本需可解析。这些路由用例只验证选路/排序/超时，不关心成本数值，
	// 故默认把未设成本来源的行标记为「绝对成本覆盖」（channel_price_id = channel_id），
	// 让 buildChatRouteCandidate 走覆盖路径拿到零成本快照，避免误判为未定价。
	rows := make([]sqlc.FindRouteCandidatesRow, len(s.rows))
	copy(rows, s.rows)
	for i := range rows {
		if rows[i].ChannelPriceID == 0 && rows[i].ChannelCostMultiplierID == 0 {
			rows[i].ChannelPriceID = rows[i].ChannelID
		}
		if rows[i].BaseCurrency == "" {
			rows[i].BaseCurrency = "USD"
			rows[i].BasePricingUnit = "per_1m_tokens"
			rows[i].UncachedInputPrice = pgtype.Numeric{Int: big.NewInt(0), Valid: true}
			rows[i].OutputPrice = pgtype.Numeric{Int: big.NewInt(0), Valid: true}
		}
		if rows[i].CostCurrency == "" {
			rows[i].CostCurrency = "USD"
			rows[i].CostPricingUnit = "per_1m_tokens"
			rows[i].UncachedInputCost = pgtype.Numeric{Int: big.NewInt(0), Valid: true}
			rows[i].OutputCost = pgtype.Numeric{Int: big.NewInt(0), Valid: true}
		}
	}
	return rows, s.err
}

// ModelExistsByID 记录模型存在性诊断参数，并返回测试预设结果。
func (s *fakeStore) ModelExistsByID(ctx context.Context, requestedModelID string) (bool, error) {
	s.modelExistsID = requestedModelID
	return s.modelExists, s.modelExistsErr
}

func (s *fakeStore) RouteOffersModel(ctx context.Context, arg sqlc.RouteOffersModelParams) (bool, error) {
	s.routeOffersParams = arg
	return s.routeOffers, s.routeOffersErr
}

// UserCanUseModel 记录 user 模型可用性诊断参数，并返回测试预设结果。
func (s *fakeStore) UserCanUseModel(ctx context.Context, arg sqlc.UserCanUseModelParams) (bool, error) {
	s.userCanUseParams = arg
	return s.userCanUse, s.userCanUseErr
}

// GetRouteByID 返回测试线路；调用方传入 RouteID 触发解析（线路必填）。
func (s *fakeStore) GetRouteByID(ctx context.Context, id int64) (sqlc.Route, error) {
	if s.routeErr != nil {
		return sqlc.Route{}, s.routeErr
	}
	mode := s.routeMode
	if mode == "" {
		mode = "balanced"
	}
	status := s.routeStatus
	if status == "" {
		status = "enabled"
	}
	return sqlc.Route{ID: id, Name: "test", Mode: mode, Status: status, PriceRatio: testPriceRatio()}, nil
}

func (s *fakeStore) CountRouteChannels(context.Context, int64) (int64, error) {
	if s.routeChannelCount == 0 {
		return 1, nil
	}
	return s.routeChannelCount, nil
}

func testRouteID() *int64 {
	id := int64(1)
	return &id
}

func TestRouterRejectsCorruptFixedRoutePool(t *testing.T) {
	store := &fakeStore{routeMode: "fixed", routeChannelCount: 2}
	router := NewRouter(store, time.Second)
	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID: 1, ModelID: "openai/gpt", IngressProtocol: ProtocolOpenAI, Endpoint: EndpointChatCompletions, RouteID: testRouteID(),
	})
	if failure.CodeOf(err) != failure.CodeRoutingNoAvailableChannel {
		t.Fatalf("corrupt fixed pool must fail closed, got %v", err)
	}
}

func TestRouterPlanChatReturnsOrderedCandidates(t *testing.T) {
	store := &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				RequestedModelID:        "openai/gpt-4.1",
				ProviderID:              11,
				ProviderOriginRevision:  3,
				ProviderStatusRevision:  4,
				ChannelConfigRevision:   5,
				ChannelCapacityRevision: 6,
				AdapterKey:              "openai",
				ChannelID:               123,
				Origin:                  "https://api.openai.example/v1",
				Credential:              "secret://openai/main",
				ResponseTimeoutMs:       pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel:           "gpt-4.1",
			},
			{
				RequestedModelID:  "openai/gpt-4.1",
				ProviderID:        11,
				AdapterKey:        "openai",
				ChannelID:         456,
				Origin:            "https://backup.openai.example/v1",
				Credential:        "secret://openai/backup",
				ResponseTimeoutMs: pgtype.Int4{Int32: 30000, Valid: true},
				UpstreamModel:     "gpt-4.1",
			},
		},
	}
	router := NewRouter(store, 30*time.Second)

	got, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
		RouteID:         testRouteID(),
	})
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}

	if store.params.UserID != 42 {
		t.Fatalf("expected user id %d, got %d", int64(42), store.params.UserID)
	}
	if store.params.RequestedModelID != "openai/gpt-4.1" {
		t.Fatalf("expected requested model %q, got %q", "openai/gpt-4.1", store.params.RequestedModelID)
	}
	if store.params.IngressProtocol != ProtocolOpenAI {
		t.Fatalf("expected ingress protocol %q, got %q", ProtocolOpenAI, store.params.IngressProtocol)
	}

	if got.RequestedModel != "openai/gpt-4.1" {
		t.Fatalf("expected requested model %q, got %q", "openai/gpt-4.1", got.RequestedModel)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got.Candidates))
	}

	first := got.Candidates[0]
	if first.ProviderID != 11 {
		t.Fatalf("expected provider id %d, got %d", int64(11), first.ProviderID)
	}
	if first.ProviderID != 11 || first.OriginRevision != 3 || first.ProviderStatusRevision != 4 {
		t.Fatalf("origin snapshot was not preserved: %+v", first)
	}
	if first.ChannelConfigRevision != 5 || first.ChannelCapacityRevision != 6 {
		t.Fatalf("channel revisions were not preserved: %+v", first)
	}
	if first.AdapterKey != "openai" {
		t.Fatalf("expected adapter key %q, got %q", "openai", first.AdapterKey)
	}
	if first.UpstreamModel != "gpt-4.1" {
		t.Fatalf("expected upstream model %q, got %q", "gpt-4.1", first.UpstreamModel)
	}
	if first.Channel.ID != 123 {
		t.Fatalf("expected channel id %d, got %d", int64(123), first.Channel.ID)
	}
	if first.Channel.Origin != "https://api.openai.example/v1" {
		t.Fatalf("expected base url %q, got %q", "https://api.openai.example/v1", first.Channel.Origin)
	}
	// 渠道凭据明文存储：候选直接取用 channels.credential 明文，无解密环节。
	if first.Channel.APIKey != "secret://openai/main" {
		t.Fatalf("expected plaintext credential as API key, got %q", first.Channel.APIKey)
	}
	if first.Channel.ResponseTimeout != 15*time.Second {
		t.Fatalf("expected response timeout %v, got %v", 15*time.Second, first.Channel.ResponseTimeout)
	}

	second := got.Candidates[1]
	if second.Channel.ID != 456 {
		t.Fatalf("expected second channel id %d, got %d", int64(456), second.Channel.ID)
	}
	if second.Channel.APIKey != "secret://openai/backup" {
		t.Fatalf("expected second plaintext credential, got %q", second.Channel.APIKey)
	}
	if second.Channel.ResponseTimeout != 30*time.Second {
		t.Fatalf("expected second timeout %v, got %v", 30*time.Second, second.Channel.ResponseTimeout)
	}
}

func TestRouterPlanChatFreezesProviderCostToSaleRatio(t *testing.T) {
	store := &fakeStore{rows: []sqlc.FindRouteCandidatesRow{{
		RequestedModelID: "openai/gpt-4.1",
		ModelDbID:        10,
		AdapterKey:       "openai",
		Protocol:         ProtocolOpenAI,
		ChannelID:        123,
		Origin:           "https://api.openai.example/v1",
		Credential:       "secret://openai/main",
		UpstreamModel:    "gpt-4.1",
		ChannelPriceID:   99,
		BaseCurrency:     "USD",
		BasePricingUnit:  "per_1m_tokens",
		UncachedInputPrice: pgtype.Numeric{
			Int: big.NewInt(10), Valid: true,
		},
		OutputPrice:     pgtype.Numeric{Int: big.NewInt(20), Valid: true},
		CostCurrency:    "USD",
		CostPricingUnit: "per_1m_tokens",
		UncachedInputCost: pgtype.Numeric{
			Int: big.NewInt(2), Valid: true,
		},
		OutputCost: pgtype.Numeric{Int: big.NewInt(8), Valid: true},
	}}}
	router := NewRouter(store, 30*time.Second)

	plan, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID: 42, ModelID: "openai/gpt-4.1", IngressProtocol: ProtocolOpenAI, RouteID: testRouteID(),
	})
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("candidate count = %d, want 1", len(plan.Candidates))
	}
	// max(2/10, 8/20) = 0.4; optional components use the same input/output fallbacks.
	if math.Abs(plan.Candidates[0].CostRatio-0.4) > 1e-12 {
		t.Fatalf("CostRatio = %v, want 0.4", plan.Candidates[0].CostRatio)
	}
}

func TestNewRouterUsesFallbackDefaultTimeout(t *testing.T) {
	store := &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				AdapterKey:        "openai",
				ChannelID:         123,
				Origin:            "https://api.openai.example/v1",
				Credential:        "secret://openai/main",
				ResponseTimeoutMs: pgtype.Int4{Valid: false},
				UpstreamModel:     "gpt-4.1",
			},
		},
	}
	router := NewRouter(store, 0)

	got, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
		RouteID:         testRouteID(),
	})
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}

	if got.Candidates[0].Channel.ResponseTimeout != defaultResponseTimeoutFallback {
		t.Fatalf("expected fallback default timeout %v, got %v", defaultResponseTimeoutFallback, got.Candidates[0].Channel.ResponseTimeout)
	}
}

func TestRouterSetDefaultTimeoutTakesEffect(t *testing.T) {
	newRows := func() []sqlc.FindRouteCandidatesRow {
		return []sqlc.FindRouteCandidatesRow{
			{
				AdapterKey:        "openai",
				ChannelID:         123,
				Origin:            "https://api.openai.example/v1",
				Credential:        "secret://openai/main",
				ResponseTimeoutMs: pgtype.Int4{Valid: false},
				UpstreamModel:     "gpt-4.1",
			},
		}
	}
	store := &fakeStore{rows: newRows()}
	router := NewRouter(store, 30*time.Second)
	req := ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
		RouteID:         testRouteID(),
	}

	// 热改默认超时:之后的候选构造用新值。
	router.SetDefaultResponseTimeout(45 * time.Second)
	got, err := router.PlanChat(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}
	if got.Candidates[0].Channel.ResponseTimeout != 45*time.Second {
		t.Fatalf("expected hot-reloaded timeout 45s, got %v", got.Candidates[0].Channel.ResponseTimeout)
	}

	// <=0 兜底为内置 30s。
	router.SetDefaultResponseTimeout(0)
	got, err = router.PlanChat(context.Background(), req)
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}
	if got.Candidates[0].Channel.ResponseTimeout != defaultResponseTimeoutFallback {
		t.Fatalf("expected fallback timeout %v, got %v", defaultResponseTimeoutFallback, got.Candidates[0].Channel.ResponseTimeout)
	}
}

func TestRouterPlanChatReturnsNoAvailableChannel(t *testing.T) {
	store := &fakeStore{}
	router := NewRouter(store, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
		RouteID:         testRouteID(),
	})
	if !errors.Is(err, ErrNoAvailableChannel) {
		t.Fatalf("expected ErrNoAvailableChannel, got %v", err)
	}
	if store.modelExistsID != "" || store.userCanUseParams.UserID != 0 {
		t.Fatalf("PlanChat must not repeat qualification checks: model=%q user=%#v", store.modelExistsID, store.userCanUseParams)
	}
}

func TestRouterValidateChatReturnsModelNotFound(t *testing.T) {
	store := &fakeStore{modelExists: false}
	router := NewRouter(store, 30*time.Second)

	err := router.ValidateChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/missing",
		IngressProtocol: ProtocolOpenAI,
		RouteID:         testRouteID(),
	})
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("expected ErrModelNotFound, got %v", err)
	}
	if store.userCanUseParams.UserID != 0 {
		t.Fatalf("expected user policy check to be skipped, got %#v", store.userCanUseParams)
	}
}

func TestRouterValidateChatReturnsRouteModelNotAvailable(t *testing.T) {
	store := &fakeStore{
		modelExists: true,
		routeOffers: false,
	}
	router := NewRouter(store, 30*time.Second)

	err := router.ValidateChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
		RouteID:         testRouteID(),
	})
	if !errors.Is(err, ErrModelNotAvailable) {
		t.Fatalf("expected ErrModelNotAvailable, got %v", err)
	}
	if store.routeOffersParams.RouteID != 1 || store.routeOffersParams.IngressProtocol != ProtocolOpenAI {
		t.Fatalf("unexpected offering params: %#v", store.routeOffersParams)
	}
	if store.userCanUseParams.UserID != 0 {
		t.Fatalf("user policy must be skipped for unoffered model: %#v", store.userCanUseParams)
	}
}

func TestRouterValidateChatReturnsUserModelNotAvailable(t *testing.T) {
	store := &fakeStore{modelExists: true, routeOffers: true, userCanUse: false}
	router := NewRouter(store, 30*time.Second)

	err := router.ValidateChat(context.Background(), ChatRouteRequest{
		UserID: 42, ModelID: "openai/gpt-4.1", IngressProtocol: ProtocolOpenAI, RouteID: testRouteID(),
	})
	if !errors.Is(err, ErrModelNotAvailable) {
		t.Fatalf("expected ErrModelNotAvailable, got %v", err)
	}
	if store.userCanUseParams.UserID != 42 || store.userCanUseParams.RequestedModelID != "openai/gpt-4.1" {
		t.Fatalf("unexpected user policy params: %#v", store.userCanUseParams)
	}
}

func TestRouterPlanChatReturnsStoreError(t *testing.T) {
	storeErr := errors.New("database unavailable")
	router := NewRouter(&fakeStore{err: storeErr}, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
		RouteID:         testRouteID(),
	})
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected store error, got %v", err)
	}
}

// TestRouterPlanChatAllCandidatesMissingCredentialReturnsNoAvailable 验证 P1-1：唯一候选缺凭据（明文为空）
// 被跳过后收口为 ErrNoAvailableChannel，不泄露内部错误。
func TestRouterPlanChatAllCandidatesMissingCredentialReturnsNoAvailable(t *testing.T) {
	store := &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				AdapterKey:        "openai",
				ChannelID:         123,
				Origin:            "https://api.openai.example/v1",
				Credential:        "",
				ResponseTimeoutMs: pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel:     "gpt-4.1",
			},
		},
	}
	router := NewRouter(store, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
		RouteID:         testRouteID(),
	})
	if !errors.Is(err, ErrNoAvailableChannel) {
		t.Fatalf("expected ErrNoAvailableChannel, got %v", err)
	}
}

// TestRouterPlanChatSkipsBadCandidateKeepsGood 验证 P1-1：单个坏候选（缺凭据）被跳过，
// 健康候选仍正常进入 plan，请求不被整盘拖垮。
func TestRouterPlanChatSkipsBadCandidateKeepsGood(t *testing.T) {
	store := &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				AdapterKey:        "openai",
				ChannelID:         111,
				Origin:            "https://bad.openai.example/v1",
				Credential:        "", // 坏候选：缺凭据，应被跳过
				ResponseTimeoutMs: pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel:     "gpt-4.1",
			},
			{
				AdapterKey:        "openai",
				ChannelID:         222,
				Origin:            "https://good.openai.example/v1",
				Credential:        "secret://openai/good",
				ResponseTimeoutMs: pgtype.Int4{Int32: 30000, Valid: true},
				UpstreamModel:     "gpt-4.1",
			},
		},
	}
	router := NewRouter(store, 30*time.Second)

	got, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
		RouteID:         testRouteID(),
	})
	if err != nil {
		t.Fatalf("expected good candidate to survive, got error: %v", err)
	}
	if len(got.Candidates) != 1 {
		t.Fatalf("expected 1 surviving candidate, got %d", len(got.Candidates))
	}
	if got.Candidates[0].Channel.ID != 222 {
		t.Fatalf("expected surviving channel 222, got %d", got.Candidates[0].Channel.ID)
	}
}

func TestRouterPlanChatReturnsRouteNotConfigured(t *testing.T) {
	store := &fakeStore{}
	router := NewRouter(store, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
	})
	if !errors.Is(err, ErrRouteNotConfigured) {
		t.Fatalf("expected ErrRouteNotConfigured, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeRoutingRouteNotConfigured {
		t.Fatalf("expected code %q, got %q", failure.CodeRoutingRouteNotConfigured, got)
	}
	if store.params != (sqlc.FindRouteCandidatesParams{}) {
		t.Fatalf("expected store query to be skipped, got %#v", store.params)
	}
}

func TestRouterPlanChatReturnsRouteNotConfiguredForMissingOrDisabledRoute(t *testing.T) {
	tests := []struct {
		name  string
		store *fakeStore
	}{
		{name: "missing", store: &fakeStore{routeErr: pgx.ErrNoRows}},
		{name: "disabled", store: &fakeStore{routeStatus: "disabled"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := NewRouter(tt.store, 30*time.Second)
			_, err := router.PlanChat(context.Background(), ChatRouteRequest{
				UserID: 42, ModelID: "openai/gpt-4.1", IngressProtocol: ProtocolOpenAI, RouteID: testRouteID(),
			})
			if failure.CodeOf(err) != failure.CodeRoutingRouteNotConfigured {
				t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeRoutingRouteNotConfigured)
			}
		})
	}
}

func TestRouterPlanChatReturnsStoreFailureWhenRouteLookupFails(t *testing.T) {
	storeErr := errors.New("database unavailable")
	store := &fakeStore{routeErr: storeErr}
	router := NewRouter(store, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID: 42, ModelID: "openai/gpt-4.1", IngressProtocol: ProtocolOpenAI, RouteID: testRouteID(),
	})
	if failure.CodeOf(err) != failure.CodeRoutingStoreFailed {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeRoutingStoreFailed)
	}
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected store error to be preserved, got %v", err)
	}
}

func TestRouterPlanChatRejectsInvalidIngressProtocolBeforeQuery(t *testing.T) {
	store := &fakeStore{}
	router := NewRouter(store, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: "unknown",
	})
	if !errors.Is(err, ErrIngressProtocolInvalid) {
		t.Fatalf("expected ErrIngressProtocolInvalid, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeRoutingProtocolInvalid {
		t.Fatalf("expected code %q, got %q", failure.CodeRoutingProtocolInvalid, got)
	}
	if store.params != (sqlc.FindRouteCandidatesParams{}) {
		t.Fatalf("expected store query to be skipped, got %#v", store.params)
	}
}
