// Package customerops 提供客户中心（用户/项目/API Key §3.7）的只读运维聚合。金额仅 USD。
package customerops

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/opsutil"
)

// Store 是客户运维聚合所需的只读存储能力（由 *sqlc.Queries 满足）。
type Store interface {
	UsersOpsSummary(ctx context.Context, arg sqlc.UsersOpsSummaryParams) (sqlc.UsersOpsSummaryRow, error)
	UsersOpsTable(ctx context.Context, arg sqlc.UsersOpsTableParams) ([]sqlc.UsersOpsTableRow, error)
	UsersOpsTableCount(ctx context.Context, search pgtype.Text) (int64, error)
	UserOpsDetail(ctx context.Context, arg sqlc.UserOpsDetailParams) (sqlc.UserOpsDetailRow, error)
	UserOpsKeys(ctx context.Context, userID int64) ([]sqlc.UserOpsKeysRow, error)
	ProjectsOpsSummary(ctx context.Context, arg sqlc.ProjectsOpsSummaryParams) (sqlc.ProjectsOpsSummaryRow, error)
	ProjectsOpsTable(ctx context.Context, arg sqlc.ProjectsOpsTableParams) ([]sqlc.ProjectsOpsTableRow, error)
	ProjectsOpsTableCount(ctx context.Context, search pgtype.Text) (int64, error)
	ApiKeysOpsSummary(ctx context.Context, projectID int64) (sqlc.ApiKeysOpsSummaryRow, error)
	ApiKeysOpsTable(ctx context.Context, arg sqlc.ApiKeysOpsTableParams) ([]sqlc.ApiKeysOpsTableRow, error)
}

// Service 提供客户运维只读聚合。
type Service struct {
	store Store
}

// NewService 创建客户运维聚合服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

// ---- 用户 ----

type UsersSummary struct {
	UserTotal       int64
	BalanceUSD      string
	ReservedUSD     string
	AvailableUSD    string
	LowBalanceTotal int64
	RequestTotal    int64
	Succeeded       int64
	SuccessRate     float64
	ConsumptionUSD  string
}

type UserRow struct {
	ID             int64
	Email          string
	DisplayName    string
	BalanceUSD     string
	ReservedUSD    string
	AvailableUSD   string
	ProjectCount   int64
	KeyTotal       int64
	RequestTotal   int64
	Succeeded      int64
	SuccessRate    float64
	ConsumptionUSD string
	LastUsedAt     *time.Time
	LowBalance     bool
}

// UsersTableParams 用户运维主表入参。
type UsersTableParams struct {
	From      time.Time
	To        time.Time
	Search    string
	SortField string
	SortDesc  bool
	Limit     int32
	Offset    int32
}

type UserDetail struct {
	BalanceUSD     string
	ReservedUSD    string
	AvailableUSD   string
	RequestTotal   int64
	Succeeded      int64
	SuccessRate    float64
	ConsumptionUSD string
}

type KeyRow struct {
	ID          int64
	Name        string
	ProjectID   int64
	ProjectName string
	Status      string
	SpendLimit  *string
	SpentTotal  string
	LastUsedAt  *time.Time
}

func (s *Service) UsersSummary(ctx context.Context, from, to time.Time) (UsersSummary, error) {
	r, err := s.store.UsersOpsSummary(ctx, sqlc.UsersOpsSummaryParams{FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return UsersSummary{}, opsutil.StoreFailed(err, "users ops summary")
	}
	balance := opsutil.NumericString(r.BalanceUsd)
	reserved := opsutil.NumericString(r.ReservedUsd)
	return UsersSummary{
		UserTotal:       r.UserTotal,
		BalanceUSD:      balance,
		ReservedUSD:     reserved,
		AvailableUSD:    opsutil.SubtractDecimal(balance, reserved),
		LowBalanceTotal: r.LowBalanceTotal,
		RequestTotal:    r.RequestTotal,
		Succeeded:       r.RequestSucceeded,
		SuccessRate:     opsutil.SuccessRate(r.RequestSucceeded, r.RequestTotal),
		ConsumptionUSD:  opsutil.NumericString(r.ConsumptionUsd),
	}, nil
}

func (s *Service) UsersTable(ctx context.Context, p UsersTableParams) ([]UserRow, int64, error) {
	rows, err := s.store.UsersOpsTable(ctx, sqlc.UsersOpsTableParams{
		FromTime:   opsutil.TsNarg(p.From),
		ToTime:     opsutil.TsNarg(p.To),
		Search:     opsutil.TextNarg(p.Search),
		SortField:  opsutil.TextNarg(p.SortField),
		SortDesc:   opsutil.BoolNarg(p.SortDesc),
		PageLimit:  p.Limit,
		PageOffset: p.Offset,
	})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "users ops table")
	}
	total, err := s.store.UsersOpsTableCount(ctx, opsutil.TextNarg(p.Search))
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "users ops table count")
	}
	out := make([]UserRow, 0, len(rows))
	for _, r := range rows {
		balance := opsutil.NumericString(r.BalanceUsd)
		reserved := opsutil.NumericString(r.ReservedUsd)
		available := opsutil.SubtractDecimal(balance, reserved)
		out = append(out, UserRow{
			ID:             r.ID,
			Email:          r.Email,
			DisplayName:    r.DisplayName,
			BalanceUSD:     balance,
			ReservedUSD:    reserved,
			AvailableUSD:   available,
			ProjectCount:   r.ProjectCount,
			KeyTotal:       r.KeyTotal,
			RequestTotal:   r.RequestTotal,
			Succeeded:      r.RequestSucceeded,
			SuccessRate:    opsutil.SuccessRate(r.RequestSucceeded, r.RequestTotal),
			ConsumptionUSD: opsutil.NumericString(r.ConsumptionUsd),
			LastUsedAt:     opsutil.TimeValue(r.LastUsedAt),
			LowBalance:     opsutil.Ratio(available, "1") < 5 && opsutil.Ratio(balance, "1") > 0,
		})
	}
	return out, total, nil
}

func (s *Service) UserDetail(ctx context.Context, userID int64, from, to time.Time) (UserDetail, error) {
	r, err := s.store.UserOpsDetail(ctx, sqlc.UserOpsDetailParams{UserID: userID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return UserDetail{}, opsutil.StoreFailed(err, "user ops detail")
	}
	balance := opsutil.NumericString(r.BalanceUsd)
	reserved := opsutil.NumericString(r.ReservedUsd)
	return UserDetail{
		BalanceUSD:     balance,
		ReservedUSD:    reserved,
		AvailableUSD:   opsutil.SubtractDecimal(balance, reserved),
		RequestTotal:   r.RequestTotal,
		Succeeded:      r.RequestSucceeded,
		SuccessRate:    opsutil.SuccessRate(r.RequestSucceeded, r.RequestTotal),
		ConsumptionUSD: opsutil.NumericString(r.ConsumptionUsd),
	}, nil
}

func (s *Service) UserKeys(ctx context.Context, userID int64) ([]KeyRow, error) {
	rows, err := s.store.UserOpsKeys(ctx, userID)
	if err != nil {
		return nil, opsutil.StoreFailed(err, "user ops keys")
	}
	now := time.Now()
	out := make([]KeyRow, 0, len(rows))
	for _, k := range rows {
		out = append(out, KeyRow{
			ID:          k.ID,
			Name:        k.Name,
			ProjectID:   k.ProjectID,
			ProjectName: k.ProjectName,
			Status:      keyStatus(k.DisabledAt, k.RevokedAt, k.ExpiresAt, now),
			SpendLimit:  numericPtr(k.SpendLimit),
			SpentTotal:  opsutil.NumericString(k.SpentTotal),
			LastUsedAt:  opsutil.TimeValue(k.LastUsedAt),
		})
	}
	return out, nil
}

// ---- 项目 ----

type ProjectsSummary struct {
	ProjectTotal   int64
	KeyTotal       int64
	KeyEnabled     int64
	RequestTotal   int64
	ConsumptionUSD string
}

type ProjectRow struct {
	ID               int64
	Name             string
	UserID           int64
	UserEmail        string
	DefaultRouteName string
	KeyTotal         int64
	KeyEnabled       int64
	RequestTotal     int64
	ConsumptionUSD   string
	LastUsedAt       *time.Time
}

// ProjectsTableParams 项目运维主表入参。
type ProjectsTableParams struct {
	From      time.Time
	To        time.Time
	Search    string
	SortField string
	SortDesc  bool
	Limit     int32
	Offset    int32
}

func (s *Service) ProjectsSummary(ctx context.Context, from, to time.Time) (ProjectsSummary, error) {
	r, err := s.store.ProjectsOpsSummary(ctx, sqlc.ProjectsOpsSummaryParams{FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return ProjectsSummary{}, opsutil.StoreFailed(err, "projects ops summary")
	}
	return ProjectsSummary{
		ProjectTotal:   r.ProjectTotal,
		KeyTotal:       r.KeyTotal,
		KeyEnabled:     r.KeyEnabled,
		RequestTotal:   r.RequestTotal,
		ConsumptionUSD: opsutil.NumericString(r.ConsumptionUsd),
	}, nil
}

func (s *Service) ProjectsTable(ctx context.Context, p ProjectsTableParams) ([]ProjectRow, int64, error) {
	rows, err := s.store.ProjectsOpsTable(ctx, sqlc.ProjectsOpsTableParams{
		FromTime:   opsutil.TsNarg(p.From),
		ToTime:     opsutil.TsNarg(p.To),
		Search:     opsutil.TextNarg(p.Search),
		SortField:  opsutil.TextNarg(p.SortField),
		SortDesc:   opsutil.BoolNarg(p.SortDesc),
		PageLimit:  p.Limit,
		PageOffset: p.Offset,
	})
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "projects ops table")
	}
	total, err := s.store.ProjectsOpsTableCount(ctx, opsutil.TextNarg(p.Search))
	if err != nil {
		return nil, 0, opsutil.StoreFailed(err, "projects ops table count")
	}
	out := make([]ProjectRow, 0, len(rows))
	for _, r := range rows {
		out = append(out, ProjectRow{
			ID:               r.ID,
			Name:             r.Name,
			UserID:           r.UserID,
			UserEmail:        r.UserEmail,
			DefaultRouteName: opsutil.TextValue(r.DefaultRouteName),
			KeyTotal:         r.KeyTotal,
			KeyEnabled:       r.KeyEnabled,
			RequestTotal:     r.RequestTotal,
			ConsumptionUSD:   opsutil.NumericString(r.ConsumptionUsd),
			LastUsedAt:       opsutil.TimeValue(r.LastUsedAt),
		})
	}
	return out, total, nil
}

// ---- API Key（项目范围）----

type ApiKeysSummary struct {
	KeyTotal    int64
	KeyEnabled  int64
	SpendCapped int64
}

type ApiKeyRow struct {
	ID             int64
	Name           string
	KeyPrefix      string
	ProjectID      int64
	Status         string
	RouteName      string
	SpendLimit     *string
	SpentTotal     string
	RequestTotal   int64
	Succeeded      int64
	SuccessRate    float64
	ConsumptionUSD string
	LastUsedAt     *time.Time
	ExpiresAt      *time.Time
}

func (s *Service) ApiKeysSummary(ctx context.Context, projectID int64) (ApiKeysSummary, error) {
	r, err := s.store.ApiKeysOpsSummary(ctx, projectID)
	if err != nil {
		return ApiKeysSummary{}, opsutil.StoreFailed(err, "api keys ops summary")
	}
	return ApiKeysSummary{KeyTotal: r.KeyTotal, KeyEnabled: r.KeyEnabled, SpendCapped: r.SpendCapped}, nil
}

func (s *Service) ApiKeysTable(ctx context.Context, projectID int64, from, to time.Time) ([]ApiKeyRow, error) {
	rows, err := s.store.ApiKeysOpsTable(ctx, sqlc.ApiKeysOpsTableParams{ProjectID: projectID, FromTime: opsutil.TsNarg(from), ToTime: opsutil.TsNarg(to)})
	if err != nil {
		return nil, opsutil.StoreFailed(err, "api keys ops table")
	}
	now := time.Now()
	out := make([]ApiKeyRow, 0, len(rows))
	for _, k := range rows {
		out = append(out, ApiKeyRow{
			ID:             k.ID,
			Name:           k.Name,
			KeyPrefix:      k.KeyPrefix,
			ProjectID:      k.ProjectID,
			Status:         keyStatus(k.DisabledAt, k.RevokedAt, k.ExpiresAt, now),
			RouteName:      opsutil.TextValue(k.RouteName),
			SpendLimit:     numericPtr(k.SpendLimit),
			SpentTotal:     opsutil.NumericString(k.SpentTotal),
			RequestTotal:   k.RequestTotal,
			Succeeded:      k.RequestSucceeded,
			SuccessRate:    opsutil.SuccessRate(k.RequestSucceeded, k.RequestTotal),
			ConsumptionUSD: opsutil.NumericString(k.ConsumptionUsd),
			LastUsedAt:     opsutil.TimeValue(k.LastUsedAt),
			ExpiresAt:      opsutil.TimeValue(k.ExpiresAt),
		})
	}
	return out, nil
}

func keyStatus(disabledAt, revokedAt, expiresAt pgtype.Timestamptz, now time.Time) string {
	switch {
	case revokedAt.Valid:
		return "revoked"
	case disabledAt.Valid:
		return "disabled"
	case expiresAt.Valid && expiresAt.Time.Before(now):
		return "expired"
	default:
		return "active"
	}
}

func numericPtr(n pgtype.Numeric) *string {
	if !n.Valid {
		return nil
	}
	s := opsutil.NumericString(n)
	return &s
}
