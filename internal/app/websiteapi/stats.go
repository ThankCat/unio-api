package websiteapi

import (
	"context"
	"net/http"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// ProductionStats 提供营销首页用的运行指标。
type ProductionStats interface {
	MinFirstTokenMs(ctx context.Context) (*float64, error)
}

type firstTokenQuery interface {
	MinWebsiteFirstTokenMs(ctx context.Context) (float64, error)
}

type sqlProductionStats struct {
	query firstTokenQuery
}

// NewSQLProductionStats 把 sqlc 的最快首字查询适配成 ProductionStats。
func NewSQLProductionStats(query firstTokenQuery) ProductionStats {
	return sqlProductionStats{query: query}
}

func (s sqlProductionStats) MinFirstTokenMs(ctx context.Context) (*float64, error) {
	ms, err := s.query.MinWebsiteFirstTokenMs(ctx)
	if err != nil {
		return nil, err
	}
	if ms <= 0 {
		return nil, nil
	}
	return &ms, nil
}

type statsHandler struct {
	service ProductionStats
	logger  *zap.Logger
}

type minFirstTokenData struct {
	// MinFirstTokenMs 是请求记录里最快的客户侧首字（毫秒）；无样本时为 null。
	MinFirstTokenMs *float64 `json:"min_first_token_ms"`
	GeneratedAt     string   `json:"generated_at"`
}

func (h *statsHandler) minFirstTokenMs(w http.ResponseWriter, r *http.Request) {
	ms, err := h.service.MinFirstTokenMs(r.Context())
	if err != nil {
		h.logger.Error("website min first token failed", zap.Error(err))
		_ = httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "failed to load min first token")
		return
	}

	w.Header().Set("Cache-Control", modelsCacheControl)
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]minFirstTokenData{"data": {
		MinFirstTokenMs: ms,
		GeneratedAt:     time.Now().UTC().Format(time.RFC3339),
	}})
}
