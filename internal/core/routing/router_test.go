package routing

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/credential"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

// fakeStore 是 routing 测试使用的候选 channel 存储替身。
type fakeStore struct {
	params              sqlc.FindRouteCandidatesParams
	rows                []sqlc.FindRouteCandidatesRow
	err                 error
	modelExistsID       string
	modelExists         bool
	modelExistsErr      error
	projectCanUseParams sqlc.ProjectCanUseModelParams
	projectCanUse       bool
	projectCanUseErr    error
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

// ProjectCanUseModel 记录 project 模型可用性诊断参数，并返回测试预设结果。
func (s *fakeStore) ProjectCanUseModel(ctx context.Context, arg sqlc.ProjectCanUseModelParams) (bool, error) {
	s.projectCanUseParams = arg
	return s.projectCanUse, s.projectCanUseErr
}

// fakeCredentialDecryptor 是 routing 测试使用的凭据解密替身。
type fakeCredentialDecryptor struct {
	ciphertexts [][]byte
	apiKey      string
	err         error
}

// Decrypt 记录密文，并返回测试预设 API key。
func (d *fakeCredentialDecryptor) Decrypt(ciphertext []byte) (string, error) {
	d.ciphertexts = append(d.ciphertexts, append([]byte(nil), ciphertext...))
	if d.err != nil {
		return "", d.err
	}

	return d.apiKey, nil
}

func mustEncryptTestCredential(t *testing.T, plaintext string) []byte {
	t.Helper()

	encrypted, err := credential.EncryptFixedTestCredential(plaintext)
	if err != nil {
		t.Fatalf("encrypt test credential: %v", err)
	}

	return encrypted
}

func TestRouterPlanChatReturnsOrderedCandidates(t *testing.T) {
	primaryEncrypted := mustEncryptTestCredential(t, "secret://openai/main")
	backupEncrypted := mustEncryptTestCredential(t, "secret://openai/backup")

	store := &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				RequestedModelID:    "openai/gpt-4.1",
				ProviderID:          11,
				AdapterKey:          "openai",
				ChannelID:           123,
				BaseUrl:             "https://api.openai.example/v1",
				CredentialEncrypted: primaryEncrypted,
				TimeoutMs:           pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel:       "gpt-4.1",
			},
			{
				RequestedModelID:    "openai/gpt-4.1",
				ProviderID:          11,
				AdapterKey:          "openai",
				ChannelID:           456,
				BaseUrl:             "https://backup.openai.example/v1",
				CredentialEncrypted: backupEncrypted,
				TimeoutMs:           pgtype.Int4{Int32: 30000, Valid: true},
				UpstreamModel:       "gpt-4.1",
			},
		},
	}
	decryptor := &fakeCredentialDecryptor{apiKey: "resolved-secret"}
	router := NewRouter(store, decryptor, 30*time.Second)

	got, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:       42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
	})
	if err != nil {
		t.Fatalf("PlanChat returned error: %v", err)
	}

	if store.params.ProjectID != 42 {
		t.Fatalf("expected project id %d, got %d", int64(42), store.params.ProjectID)
	}
	if store.params.RequestedModelID != "openai/gpt-4.1" {
		t.Fatalf("expected requested model %q, got %q", "openai/gpt-4.1", store.params.RequestedModelID)
	}
	if store.params.IngressProtocol != ProtocolOpenAI {
		t.Fatalf("expected ingress protocol %q, got %q", ProtocolOpenAI, store.params.IngressProtocol)
	}
	if len(decryptor.ciphertexts) != 2 {
		t.Fatalf("expected 2 credential decrypt calls, got %d", len(decryptor.ciphertexts))
	}
	if !bytes.Equal(decryptor.ciphertexts[0], primaryEncrypted) {
		t.Fatal("expected primary encrypted credential to be decrypted")
	}
	if !bytes.Equal(decryptor.ciphertexts[1], backupEncrypted) {
		t.Fatal("expected backup encrypted credential to be decrypted")
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
	if first.Channel.APIKey != "resolved-secret" {
		t.Fatalf("expected resolved API key, got %q", first.Channel.APIKey)
	}
	if first.Channel.Timeout != 15*time.Second {
		t.Fatalf("expected timeout %v, got %v", 15*time.Second, first.Channel.Timeout)
	}

	second := got.Candidates[1]
	if second.Channel.ID != 456 {
		t.Fatalf("expected second channel id %d, got %d", int64(456), second.Channel.ID)
	}
	if second.Channel.Timeout != 30*time.Second {
		t.Fatalf("expected second timeout %v, got %v", 30*time.Second, second.Channel.Timeout)
	}
}

func TestNewRouterUsesFallbackDefaultTimeout(t *testing.T) {
	store := &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				AdapterKey:          "openai",
				ChannelID:           123,
				BaseUrl:             "https://api.openai.example/v1",
				CredentialEncrypted: mustEncryptTestCredential(t, "secret://openai/main"),
				TimeoutMs:           pgtype.Int4{Valid: false},
				UpstreamModel:       "gpt-4.1",
			},
		},
	}
	router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved-secret"}, 0)

	got, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:       42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
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
		modelExists:   true,
		projectCanUse: true,
	}
	router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved-secret"}, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:       42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
	})
	if !errors.Is(err, ErrNoAvailableChannel) {
		t.Fatalf("expected ErrNoAvailableChannel, got %v", err)
	}
	if store.modelExistsID != "openai/gpt-4.1" {
		t.Fatalf("expected model exists check for %q, got %q", "openai/gpt-4.1", store.modelExistsID)
	}
	if store.projectCanUseParams.ProjectID != 42 {
		t.Fatalf("expected project can use check for project %d, got %d", int64(42), store.projectCanUseParams.ProjectID)
	}
	if store.projectCanUseParams.RequestedModelID != "openai/gpt-4.1" {
		t.Fatalf("expected project can use check for model %q, got %q", "openai/gpt-4.1", store.projectCanUseParams.RequestedModelID)
	}
}

func TestRouterPlanChatReturnsModelNotFound(t *testing.T) {
	store := &fakeStore{modelExists: false}
	router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved-secret"}, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:       42,
		ModelID:         "openai/missing",
		IngressProtocol: ProtocolOpenAI,
	})
	if !errors.Is(err, ErrModelNotFound) {
		t.Fatalf("expected ErrModelNotFound, got %v", err)
	}
	if store.projectCanUseParams.ProjectID != 0 {
		t.Fatalf("expected project policy check to be skipped, got %#v", store.projectCanUseParams)
	}
}

func TestRouterPlanChatReturnsModelNotAvailable(t *testing.T) {
	store := &fakeStore{
		modelExists:   true,
		projectCanUse: false,
	}
	router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved-secret"}, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:       42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
	})
	if !errors.Is(err, ErrModelNotAvailable) {
		t.Fatalf("expected ErrModelNotAvailable, got %v", err)
	}
}

func TestRouterPlanChatReturnsStoreError(t *testing.T) {
	storeErr := errors.New("database unavailable")
	router := NewRouter(&fakeStore{err: storeErr}, &fakeCredentialDecryptor{apiKey: "resolved-secret"}, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:       42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
	})
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected store error, got %v", err)
	}
}

func TestRouterPlanChatReturnsCredentialDecryptError(t *testing.T) {
	decryptErr := errors.New("decrypt unavailable")
	store := &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				AdapterKey:          "openai",
				ChannelID:           123,
				BaseUrl:             "https://api.openai.example/v1",
				CredentialEncrypted: mustEncryptTestCredential(t, "secret://openai/main"),
				TimeoutMs:           pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel:       "gpt-4.1",
			},
		},
	}
	router := NewRouter(store, &fakeCredentialDecryptor{err: decryptErr}, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:       42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
	})
	if !errors.Is(err, decryptErr) {
		t.Fatalf("expected credential decrypt error, got %v", err)
	}
}

func TestRouterPlanChatReturnsMissingCredentialError(t *testing.T) {
	store := &fakeStore{
		rows: []sqlc.FindRouteCandidatesRow{
			{
				AdapterKey:          "openai",
				ChannelID:           123,
				BaseUrl:             "https://api.openai.example/v1",
				CredentialEncrypted: nil,
				TimeoutMs:           pgtype.Int4{Int32: 15000, Valid: true},
				UpstreamModel:       "gpt-4.1",
			},
		},
	}
	router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved-secret"}, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:       42,
		ModelID:         "openai/gpt-4.1",
		IngressProtocol: ProtocolOpenAI,
	})
	if !errors.Is(err, ErrChannelCredentialMissing) {
		t.Fatalf("expected ErrChannelCredentialMissing, got %v", err)
	}
}

func TestRouterPlanChatRejectsInvalidIngressProtocolBeforeQuery(t *testing.T) {
	store := &fakeStore{}
	router := NewRouter(store, &fakeCredentialDecryptor{apiKey: "resolved-secret"}, 30*time.Second)

	_, err := router.PlanChat(context.Background(), ChatRouteRequest{
		ProjectID:       42,
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
