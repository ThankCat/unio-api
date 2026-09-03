// Package proxy 管理出站代理实体（网关中心 · 代理）。
//
// 代理是被账号与渠道复用的出口资产：URL 在写入时规范化（凭据转义）落到 url 列，
// 运行时（候选/出站/探测查询）只 JOIN 读 url——热路径不拼串。
// 删除有 FK RESTRICT 护栏：被引用的代理不可物理删除；停用则引用方回退直连（管理页提示）。
package proxy

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// Queries 是代理管理所需的存储能力。
type Queries interface {
	AdminListProxies(ctx context.Context) ([]sqlc.AdminListProxiesRow, error)
	AdminGetProxy(ctx context.Context, id int64) (sqlc.Proxy, error)
	AdminCreateProxy(ctx context.Context, arg sqlc.AdminCreateProxyParams) (sqlc.Proxy, error)
	AdminUpdateProxy(ctx context.Context, arg sqlc.AdminUpdateProxyParams) (sqlc.Proxy, error)
	AdminSetProxyStatus(ctx context.Context, arg sqlc.AdminSetProxyStatusParams) (sqlc.Proxy, error)
	AdminDeleteProxy(ctx context.Context, id int64) (int64, error)
	AdminCountProxyReferences(ctx context.Context, id int64) (sqlc.AdminCountProxyReferencesRow, error)
}

// Service 编排代理 CRUD。
type Service struct {
	queries Queries
}

// NewService 创建代理管理服务。
func NewService(queries Queries) *Service {
	return &Service{queries: queries}
}

// Proxy 是代理管理视图。密码只回掩码：列表/编辑不需要明文回显，改密码就是重新输入。
type Proxy struct {
	ID          int64  `json:"id"`
	Name        string `json:"name"`
	Protocol    string `json:"protocol"`
	Host        string `json:"host"`
	Port        int32  `json:"port"`
	Username    string `json:"username,omitempty"`
	HasPassword bool   `json:"has_password"`
	// URL 是脱敏后的展示 URL（凭据掩码）；出站用的完整 URL 只在库里。
	URL         string `json:"url"`
	Status      string `json:"status"`
	Note        string `json:"note,omitempty"`
	ChannelRefs int64  `json:"channel_refs"`
	AccountRefs int64  `json:"account_refs"`
	CreatedAt   string `json:"created_at"`
	UpdatedAt   string `json:"updated_at"`
}

// Input 是创建/更新代理的入参。
type Input struct {
	ID       int64
	Name     string
	Protocol string
	Host     string
	Port     int32
	Username string
	// Password 空串在更新时表示「保持不变」；创建时表示无密码。
	Password string
	Note     string
}

// List 列出全部代理（含引用计数）。
func (s *Service) List(ctx context.Context) ([]Proxy, error) {
	rows, err := s.queries.AdminListProxies(ctx)
	if err != nil {
		return nil, storeFailed(err, "list proxies")
	}
	out := make([]Proxy, 0, len(rows))
	for _, row := range rows {
		out = append(out, Proxy{
			ID:          row.ID,
			Name:        row.Name,
			Protocol:    row.Protocol,
			Host:        row.Host,
			Port:        row.Port,
			Username:    textOr(row.Username),
			HasPassword: row.Password.Valid && row.Password.String != "",
			URL:         maskedURL(row.Protocol, row.Host, row.Port, textOr(row.Username), row.Password.Valid && row.Password.String != ""),
			Status:      row.Status,
			Note:        textOr(row.Note),
			ChannelRefs: row.ChannelRefs,
			AccountRefs: row.AccountRefs,
			CreatedAt:   row.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
			UpdatedAt:   row.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
		})
	}
	return out, nil
}

// Create 创建代理（URL 规范化后一并落库）。
func (s *Service) Create(ctx context.Context, in Input) (Proxy, error) {
	if err := validateInput(in, false); err != nil {
		return Proxy{}, err
	}
	row, err := s.queries.AdminCreateProxy(ctx, sqlc.AdminCreateProxyParams{
		Name:     strings.TrimSpace(in.Name),
		Protocol: in.Protocol,
		Host:     strings.TrimSpace(in.Host),
		Port:     in.Port,
		Username: optText(in.Username),
		Password: optText(in.Password),
		Url:      canonicalURL(in.Protocol, in.Host, in.Port, in.Username, in.Password),
		Status:   "enabled",
		Note:     optText(in.Note),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Proxy{}, conflict("代理名称已存在")
		}
		return Proxy{}, storeFailed(err, "create proxy")
	}
	return s.byID(ctx, row.ID)
}

// Update 更新代理（密码空串 = 保持不变）。
func (s *Service) Update(ctx context.Context, in Input) (Proxy, error) {
	if in.ID <= 0 {
		return Proxy{}, invalidArgument("id", "proxy id must be positive")
	}
	if err := validateInput(in, true); err != nil {
		return Proxy{}, err
	}
	current, err := s.queries.AdminGetProxy(ctx, in.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Proxy{}, notFound("proxy not found")
		}
		return Proxy{}, storeFailed(err, "get proxy")
	}
	password := in.Password
	if password == "" && current.Password.Valid {
		password = current.Password.String
	}
	row, err := s.queries.AdminUpdateProxy(ctx, sqlc.AdminUpdateProxyParams{
		ID:       in.ID,
		Name:     strings.TrimSpace(in.Name),
		Protocol: in.Protocol,
		Host:     strings.TrimSpace(in.Host),
		Port:     in.Port,
		Username: optText(in.Username),
		Password: optText(password),
		Url:      canonicalURL(in.Protocol, in.Host, in.Port, in.Username, password),
		Note:     optText(in.Note),
	})
	if err != nil {
		if isUniqueViolation(err) {
			return Proxy{}, conflict("代理名称已存在")
		}
		return Proxy{}, storeFailed(err, "update proxy")
	}
	return s.byID(ctx, row.ID)
}

// SetStatus 启停代理。停用后引用它的账号/渠道回退直连（出站 JOIN 只认 enabled）。
func (s *Service) SetStatus(ctx context.Context, id int64, status string) (Proxy, error) {
	if status != "enabled" && status != "disabled" {
		return Proxy{}, invalidArgument("status", "status must be enabled or disabled")
	}
	if _, err := s.queries.AdminSetProxyStatus(ctx, sqlc.AdminSetProxyStatusParams{ID: id, Status: status}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Proxy{}, notFound("proxy not found")
		}
		return Proxy{}, storeFailed(err, "set proxy status")
	}
	return s.byID(ctx, id)
}

// Delete 物理删除；被引用（FK RESTRICT）降级 409 提示先解除引用或改停用。
func (s *Service) Delete(ctx context.Context, id int64) error {
	affected, err := s.queries.AdminDeleteProxy(ctx, id)
	if err != nil {
		if isForeignKeyViolation(err) {
			refs, refErr := s.queries.AdminCountProxyReferences(ctx, id)
			if refErr == nil {
				return conflict("代理正被 " + strconv.FormatInt(refs.ChannelRefs, 10) + " 条渠道、" +
					strconv.FormatInt(refs.AccountRefs, 10) + " 个账号引用；先解除引用，或改用停用（引用方回退直连）")
			}
			return conflict("代理正被渠道/账号引用；先解除引用，或改用停用")
		}
		return storeFailed(err, "delete proxy")
	}
	if affected == 0 {
		return notFound("proxy not found")
	}
	return nil
}

func (s *Service) byID(ctx context.Context, id int64) (Proxy, error) {
	rows, err := s.queries.AdminListProxies(ctx)
	if err != nil {
		return Proxy{}, storeFailed(err, "list proxies")
	}
	for _, row := range rows {
		if row.ID == id {
			return Proxy{
				ID:          row.ID,
				Name:        row.Name,
				Protocol:    row.Protocol,
				Host:        row.Host,
				Port:        row.Port,
				Username:    textOr(row.Username),
				HasPassword: row.Password.Valid && row.Password.String != "",
				URL:         maskedURL(row.Protocol, row.Host, row.Port, textOr(row.Username), row.Password.Valid && row.Password.String != ""),
				Status:      row.Status,
				Note:        textOr(row.Note),
				ChannelRefs: row.ChannelRefs,
				AccountRefs: row.AccountRefs,
				CreatedAt:   row.CreatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
				UpdatedAt:   row.UpdatedAt.Time.UTC().Format("2006-01-02T15:04:05Z07:00"),
			}, nil
		}
	}
	return Proxy{}, notFound("proxy not found")
}

func validateInput(in Input, isUpdate bool) error {
	if strings.TrimSpace(in.Name) == "" {
		return invalidArgument("name", "代理名称不能为空")
	}
	switch in.Protocol {
	case "http", "https", "socks5":
	default:
		return invalidArgument("protocol", "协议只支持 http / https / socks5")
	}
	if strings.TrimSpace(in.Host) == "" {
		return invalidArgument("host", "主机不能为空")
	}
	if in.Port <= 0 || in.Port > 65535 {
		return invalidArgument("port", "端口须在 1~65535")
	}
	if in.Password != "" && strings.TrimSpace(in.Username) == "" {
		return invalidArgument("username", "设置了密码就必须有用户名")
	}
	_ = isUpdate
	return nil
}

// canonicalURL 组装出站用的规范代理 URL；凭据经 URL 转义，特殊字符安全。
func canonicalURL(protocol, host string, port int32, username, password string) string {
	u := &url.URL{Scheme: protocol, Host: strings.TrimSpace(host) + ":" + strconv.Itoa(int(port))}
	if strings.TrimSpace(username) != "" {
		if password != "" {
			u.User = url.UserPassword(strings.TrimSpace(username), password)
		} else {
			u.User = url.User(strings.TrimSpace(username))
		}
	}
	return u.String()
}

// maskedURL 是管理页展示用的脱敏 URL（密码恒掩码）。
func maskedURL(protocol, host string, port int32, username string, hasPassword bool) string {
	auth := ""
	if username != "" {
		auth = username
		if hasPassword {
			auth += ":****"
		}
		auth += "@"
	}
	return protocol + "://" + auth + host + ":" + strconv.Itoa(int(port))
}

func textOr(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

func optText(v string) pgtype.Text {
	trimmed := strings.TrimSpace(v)
	if trimmed == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: trimmed, Valid: true}
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument,
		failure.WithMessage(message), failure.WithField("field", field))
}

func conflict(message string) error {
	return failure.New(failure.CodeAdminConflict, failure.WithMessage(message))
}

func notFound(message string) error {
	return failure.New(failure.CodeAdminNotFound, failure.WithMessage(message))
}

func storeFailed(err error, operation string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(operation))
}
