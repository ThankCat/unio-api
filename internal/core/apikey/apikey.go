package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

const (
	keyPrefix       = "unio_sk_"
	prefixRandomLen = 8
	// randomLen 是明文 key 随机部分的字符数；43 个 base62 字符约等于 256 bit 熵。
	randomLen = 43
	// base62Alphabet 是无符号、可安全展示的 key 字符集，避免 base64 的 - 和 _。
	base62Alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
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
	random, err := randomBase62(randomLen)
	if err != nil {
		return Key{}, err
	}

	plaintext := keyPrefix + random

	return Key{
		Plaintext: plaintext,
		Prefix:    Prefix(plaintext),
		Hash:      Hash(plaintext),
	}, nil
}

// randomBase62 生成 n 个均匀分布的 base62 字符。
// 用拒绝采样丢弃落在 256 % 62 余数区间的字节，避免取模偏置削弱熵。
func randomBase62(n int) (string, error) {
	const maxUnbiased = 256 - (256 % len(base62Alphabet))

	out := make([]byte, n)
	buf := make([]byte, n)
	filled := 0

	for filled < n {
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		for _, c := range buf {
			if int(c) >= maxUnbiased {
				continue
			}
			out[filled] = base62Alphabet[int(c)%len(base62Alphabet)]
			filled++
			if filled == n {
				break
			}
		}
	}

	return string(out), nil
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
