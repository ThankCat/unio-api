// Package adminsession 在 Redis 上实现 admin 控制台的登录会话。
//
// 单管理员极简版：账号口令是唯一登录凭证，登录成功后由服务端现场签发随机会话 token，
// 此后全部 admin 端点凭该 token 认证。不做 JWT——引入 RBAC 时再评估无状态令牌。
//
// 选择服务端存储而非签名令牌的理由是可吊销：改口令、登出、疑似泄露都能立即让 token 失效，
// 而签名令牌在过期前无法撤回。Redis 是 admin-server 已有的依赖，不新增基础设施。
//
// Redis 里只保存 token 的 SHA-256 摘要而非原文：Redis 快照或备份泄露不会直接产出可用 token。
package adminsession

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// tokenRandomBytes 是会话 token 的随机字节数；32 字节即 256 bit 熵，hex 编码后 64 字符。
const tokenRandomBytes = 32

// Store 读写 Redis 上的 admin 会话。
type Store struct {
	client    redis.Cmdable
	keyPrefix string
	ttl       time.Duration
}

// NewStore 创建会话存储。keyNamespace 与其余运行态共用 REDIS_KEY_NAMESPACE。
func NewStore(client redis.Cmdable, keyNamespace string, ttl time.Duration) *Store {
	if client == nil {
		panic("adminsession: redis client is required")
	}

	keyNamespace = strings.Trim(keyNamespace, ":")
	if keyNamespace == "" {
		keyNamespace = "unio"
	}

	return &Store{
		client:    client,
		keyPrefix: keyNamespace + ":admin:session:",
		ttl:       ttl,
	}
}

// TTL 返回会话有效期，供调用方回传给客户端做过期提示。
func (s *Store) TTL() time.Duration {
	return s.ttl
}

// key 由 token 摘要构成；调用方持有的原文 token 不落 Redis。
func (s *Store) key(token string) string {
	sum := sha256.Sum256([]byte(token))
	return s.keyPrefix + hex.EncodeToString(sum[:])
}

// Issue 签发一个新会话并返回 token 原文。原文只在此刻返回一次，之后无法从 Redis 反查。
func (s *Store) Issue(ctx context.Context) (string, error) {
	var b [tokenRandomBytes]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", failure.Wrap(
			failure.CodeAdminSessionStoreFailed,
			err,
			failure.WithMessage("generate admin session token"),
		)
	}

	token := hex.EncodeToString(b[:])

	// 值本身不承载权限信息：单管理员模式下「key 存在」即代表会话有效。
	// 存签发时间只为排障，认证路径不解析它。
	if err := s.client.Set(ctx, s.key(token), time.Now().UTC().Format(time.RFC3339), s.ttl).Err(); err != nil {
		return "", failure.Wrap(
			failure.CodeAdminSessionStoreFailed,
			err,
			failure.WithMessage("persist admin session"),
		)
	}

	return token, nil
}

// Validate 判断 token 是否对应一个有效会话。
//
// Redis 不可用时返回错误而不是 false：认证失败与依赖故障必须可区分，
// 否则一次 Redis 抖动会被渲染成「登录已过期」，把管理员误导去反复重登。
func (s *Store) Validate(ctx context.Context, token string) (bool, error) {
	if token == "" {
		return false, nil
	}

	n, err := s.client.Exists(ctx, s.key(token)).Result()
	if err != nil {
		return false, failure.Wrap(
			failure.CodeAdminSessionStoreFailed,
			err,
			failure.WithMessage("read admin session"),
		)
	}

	return n > 0, nil
}

// Revoke 立即失效一个会话；token 不存在时按幂等处理，不报错。
func (s *Store) Revoke(ctx context.Context, token string) error {
	if token == "" {
		return nil
	}

	if err := s.client.Del(ctx, s.key(token)).Err(); err != nil {
		return failure.Wrap(
			failure.CodeAdminSessionStoreFailed,
			err,
			failure.WithMessage("revoke admin session"),
		)
	}

	return nil
}
