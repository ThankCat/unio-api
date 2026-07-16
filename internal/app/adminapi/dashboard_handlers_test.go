package adminapi_test

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/admin/dashboard"
)

type fakeDashboardService struct {
	// gotMetric/gotInterval 记录最近一次 Timeseries 入参，用于断言透传。
	gotMetric   string
	gotInterval string
}

func (s *fakeDashboardService) Timeseries(_ context.Context, metric, interval string, from, to time.Time) (dashboard.Series, error) {
	s.gotMetric, s.gotInterval = metric, interval
	// 模拟真实 service：metric/interval 非法返回 admin_invalid_argument。
	if !validDashboardTestInterval(interval) {
		return dashboard.Series{}, failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage("bad interval"))
	}
	switch metric {
	case dashboard.MetricRequests, dashboard.MetricTokens, dashboard.MetricSpend, dashboard.MetricCost:
	default:
		return dashboard.Series{}, failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage("bad metric"))
	}
	return dashboard.Series{Metric: metric, Interval: interval, From: from, To: to}, nil
}

func (s *fakeDashboardService) Radar(_ context.Context, from, to time.Time) (dashboard.RadarReport, error) {
	return dashboard.RadarReport{From: from, To: to}, nil
}

func (s *fakeDashboardService) Breakdown(_ context.Context, dimension string, _, _ time.Time) ([]dashboard.BreakdownRow, error) {
	switch dimension {
	case dashboard.BreakdownProvider, dashboard.BreakdownRoute, dashboard.BreakdownChannel, dashboard.BreakdownModel:
		return []dashboard.BreakdownRow{}, nil
	default:
		return nil, failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage("bad dimension"))
	}
}

func (s *fakeDashboardService) PerformanceTimeseries(_ context.Context, interval string, _, _ time.Time) ([]dashboard.PerformancePoint, error) {
	if !validDashboardTestInterval(interval) {
		return nil, failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage("bad interval"))
	}
	return []dashboard.PerformancePoint{}, nil
}

func (s *fakeDashboardService) TopErrors(_ context.Context, _, _ time.Time) ([]dashboard.ErrorGroup, error) {
	return []dashboard.ErrorGroup{}, nil
}

func validDashboardTestInterval(interval string) bool {
	return interval == dashboard.IntervalMinute || interval == dashboard.IntervalHour || interval == dashboard.IntervalDay
}

func TestDashboardTimeseriesReturns200(t *testing.T) {
	svc := &fakeDashboardService{}
	handler := newQueryRouter(t, adminapi.RouterDeps{DashboardService: svc})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/dashboard/timeseries?metric=spend&interval=day", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
	if svc.gotMetric != "spend" || svc.gotInterval != "day" {
		t.Fatalf("expected metric=spend interval=day passed through, got %q/%q", svc.gotMetric, svc.gotInterval)
	}
}

func TestDashboardTimeseriesInvalidMetricReturns400(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{DashboardService: &fakeDashboardService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/dashboard/timeseries?metric=bogus&interval=day", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestDashboardTimeseriesInvalidIntervalReturns400(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{DashboardService: &fakeDashboardService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/dashboard/timeseries?metric=requests&interval=week", "", true)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected %d, got %d (%s)", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
}

func TestDashboardRequiresToken(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{DashboardService: &fakeDashboardService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/dashboard/radar", "", false)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestDashboardBreakdownProviderDimensionReturns200(t *testing.T) {
	handler := newQueryRouter(t, adminapi.RouterDeps{DashboardService: &fakeDashboardService{}})

	rec := doAdmin(t, handler, http.MethodGet, "/admin/v1/dashboard/breakdown?dimension=provider&range=24h", "", true)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected %d, got %d (%s)", http.StatusOK, rec.Code, rec.Body.String())
	}
}
