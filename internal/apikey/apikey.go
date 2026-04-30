package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
)

const (
	keyPrefix       = "unio_sk_"
	prefixRandomLen = 8
)

// Key 表示一次新生成的 API Key。
// Plaintext 只在创建时返回给用户，不能保存到数据库或写入日志。
type Key struct {
	// Plaintext 是完整明文 key，只能在创建时展示一次。
	// 示例格式：unio_sk_<random>
	Plaintext string

	// Prefix 是可安全展示的短前缀，用于识别 key。
	// 示例格式：unio_sk_<前 8 位 random>
	Prefix string

	// Hash 是明文 key 的哈希值，用于数据库存储和认证匹配。
	// 示例格式：64 位十六进制字符串。
	Hash string
}

// Generate 生成一个新的高熵 API Key，并返回明文、展示前缀和哈希值。
func Generate() (Key, error) {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		return Key{}, err
	}

	random := base64.RawURLEncoding.EncodeToString(b[:])
	plaintext := keyPrefix + random

	return Key{
		Plaintext: plaintext,
		Prefix:    Prefix(plaintext),
		Hash:      Hash(plaintext),
	}, nil
}

// Prefix 返回 API Key 的安全展示前缀，用于后台识别和日志排查。
func Prefix(plaintext string) string {
	prefixLength := len(keyPrefix) + prefixRandomLen

	if len(plaintext) <= prefixLength {
		return plaintext
	}

	return plaintext[:prefixLength]
}

// Hash 返回 API Key 的 SHA-256 哈希值，用于数据库持久化。
func Hash(plaintext string) string {
	sum := sha256.Sum256([]byte(plaintext))
	return hex.EncodeToString(sum[:])
}

// Verify 使用常量时间比较验证明文 API Key 是否匹配哈希值。
func Verify(plaintext string, hash string) bool {
	got := Hash(plaintext)
	return subtle.ConstantTimeCompare([]byte(got), []byte(hash)) == 1
}
