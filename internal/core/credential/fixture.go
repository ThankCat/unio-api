package credential

import "bytes"

// FixedTestMasterKeyBase64 是固定测试 master key 的 base64 表示，仅用于测试夹具。
const FixedTestMasterKeyBase64 = "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE="

var fixedTestMasterKey = bytes.Repeat([]byte{0x01}, masterKeySize)

// EncryptFixedTestCredential 用固定测试 master key 加密上游凭据，供跨包测试夹具使用。
func EncryptFixedTestCredential(plaintext string) ([]byte, error) {
	c, err := NewAESGCMCipher(fixedTestMasterKey)
	if err != nil {
		return nil, err
	}

	return c.Encrypt(plaintext)
}
