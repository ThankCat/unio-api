package credential

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

const masterKeySize = 32

var (
	// ErrMasterKeyInvalid 表示 master key 不是合法的 32 字节 AES-256 key。
	ErrMasterKeyInvalid = errors.New("credential master key invalid")
	// ErrPlaintextEmpty 表示待加密的上游凭据为空。
	ErrPlaintextEmpty = errors.New("credential plaintext empty")
	// ErrCiphertextInvalid 表示密文长度不足以包含 nonce。
	ErrCiphertextInvalid = errors.New("credential ciphertext invalid")
)

// Cipher 负责上游凭据的加密与解密；明文绝不持久化、绝不进日志。
type Cipher interface {
	Encrypt(plaintext string) ([]byte, error)
	Decrypt(ciphertext []byte) (string, error)
}

// AESGCMCipher 使用 AES-256-GCM 加密上游凭据；密文格式为 nonce‖ciphertext‖tag。
type AESGCMCipher struct {
	gcm cipher.AEAD
}

// NewAESGCMCipher 创建 AES-GCM 凭据加密器；key 必须是 32 字节。
func NewAESGCMCipher(key []byte) (*AESGCMCipher, error) {
	if len(key) != masterKeySize {
		return nil, failure.Wrap(
			failure.CodeCredentialMasterKeyInvalid,
			ErrMasterKeyInvalid,
			failure.WithMessage(fmt.Sprintf("credential master key must be %d bytes", masterKeySize)),
		)
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeCredentialMasterKeyInvalid,
			ErrMasterKeyInvalid,
			failure.WithMessage("create aes cipher"),
		)
	}

	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeCredentialMasterKeyInvalid,
			ErrMasterKeyInvalid,
			failure.WithMessage("create aes gcm"),
		)
	}

	return &AESGCMCipher{gcm: gcm}, nil
}

func (c *AESGCMCipher) Encrypt(plaintext string) ([]byte, error) {
	if strings.TrimSpace(plaintext) == "" {
		return nil, failure.Wrap(
			failure.CodeCredentialEncryptFailed,
			ErrPlaintextEmpty,
			failure.WithMessage(ErrPlaintextEmpty.Error()),
		)
	}

	nonce := make([]byte, c.gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, failure.Wrap(
			failure.CodeCredentialEncryptFailed,
			err,
			failure.WithMessage("generate credential encryption nonce"),
		)
	}

	return c.gcm.Seal(nonce, nonce, []byte(plaintext), nil), nil
}

func (c *AESGCMCipher) Decrypt(data []byte) (string, error) {
	nonceSize := c.gcm.NonceSize()
	if len(data) < nonceSize {
		return "", failure.Wrap(
			failure.CodeCredentialCiphertextInvalid,
			ErrCiphertextInvalid,
			failure.WithMessage(ErrCiphertextInvalid.Error()),
		)
	}

	nonce, ct := data[:nonceSize], data[nonceSize:]
	plaintext, err := c.gcm.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", failure.Wrap(
			failure.CodeCredentialDecryptFailed,
			err,
			failure.WithMessage("decrypt channel credential"),
		)
	}

	return string(plaintext), nil
}

// ParseMasterKey 把 base64 编码的 CREDENTIAL_MASTER_KEY 解析成 32 字节 AES-256 key。
func ParseMasterKey(encoded string) ([]byte, error) {
	encoded = strings.TrimSpace(encoded)
	if encoded == "" {
		return nil, failure.Wrap(
			failure.CodeCredentialMasterKeyInvalid,
			ErrMasterKeyInvalid,
			failure.WithMessage("credential master key is empty"),
		)
	}

	key, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeCredentialMasterKeyInvalid,
			ErrMasterKeyInvalid,
			failure.WithMessage("credential master key is not valid base64"),
		)
	}

	if len(key) != masterKeySize {
		return nil, failure.Wrap(
			failure.CodeCredentialMasterKeyInvalid,
			ErrMasterKeyInvalid,
			failure.WithMessage(fmt.Sprintf("credential master key must be %d bytes", masterKeySize)),
		)
	}

	return key, nil
}
