package capability

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	core "github.com/ThankCat/unio-gateway/internal/core/capability"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

var capabilityKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9_]*(\.[a-z][a-z0-9_]*)+$`)

// CreateCapabilityKeyInput 是新增能力 key 字典行的入参。
type CreateCapabilityKeyInput struct {
	Key           string
	Domain        string
	DisplayName   string
	Description   string
	SortOrder     int32
	Deprecated    bool
	ProtocolScope string
}

// UpdateCapabilityKeyInput 是更新能力 key 字典元数据的入参。
type UpdateCapabilityKeyInput struct {
	Key           string
	Domain        string
	DisplayName   string
	Description   string
	SortOrder     int32
	Deprecated    bool
	ProtocolScope string
}

// GetKey 读取单条能力 key 字典。
func (s *CapabilityService) GetKey(ctx context.Context, key string) (core.CapabilityKey, error) {
	k, err := normalizeDictionaryKey(key)
	if err != nil {
		return core.CapabilityKey{}, err
	}
	return s.store.GetCapabilityKey(ctx, k)
}

// CreateKey 新增能力 key 字典行。
func (s *CapabilityService) CreateKey(ctx context.Context, in CreateCapabilityKeyInput) (core.CapabilityKey, error) {
	params, err := validateCreateCapabilityKey(in)
	if err != nil {
		return core.CapabilityKey{}, err
	}
	item, err := s.store.CreateCapabilityKey(ctx, params)
	if err != nil {
		if isUniqueViolation(err) {
			return core.CapabilityKey{}, dictionaryConflict("capability key already exists")
		}
		return core.CapabilityKey{}, err
	}
	return item, nil
}

// UpdateKey 更新能力 key 字典元数据（key 本身不可改）。
func (s *CapabilityService) UpdateKey(ctx context.Context, in UpdateCapabilityKeyInput) (core.CapabilityKey, error) {
	params, err := validateUpdateCapabilityKey(in)
	if err != nil {
		return core.CapabilityKey{}, err
	}
	item, err := s.store.UpdateCapabilityKey(ctx, params)
	if err != nil {
		return core.CapabilityKey{}, err
	}
	return item, nil
}

// DeleteKey 删除能力 key 字典行；被 model_capabilities 引用时返回 conflict。
func (s *CapabilityService) DeleteKey(ctx context.Context, key string) error {
	k, err := normalizeDictionaryKey(key)
	if err != nil {
		return err
	}
	if err := s.store.DeleteCapabilityKey(ctx, k); err != nil {
		if isForeignKeyViolation(err) {
			return dictionaryConflict("capability key is referenced by model_capabilities; deprecate instead")
		}
		return err
	}
	return nil
}

func validateCreateCapabilityKey(in CreateCapabilityKeyInput) (core.CreateCapabilityKeyParams, error) {
	key, err := normalizeDictionaryKey(in.Key)
	if err != nil {
		return core.CreateCapabilityKeyParams{}, err
	}
	domain := strings.TrimSpace(in.Domain)
	if domain == "" {
		return core.CreateCapabilityKeyParams{}, invalidArgument("domain", "domain is required")
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		return core.CreateCapabilityKeyParams{}, invalidArgument("display_name", "display_name is required")
	}
	description := strings.TrimSpace(in.Description)
	scope := core.NormalizeProtocolScope(in.ProtocolScope)
	if !core.IsValidProtocolScope(scope) {
		return core.CreateCapabilityKeyParams{}, invalidArgument("protocol_scope", "protocol_scope must be shared, openai or anthropic")
	}
	return core.CreateCapabilityKeyParams{
		Key:           key,
		Domain:        domain,
		DisplayName:   displayName,
		Description:   description,
		SortOrder:     in.SortOrder,
		Deprecated:    in.Deprecated,
		ProtocolScope: scope,
	}, nil
}

func validateUpdateCapabilityKey(in UpdateCapabilityKeyInput) (core.UpdateCapabilityKeyParams, error) {
	key, err := normalizeDictionaryKey(in.Key)
	if err != nil {
		return core.UpdateCapabilityKeyParams{}, err
	}
	domain := strings.TrimSpace(in.Domain)
	if domain == "" {
		return core.UpdateCapabilityKeyParams{}, invalidArgument("domain", "domain is required")
	}
	displayName := strings.TrimSpace(in.DisplayName)
	if displayName == "" {
		return core.UpdateCapabilityKeyParams{}, invalidArgument("display_name", "display_name is required")
	}
	description := strings.TrimSpace(in.Description)
	scope := core.NormalizeProtocolScope(in.ProtocolScope)
	if !core.IsValidProtocolScope(scope) {
		return core.UpdateCapabilityKeyParams{}, invalidArgument("protocol_scope", "protocol_scope must be shared, openai or anthropic")
	}
	return core.UpdateCapabilityKeyParams{
		Key:           key,
		Domain:        domain,
		DisplayName:   displayName,
		Description:   description,
		SortOrder:     in.SortOrder,
		Deprecated:    in.Deprecated,
		ProtocolScope: scope,
	}, nil
}

func normalizeDictionaryKey(raw string) (core.Key, error) {
	key := core.Key(strings.TrimSpace(raw))
	if key == "" {
		return "", invalidArgument("key", "key is required")
	}
	if !capabilityKeyPattern.MatchString(string(key)) {
		return "", invalidArgument("key", "key must match ^[a-z][a-z0-9_]*(\\.[a-z][a-z0-9_]*)+$")
	}
	return key, nil
}

func dictionaryConflict(message string) error {
	return failure.New(failure.CodeAdminConflict, failure.WithMessage(message))
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func isForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23503"
}
