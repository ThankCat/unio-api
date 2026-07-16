package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type fakeGatewayServerAppDB struct {
	rows       []sqlc.ListEnabledChannelAdaptersRow
	queryErr   error
	queryCount int
}

func (db *fakeGatewayServerAppDB) Exec(ctx context.Context, sql string, args ...interface{}) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, nil
}

func (db *fakeGatewayServerAppDB) Query(ctx context.Context, sql string, args ...interface{}) (pgx.Rows, error) {
	db.queryCount++
	if db.queryErr != nil {
		return nil, db.queryErr
	}
	return &fakeGatewayServerAppRows{rows: db.rows, index: -1}, nil
}

func (db *fakeGatewayServerAppDB) QueryRow(ctx context.Context, sql string, args ...interface{}) pgx.Row {
	return fakeGatewayServerAppRow{}
}

func (db *fakeGatewayServerAppDB) Begin(ctx context.Context) (pgx.Tx, error) {
	return nil, nil
}

type fakeGatewayServerAppRow struct{}

func (r fakeGatewayServerAppRow) Scan(dest ...any) error {
	return errors.New("fake server app row is not implemented")
}

type fakeGatewayServerAppRows struct {
	rows   []sqlc.ListEnabledChannelAdaptersRow
	index  int
	closed bool
}

func (r *fakeGatewayServerAppRows) Close() {
	r.closed = true
}

func (r *fakeGatewayServerAppRows) Err() error {
	return nil
}

func (r *fakeGatewayServerAppRows) CommandTag() pgconn.CommandTag {
	return pgconn.CommandTag{}
}

func (r *fakeGatewayServerAppRows) FieldDescriptions() []pgconn.FieldDescription {
	return nil
}

func (r *fakeGatewayServerAppRows) Next() bool {
	if r.closed {
		return false
	}

	r.index++
	if r.index >= len(r.rows) {
		r.Close()
		return false
	}

	return true
}

func (r *fakeGatewayServerAppRows) Scan(dest ...any) error {
	if r.index < 0 || r.index >= len(r.rows) {
		return errors.New("fake server app rows scan called without current row")
	}
	if len(dest) != 4 {
		return fmt.Errorf("expected 4 scan destinations, got %d", len(dest))
	}

	channelID, ok := dest[0].(*int64)
	if !ok {
		return fmt.Errorf("expected destination 0 to be *int64, got %T", dest[0])
	}
	protocol, ok := dest[1].(*string)
	if !ok {
		return fmt.Errorf("expected destination 1 to be *string, got %T", dest[1])
	}
	adapterKey, ok := dest[2].(*string)
	if !ok {
		return fmt.Errorf("expected destination 2 to be *string, got %T", dest[2])
	}
	providerSlug, ok := dest[3].(*string)
	if !ok {
		return fmt.Errorf("expected destination 3 to be *string, got %T", dest[3])
	}

	row := r.rows[r.index]
	*channelID = row.ChannelID
	*protocol = row.Protocol
	*adapterKey = row.AdapterKey
	*providerSlug = row.ProviderSlug
	return nil
}

func (r *fakeGatewayServerAppRows) Values() ([]any, error) {
	if r.index < 0 || r.index >= len(r.rows) {
		return nil, errors.New("fake server app rows values called without current row")
	}

	row := r.rows[r.index]
	return []any{row.ChannelID, row.Protocol, row.AdapterKey, row.ProviderSlug}, nil
}

func (r *fakeGatewayServerAppRows) RawValues() [][]byte {
	return nil
}

func (r *fakeGatewayServerAppRows) Conn() *pgx.Conn {
	return nil
}

func TestNewGatewayServerAppBuildsHandlerAfterProviderPreflight(t *testing.T) {
	db := &fakeGatewayServerAppDB{
		rows: []sqlc.ListEnabledChannelAdaptersRow{
			{ChannelID: 1, Protocol: "openai", AdapterKey: "deepseek", ProviderSlug: "deepseek"},
			{ChannelID: 2, Protocol: "anthropic", AdapterKey: "deepseek", ProviderSlug: "deepseek"},
		},
	}

	app, err := NewGatewayServerApp(context.Background(), GatewayServerAppDeps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: newGatewayServerAppTestConfig(),
		DB:     db,
	})
	if err != nil {
		t.Fatalf("NewGatewayServerApp returned error: %v", err)
	}
	if app == nil || app.Handler == nil {
		t.Fatal("expected server app with handler")
	}
	if db.queryCount != 1 {
		t.Fatalf("expected provider adapter preflight query once, got %d", db.queryCount)
	}

	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	app.Handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %q", http.StatusOK, rec.Code, rec.Body.String())
	}
	if rec.Body.String() != "{\"status\":\"ok\"}\n" {
		t.Fatalf("unexpected health response body %q", rec.Body.String())
	}
}

func TestNewGatewayServerAppReturnsProviderAdapterPreflightError(t *testing.T) {
	db := &fakeGatewayServerAppDB{
		rows: []sqlc.ListEnabledChannelAdaptersRow{
			{ChannelID: 1, Protocol: "openai", AdapterKey: "unknown", ProviderSlug: "unknown"},
		},
	}

	app, err := NewGatewayServerApp(context.Background(), GatewayServerAppDeps{
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: newGatewayServerAppTestConfig(),
		DB:     db,
	})
	if err == nil {
		t.Fatal("expected preflight error")
	}
	if app != nil {
		t.Fatal("expected no server app when preflight fails")
	}
	if !errors.Is(err, ErrProviderAdapterCapabilityMissing) {
		t.Fatalf("expected ErrProviderAdapterCapabilityMissing, got %v", err)
	}
}

func newGatewayServerAppTestConfig() config.Config {
	// 限流/熔断等 6 组已迁移为运行时配置(app_settings):fake DB 读不到行时回退注册表默认。
	return config.Config{
		Redis: config.RedisConfig{KeyNamespace: "unio:test"},
	}
}
