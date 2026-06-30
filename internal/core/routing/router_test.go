package routing

import (
	"context"
	"errors"
	"math/big"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// testPriceRatio 返回测试用的有效线路价格倍率（1.0）；不设倍率会让 ScaleCustomerPrice 因无效倍率报错。
func testPriceRatio() pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(1), Exp: 0, Valid: true}
}

// fakeStore 是 routing 测试使用的候选 channel 存储替身。
type fakeStore struct {
	params           sqlc.FindRouteCandidatesParams
	rows             []sqlc.FindRouteCandidatesRow
	err              error
	modelExistsID    string
	modelExists      bool
	modelExistsErr   error
	userCanUseParams sqlc.UserCanUseModelParams
	userCanUse       bool
	userCanUseErr    error
}

// FindRouteCandidates 记录查询参数，并返回测试预设候选结果。
func (s *fakeStore) FindRouteCandidates(ctx context.Context, arg sqlc.FindRouteCandidatesParams) ([]sqlc.FindRouteCandidatesRow, error) {
	s.params = arg
	return s.rows, s.err
}

// ModelExistsByID 记录模型存在性诊断参数，并返回测试预设结果。
func (s *fakeStore) ModelExistsByID(ctx context.Context, requestedModelID string) (bool, error) {
	s.modelExistsID = requestedModelID
	return s.modelExists, s.modelExistsErr
}

// UserCanUseModel 记录 user 模型可用性诊断参数，并返回测试预设结果。
func (s *fakeStore) UserCanUseModel(ctx context.Context, arg sqlc.UserCanUseModelParams) (bool, error) {
	s.userCanUseParams = arg
	return s.userCanUse, s.userCanUseErr
}

// GetRouteByID 返回测试线路；调用方传入 RouteID 触发解析（线路必填）。
func (s *fakeStore) GetRouteByID(ctx context.Context, id int64) (sqlc.Route, error) {
	return sqlc.Route{ID: id, Name: "test", Mode: "cheapest", PoolKind: "all", Status: "enabled", PriceRatio: testPriceRatio()}, nil
}

func testRouteID() *int64 {
	id := int64(1)
	return &id
}

func TestRouterPlanChatReturnsOrderedCandidates(t *testing.T) {
	store := &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				RequestedModelID: "openai/gpt-4.1",
				ProviderID:       11,
				AdapterKey:       "openai",
				ChannelID:        123,
				BaseUrl:          "https://api.openai.example/v1",
				Credential:       "secret://openai/main",
				TimeoutMs:        pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel:    "gpt-4.1",
			},
			{
				RequestedModelID: "openai/gpt-4.1",
				ProviderID:       11,
				AdapterKey:       "openai",
				ChannelID:        456,
				BaseUrl:          "https://backup.openai.example/v1",
				Credential:       "secret://openai/backup",
				TimeoutMs:        pgtype.Int4{Int32: 30000, Valid: true},
				UpstreamModel:    "gpt-4.1",
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
	if first.AdapterKey != "openai" {
		t.Fatalf("expected adapter key %q, got %q", "openai", first.AdapterKey)
	}
	if first.UpstreamModel != "gpt-4.1" {
		t.Fatalf("expected upstream model %q, got %q", "gpt-4.1", first.UpstreamModel)
	}
	if first.Channel.ID != 123 {
		t.Fatalf("expected channel id %d, got %d", int64(123), first.Channel.ID)
	}
	if first.Channel.BaseURL != "https://api.openai.example/v1" {
		t.Fatalf("expected base url %q, got %q", "https://api.openai.example/v1", first.Channel.BaseURL)
	}
	// 渠道凭据明文存储：候选直接取用 channels.credential 明文，无解密环节。
	if first.Channel.APIKey != "secret://openai/main" {
		t.Fatalf("expected plaintext credential as API key, got %q", first.Channel.APIKey)
	}
	if first.Channel.Timeout != 15*time.Second {
		t.Fatalf("expected timeout %v, got %v", 15*time.Second, first.Channel.Timeout)
	}

	second := got.Candidates[1]
	if second.Channel.ID != 456 {
		t.Fatalf("expected second channel id %d, got %d", int64(456), second.Channel.ID)
	}
	if second.Channel.APIKey != "secret://openai/backup" {
		t.Fatalf("expected second plaintext credential, got %q", second.Channel.APIKey)
	}
	if second.Channel.Timeout != 30*time.Second {
		t.Fatalf("expected second timeout %v, got %v", 30*time.Second, second.Channel.Timeout)
	}
}

func TestNewRouterUsesFallbackDefaultTimeout(t *testing.T) {
	store := &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				AdapterKey:    "openai",
				ChannelID:     123,
				BaseUrl:       "https://api.openai.example/v1",
				Credential:    "secret://openai/main",
				TimeoutMs:     pgtype.Int4{Valid: false},
				UpstreamModel: "gpt-4.1",
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

	if got.Candidates[0].Channel.Timeout != defaultChannelTimeout {
		t.Fatalf("expected fallback default timeout %v, got %v", defaultChannelTimeout, got.Candidates[0].Channel.Timeout)
	}
}

func TestRouterPlanChatReturnsNoAvailableChannel(t *testing.T) {
	store := &fakeStore{
		modelExists: true,
		userCanUse:  true,
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
	if store.modelExistsID != "openai/gpt-4.1" {
		t.Fatalf("expected model exists check for %q, got %q", "openai/gpt-4.1", store.modelExistsID)
	}
	if store.userCanUseParams.UserID != 42 {
		t.Fatalf("expected user can use check for user %d, got %d", int64(42), store.userCanUseParams.UserID)
	}
	if store.userCanUseParams.RequestedModelID != "openai/gpt-4.1" {
		t.Fatalf("expected user can use check for model %q, got %q", "openai/gpt-4.1", store.userCanUseParams.RequestedModelID)
	}
}

func TestRouterPlanChatReturnsModelNotFound(t *testing.T) {
	store := &fakeStore{modelExists: false}
	router := NewRouter(store, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
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

func TestRouterPlanChatReturnsModelNotAvailable(t *testing.T) {
	store := &fakeStore{
		modelExists: true,
		userCanUse:  false,
	}
	router := NewRouter(store, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		UserID:          42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
		RouteID:         testRouteID(),
	})
	if !errors.Is(err, ErrModelNotAvailable) {
		t.Fatalf("expected ErrModelNotAvailable, got %v", err)
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
				AdapterKey:    "openai",
				ChannelID:     123,
				BaseUrl:       "https://api.openai.example/v1",
				Credential:    "",
				TimeoutMs:     pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel: "gpt-4.1",
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
				AdapterKey:    "openai",
				ChannelID:     111,
				BaseUrl:       "https://bad.openai.example/v1",
				Credential:    "", // 坏候选：缺凭据，应被跳过
				TimeoutMs:     pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel: "gpt-4.1",
			},
			{
				AdapterKey:    "openai",
				ChannelID:     222,
				BaseUrl:       "https://good.openai.example/v1",
				Credential:    "secret://openai/good",
				TimeoutMs:     pgtype.Int4{Int32: 30000, Valid: true},
				UpstreamModel: "gpt-4.1",
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
