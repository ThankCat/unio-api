package websiteapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/service/publicmodels"
)

type fakeModels struct {
	ratio *float64
	err   error
}

func (f *fakeModels) List(context.Context) ([]publicmodels.Model, error) {
	return nil, nil
}

func (f *fakeModels) ListDiscountHistory(context.Context, time.Duration) ([]publicmodels.DiscountHistory, error) {
	return nil, nil
}

func (f *fakeModels) MinSaleDiscount(context.Context) (*float64, error) {
	return f.ratio, f.err
}

func TestMinSaleDiscountRoute(t *testing.T) {
	t.Parallel()
	ratio := 0.2
	handler := NewRouter(Deps{Logger: zap.NewNop(), Models: &fakeModels{ratio: &ratio}})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/min-sale-discount", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != modelsCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, modelsCacheControl)
	}

	var body struct {
		Data struct {
			MinSaleDiscount *float64 `json:"min_sale_discount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	if body.Data.MinSaleDiscount == nil || *body.Data.MinSaleDiscount != 0.2 {
		t.Fatalf("min_sale_discount = %v, want 0.2", body.Data.MinSaleDiscount)
	}
}

func TestMinSaleDiscountRouteEmpty(t *testing.T) {
	t.Parallel()
	handler := NewRouter(Deps{Logger: zap.NewNop(), Models: &fakeModels{}})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/models/min-sale-discount", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data struct {
			MinSaleDiscount *float64 `json:"min_sale_discount"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	if body.Data.MinSaleDiscount != nil {
		t.Fatalf("empty catalog want null, got %v", *body.Data.MinSaleDiscount)
	}
}

type fakeStats struct {
	ms  *float64
	err error
}

func (f *fakeStats) MinFirstTokenMs(context.Context) (*float64, error) {
	return f.ms, f.err
}

func TestMinFirstTokenMsRoute(t *testing.T) {
	t.Parallel()
	ms := 847.0
	handler := NewRouter(Deps{
		Logger: zap.NewNop(),
		Models: &fakeModels{},
		Stats:  &fakeStats{ms: &ms},
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/stats/min-first-token-ms", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Cache-Control"); got != modelsCacheControl {
		t.Fatalf("Cache-Control = %q, want %q", got, modelsCacheControl)
	}

	var body struct {
		Data struct {
			MinFirstTokenMs *float64 `json:"min_first_token_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	if body.Data.MinFirstTokenMs == nil || *body.Data.MinFirstTokenMs != 847 {
		t.Fatalf("min_first_token_ms = %v, want 847", body.Data.MinFirstTokenMs)
	}
}

func TestMinFirstTokenMsRouteEmpty(t *testing.T) {
	t.Parallel()
	handler := NewRouter(Deps{
		Logger: zap.NewNop(),
		Models: &fakeModels{},
		Stats:  &fakeStats{},
	})

	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/v1/stats/min-first-token-ms", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", rec.Code, rec.Body.String())
	}

	var body struct {
		Data struct {
			MinFirstTokenMs *float64 `json:"min_first_token_ms"`
		} `json:"data"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v; body = %s", err, rec.Body.String())
	}
	if body.Data.MinFirstTokenMs != nil {
		t.Fatalf("empty records want null, got %v", *body.Data.MinFirstTokenMs)
	}
}

type fakeFirstTokenQuery struct {
	ms  float64
	err error
}

func (f fakeFirstTokenQuery) MinWebsiteFirstTokenMs(context.Context) (float64, error) {
	return f.ms, f.err
}

func TestSQLProductionStatsTreatsZeroAsEmpty(t *testing.T) {
	t.Parallel()
	got, err := NewSQLProductionStats(fakeFirstTokenQuery{}).MinFirstTokenMs(context.Background())
	if err != nil {
		t.Fatalf("MinFirstTokenMs: %v", err)
	}
	if got != nil {
		t.Fatalf("zero want nil, got %v", *got)
	}
}

func TestSQLProductionStatsReturnsPositiveMs(t *testing.T) {
	t.Parallel()
	got, err := NewSQLProductionStats(fakeFirstTokenQuery{ms: 847}).MinFirstTokenMs(context.Background())
	if err != nil {
		t.Fatalf("MinFirstTokenMs: %v", err)
	}
	if got == nil || *got != 847 {
		t.Fatalf("got %v, want 847", got)
	}
}
