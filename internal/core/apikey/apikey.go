package apikey

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
)

const (
	// keyPrefix 用连字符分隔，与 OpenAI 的 sk-proj- 同形；用户在 SDK 配置里
	// 两家 key 并排放时不会因为分隔符不同而显得是另一套体系。
	//
	// 换掉下划线不影响存量 key：认证只比对 key_hash，从不解析前缀。
	// 老 key 的明文与 key_prefix 仍是 sk_unio_ 开头，展示层同时认这两种命名空间。
	keyPrefix       = "sk-unio-"
	prefixRandomLen = 8
	// suffixLen 是掩码展示时露出的尾部位数。
	// 加上 8 位前缀共暴露 12 位，明文仍余 44 位随机（约 227 bit 熵）。
	suffixLen = 4
	// randomLen 是明文 key 随机部分的字符数。
	//
	// 取 48 是为了和 OpenAI 传统 key（sk- 后跟 48 字符）的随机段等长：用户往 SDK 里
	// 粘贴时，两家 key 在输入框里占的宽度一致，不会因为我们的明显偏短而显得像被截断了。
	// 48 个 base36 字符约 248 bit 熵，远超碰撞与暴力枚举所需。
	randomLen = 48
	// base36Alphabet 只含小写字母和数字：明文全小写，复制粘贴时不会因大小写误读。
	// 随机段本身不含 - 与 _，因此整串 key 里唯一的连字符就是命名空间分隔符，
	// 按 - 切分永远只得到 sk / unio / random 三段。
	base36Alphabet = "0123456789abcdefghijklmnopqrstuvwxyz"
	// MaxPlaintextLen 是明文 key 的总长上限，供调用方做防御性校验。
	// = len(keyPrefix) + randomLen。
	MaxPlaintextLen = 56
)

// Key 表示一次新生成的 API Key。
// Plaintext 只在创建响应里返回一次，既不落库也不写日志——之后任何接口都拿不回明文。
type Key struct {
	// Plaintext 是完整明文 key，只能在创建时展示一次。
	// 示例格式：sk-unio-<48 位小写随机>
	Plaintext string

	// Prefix 是可安全展示的短前缀，明文丢弃后靠它识别是哪一把 key。
	// 示例格式：sk-unio-<前 8 位 random>
	Prefix string

	// Suffix 是明文末 4 位，仅供掩码展示拼出尾段（sk-unio-xxxx···abcd）。
	// 尾部往往比前缀更容易被记住——SDK 报错与日志截断通常留的是尾巴。
	Suffix string

	// Hash 是明文 key 的哈希值，用于数据库存储和认证匹配。
	// 示例格式：64 位十六进制字符串。
	Hash string
}

// Generate 生成一个新的高熵 API Key，并返回明文、展示前缀和哈希值。
func Generate() (Key, error) {
	random, err := randomBase36(randomLen)
	if err != nil {
		return Key{}, err
	}

	plaintext := keyPrefix + random

	return Key{
		Plaintext: plaintext,
		Prefix:    Prefix(plaintext),
		Suffix:    Suffix(plaintext),
		Hash:      Hash(plaintext),
	}, nil
}

// randomBase36 生成 n 个均匀分布的 base36 字符。
// 用拒绝采样丢弃落在 256 % 36 余数区间的字节，避免取模偏置削弱熵。
func randomBase36(n int) (string, error) {
	const maxUnbiased = 256 - (256 % len(base36Alphabet))

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
			out[filled] = base36Alphabet[int(c)%len(base36Alphabet)]
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

// Suffix 返回明文末 4 位。明文短于 4 位时原样返回（只可能出现在构造异常的入参上）。
func Suffix(plaintext string) string {
	if len(plaintext) <= suffixLen {
		return plaintext
	}

	return plaintext[len(plaintext)-suffixLen:]
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
