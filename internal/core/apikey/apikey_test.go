package apikey_test

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/apikey"
)

func TestGenerate(t *testing.T) {
	key, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	if key.Plaintext == "" || key.Prefix == "" || key.Hash == "" {
		t.Fatal("expected plaintext, prefix, and hash to be non-empty")
	}

	if !strings.HasPrefix(key.Plaintext, "sk-unio-") {
		t.Fatal("expected plaintext to start with sk-unio-")
	}

	if len(key.Prefix) >= len(key.Plaintext) {
		t.Fatal("expected prefix to be shorter than plaintext")
	}

	if apikey.Hash(key.Plaintext) == key.Plaintext {
		t.Fatal("expected hash to differ from plaintext")
	}

	if apikey.Hash(key.Plaintext) != key.Hash {
		t.Fatal("expected generated key hash to match its plaintext")
	}
}

// 明文长度是对外承诺的一部分：调用方按 32 字符设计输入框和存储字段。
func TestGeneratePlaintextLength(t *testing.T) {
	key, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	if len(key.Plaintext) != apikey.MaxPlaintextLen {
		t.Fatalf(
			"expected plaintext length %d, got %d",
			apikey.MaxPlaintextLen,
			len(key.Plaintext),
		)
	}
}

func TestGeneratePlaintextIsLowercaseBase36(t *testing.T) {
	key, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate api key: %v", err)
	}

	random := strings.TrimPrefix(key.Plaintext, "sk-unio-")
	if random == key.Plaintext {
		t.Fatal("expected plaintext to start with sk-unio-")
	}

	for _, c := range random {
		isDigit := c >= '0' && c <= '9'
		isLower := c >= 'a' && c <= 'z'
		if !isDigit && !isLower {
			t.Fatalf("expected only lowercase base36 chars in random part, got %q", c)
		}
	}
}

// 拒绝采样如果写错，最容易的表现是字符集尾部（这里是 y、z）永远取不到。
// 单次生成看不出来，跑够样本才能暴露。
func TestGenerateCoversWholeAlphabet(t *testing.T) {
	seen := make(map[rune]bool)

	for range 400 {
		key, err := apikey.Generate()
		if err != nil {
			t.Fatalf("generate api key: %v", err)
		}
		for _, c := range strings.TrimPrefix(key.Plaintext, "sk-unio-") {
			seen[c] = true
		}
	}

	for _, c := range "0123456789abcdefghijklmnopqrstuvwxyz" {
		if !seen[c] {
			t.Fatalf("expected %q to appear across generated keys", c)
		}
	}
}

func TestGenerateUniqueKeys(t *testing.T) {
	key1, _ := apikey.Generate()
	key2, _ := apikey.Generate()

	if key1 == key2 {
		t.Fatal("expected generated keys to be unique")
	}
}

func TestHashWrongKey(t *testing.T) {
	key, _ := apikey.Generate()
	if apikey.Hash("something") == key.Hash {
		t.Fatal("expected wrong key hash to differ")
	}
}

func TestPrefixShortPlaintext(t *testing.T) {
	if apikey.Prefix("abc") != "abc" {
		t.Fatal("expected short plaintext prefix to be returned unchanged")
	}
}

// 旧格式 key 的 hash 算法不变（无盐 SHA-256 hex），认证按 hash 查表仍要命中；只是明文再也拿不回来。
func TestHashLegacyFormatKeyIsStable(t *testing.T) {
	const legacy = "unio_sk_XhE8wL5DqR2mNvT7cB4jY9kZ6pA3sF1u"

	sum := sha256.Sum256([]byte(legacy))
	if got := apikey.Hash(legacy); got != hex.EncodeToString(sum[:]) {
		t.Fatalf("legacy-format key hash = %s, want unsalted sha256 hex", got)
	}
}

// 尾段是掩码展示的一半依据，它必须真的来自明文末尾，且不能把整串泄露出去。
func TestGenerateExposesOnlyTheLastFourChars(t *testing.T) {
	key, err := apikey.Generate()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	if len(key.Suffix) != 4 {
		t.Fatalf("suffix = %q, want 4 chars", key.Suffix)
	}
	if !strings.HasSuffix(key.Plaintext, key.Suffix) {
		t.Fatalf("suffix %q is not the tail of %q", key.Suffix, key.Plaintext)
	}
	// 前缀 8 位 + 尾段 4 位之外的部分绝不能被这两个字段拼出来。
	if len(key.Prefix)+len(key.Suffix) >= len(key.Plaintext) {
		t.Fatalf("prefix+suffix must stay shorter than the plaintext")
	}
}
