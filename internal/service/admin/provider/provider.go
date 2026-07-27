// Package provider 编排 admin 管理端的 Provider 读写与独立运行态围栏。
package provider

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const (
	StatusEnabled  = "enabled"
	StatusDisabled = "disabled"
	StatusArchived = "archived"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

type Store interface {
	ListProvidersPage(context.Context, sqlc.ListProvidersPageParams) ([]sqlc.Provider, error)
	CountProviders(context.Context, sqlc.CountProvidersParams) (int64, error)
	GetProvider(context.Context, int64) (sqlc.Provider, error)
	CreateProvider(context.Context, sqlc.CreateProviderParams) (sqlc.Provider, error)
	UpdateProvider(context.Context, sqlc.UpdateProviderParams) (sqlc.Provider, error)
	DeleteProvider(context.Context, int64) (int64, error)
	ArchiveProvider(context.Context, int64) (int64, error)
	RestoreProvider(context.Context, int64) (int64, error)
	CountEnabledChannelsByProvider(context.Context, int64) (int64, error)
	CountNonArchivedChannelsByProvider(context.Context, int64) (int64, error)
}

type RuntimeControl interface {
	InitProviderControl(context.Context, int64, int64, int64, string) (bool, error)
	PurgeProviderRuntime(context.Context, int64) error
}

type ListParams struct {
	Status string
	Query  string
	Limit  int32
	Offset int32
}

type ListResult struct {
	Items []Provider
	Total int64
}

type Provider struct {
	ID                 int64
	Slug               string
	Name               string
	Origin             string
	OriginRevision     int64
	Status             string
	StatusRevision     int64
	CreatedAt          time.Time
	UpdatedAt          time.Time
	ArchivedAt         *time.Time
	RuntimeSyncPending bool
}

type StatusChangeResult struct {
	RuntimeSyncPending bool
}

type CreateInput struct {
	Slug   string
	Name   string
	Origin string
	Status string
}

type UpdateInput struct {
	ID   int64
	Name string
}

type UpdateOriginInput struct {
	ID                     int64
	Origin                 string
	ExpectedOriginRevision int64
	ConfirmEnabledChannels bool
}

type UpdateStatusInput struct {
	ID                     int64
	Status                 string
	ExpectedStatusRevision int64
}

type Service struct {
	store   Store
	fencer  *Fencer
	runtime RuntimeControl
}

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) WithRuntimeControl(fencer *Fencer, runtime RuntimeControl) *Service {
	s.fencer = fencer
	s.runtime = runtime
	return s
}

func (s *Service) List(ctx context.Context, params ListParams) (ListResult, error) {
	status := textParam(params.Status)
	query := textParam(params.Query)
	rows, err := s.store.ListProvidersPage(ctx, sqlc.ListProvidersPageParams{Status: status, Q: query, PageLimit: params.Limit, PageOffset: params.Offset})
	if err != nil {
		return ListResult{}, storeFailed(err, "list providers")
	}
	total, err := s.store.CountProviders(ctx, sqlc.CountProvidersParams{Status: status, Q: query})
	if err != nil {
		return ListResult{}, storeFailed(err, "count providers")
	}
	items := make([]Provider, 0, len(rows))
	for _, row := range rows {
		items = append(items, toProvider(row))
	}
	return ListResult{Items: items, Total: total}, nil
}

func (s *Service) Get(ctx context.Context, id int64) (Provider, error) {
	row, err := s.getRow(ctx, id)
	if err != nil {
		return Provider{}, err
	}
	return toProvider(row), nil
}

func (s *Service) Create(ctx context.Context, input CreateInput) (Provider, error) {
	slug := strings.TrimSpace(input.Slug)
	name := strings.TrimSpace(input.Name)
	if !slugPattern.MatchString(slug) {
		return Provider{}, invalidArgument("slug", "slug must match ^[a-z0-9][a-z0-9-]{0,63}$")
	}
	if name == "" {
		return Provider{}, invalidArgument("name", "name is required")
	}
	origin, err := NormalizeOrigin(input.Origin)
	if err != nil {
		return Provider{}, err
	}
	status := strings.TrimSpace(input.Status)
	if err := validateMutableStatus(status); err != nil {
		return Provider{}, err
	}
	row, err := s.store.CreateProvider(ctx, sqlc.CreateProviderParams{Slug: slug, Name: name, Origin: origin, Status: status})
	if err != nil {
		if isUniqueViolation(err) {
			return Provider{}, conflict("provider slug or origin already exists")
		}
		return Provider{}, storeFailed(err, "create provider")
	}
	result := toProvider(row)
	if s.runtime != nil {
		_, runtimeErr := s.runtime.InitProviderControl(ctx, row.ID, row.OriginRevision, row.StatusRevision, row.Status)
		result.RuntimeSyncPending = runtimeErr != nil
	}
	return result, nil
}

func (s *Service) Update(ctx context.Context, input UpdateInput) (Provider, error) {
	if input.ID <= 0 {
		return Provider{}, invalidArgument("id", "provider id must be positive")
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		return Provider{}, invalidArgument("name", "name is required")
	}
	row, err := s.store.UpdateProvider(ctx, sqlc.UpdateProviderParams{ID: input.ID, Name: name})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Provider{}, notFound("provider not found")
		}
		return Provider{}, storeFailed(err, "update provider")
	}
	return toProvider(row), nil
}

func (s *Service) UpdateOrigin(ctx context.Context, input UpdateOriginInput) (Provider, error) {
	current, err := s.getRow(ctx, input.ID)
	if err != nil {
		return Provider{}, err
	}
	if current.Status == StatusArchived {
		return Provider{}, conflict("archived provider origin cannot be changed")
	}
	if input.ExpectedOriginRevision < 1 || input.ExpectedOriginRevision != current.OriginRevision {
		return Provider{}, conflict("provider origin revision is stale")
	}
	origin, err := NormalizeOrigin(input.Origin)
	if err != nil {
		return Provider{}, err
	}
	if origin == current.Origin {
		return toProvider(current), nil
	}
	enabled, err := s.store.CountEnabledChannelsByProvider(ctx, input.ID)
	if err != nil {
		return Provider{}, storeFailed(err, "count enabled provider channels")
	}
	if enabled > 0 && !input.ConfirmEnabledChannels {
		return Provider{}, conflict("changing provider origin with enabled channels requires confirm_enabled_channels=true")
	}
	if s.fencer == nil {
		return Provider{}, runtimeUnavailable("provider origin runtime publisher is unavailable")
	}
	result, pending, err := s.fencer.UpdateOrigin(ctx, current, origin, input.ConfirmEnabledChannels)
	if err != nil {
		if isUniqueViolation(err) {
			return Provider{}, conflict("provider origin already exists")
		}
		return Provider{}, err
	}
	provider := toProvider(result)
	provider.RuntimeSyncPending = pending
	return provider, nil
}

func (s *Service) UpdateStatus(ctx context.Context, input UpdateStatusInput) (Provider, error) {
	if err := validateMutableStatus(strings.TrimSpace(input.Status)); err != nil {
		return Provider{}, err
	}
	current, err := s.getRow(ctx, input.ID)
	if err != nil {
		return Provider{}, err
	}
	if current.Status == StatusArchived {
		return Provider{}, conflict("archived provider status cannot be changed")
	}
	if input.ExpectedStatusRevision < 1 || input.ExpectedStatusRevision != current.StatusRevision {
		return Provider{}, conflict("provider status revision is stale")
	}
	if current.Status == input.Status {
		return toProvider(current), nil
	}
	if input.Status == StatusDisabled {
		enabled, countErr := s.store.CountEnabledChannelsByProvider(ctx, input.ID)
		if countErr != nil {
			return Provider{}, storeFailed(countErr, "count enabled provider channels")
		}
		if enabled > 0 {
			return Provider{}, conflict("disable enabled channels before disabling provider")
		}
	}
	if s.fencer == nil {
		return Provider{}, runtimeUnavailable("provider status runtime publisher is unavailable")
	}
	result, pending, err := s.fencer.UpdateStatus(ctx, current, input.Status)
	if err != nil {
		return Provider{}, err
	}
	provider := toProvider(result)
	provider.RuntimeSyncPending = pending
	return provider, nil
}

func (s *Service) Archive(ctx context.Context, id int64) (StatusChangeResult, error) {
	current, err := s.getRow(ctx, id)
	if err != nil {
		return StatusChangeResult{}, err
	}
	if current.Status == StatusArchived {
		return StatusChangeResult{}, notFound("provider not found or already archived")
	}
	channels, err := s.store.CountNonArchivedChannelsByProvider(ctx, id)
	if err != nil {
		return StatusChangeResult{}, storeFailed(err, "count provider channels")
	}
	if channels > 0 {
		return StatusChangeResult{}, conflict("archive all provider channels before archiving provider")
	}
	affected, err := s.store.ArchiveProvider(ctx, id)
	if err != nil {
		return StatusChangeResult{}, storeFailed(err, "archive provider")
	}
	if affected == 0 {
		return StatusChangeResult{}, conflict("provider has an in-flight routing operation")
	}
	pending := false
	if s.runtime != nil {
		pending = s.runtime.PurgeProviderRuntime(ctx, id) != nil
	}
	return StatusChangeResult{RuntimeSyncPending: pending}, nil
}

func (s *Service) Restore(ctx context.Context, id int64) (StatusChangeResult, error) {
	current, err := s.getRow(ctx, id)
	if err != nil {
		return StatusChangeResult{}, err
	}
	if current.Status != StatusArchived {
		return StatusChangeResult{}, notFound("provider not found or not archived")
	}
	affected, err := s.store.RestoreProvider(ctx, id)
	if err != nil {
		if isUniqueViolation(err) {
			return StatusChangeResult{}, conflict("provider origin is already in use")
		}
		return StatusChangeResult{}, storeFailed(err, "restore provider")
	}
	if affected == 0 {
		return StatusChangeResult{}, conflict("provider archived origin suffix is invalid")
	}
	updated, err := s.store.GetProvider(ctx, id)
	if err != nil {
		return StatusChangeResult{}, storeFailed(err, "get restored provider")
	}
	pending := false
	if s.runtime != nil {
		_, runtimeErr := s.runtime.InitProviderControl(ctx, id, updated.OriginRevision, updated.StatusRevision, updated.Status)
		pending = runtimeErr != nil
	}
	return StatusChangeResult{RuntimeSyncPending: pending}, nil
}

func (s *Service) Delete(ctx context.Context, id int64) error {
	current, err := s.getRow(ctx, id)
	if err != nil {
		return err
	}
	if current.Status != StatusArchived {
		return conflict("provider must be archived before deletion")
	}
	affected, err := s.store.DeleteProvider(ctx, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			return conflict("provider is still referenced by channels, history, or an in-flight operation")
		}
		return storeFailed(err, "delete provider")
	}
	if affected == 0 {
		return notFound("provider not found")
	}
	return nil
}

func NormalizeOrigin(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	parsed, err := url.Parse(trimmed)
	if trimmed == "" || err != nil {
		return "", invalidArgument("origin", "origin must be a valid URL")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", invalidArgument("origin", "origin must use http or https")
	}
	if parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", invalidArgument("origin", "origin must not contain userinfo, query, or fragment")
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return "", invalidArgument("origin", "origin must include a host")
	}
	port := parsed.Port()
	if (scheme == "http" && port == "80") || (scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		host = net.JoinHostPort(host, port)
	} else if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	path := strings.TrimRight(parsed.EscapedPath(), "/")
	return scheme + "://" + host + path, nil
}

func (s *Service) getRow(ctx context.Context, id int64) (sqlc.Provider, error) {
	if id <= 0 {
		return sqlc.Provider{}, invalidArgument("id", "provider id must be positive")
	}
	row, err := s.store.GetProvider(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.Provider{}, notFound("provider not found")
		}
		return sqlc.Provider{}, storeFailed(err, "get provider")
	}
	return row, nil
}

func toProvider(row sqlc.Provider) Provider {
	result := Provider{ID: row.ID, Slug: row.Slug, Name: row.Name, Origin: row.Origin, OriginRevision: row.OriginRevision, Status: row.Status, StatusRevision: row.StatusRevision, CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time}
	if row.ArchivedAt.Valid {
		archivedAt := row.ArchivedAt.Time
		result.ArchivedAt = &archivedAt
	}
	return result
}

func textParam(value string) pgtype.Text {
	value = strings.TrimSpace(value)
	return pgtype.Text{String: value, Valid: value != ""}
}

func validateMutableStatus(status string) error {
	if status != StatusEnabled && status != StatusDisabled {
		return invalidArgument("status", fmt.Sprintf("status must be %q or %q", StatusEnabled, StatusDisabled))
	}
	return nil
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
func runtimeUnavailable(message string) error {
	return failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage(message))
}
func storeFailed(cause error, message string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, cause, failure.WithMessage(message))
}
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}
func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
