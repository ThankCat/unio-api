package bootstrap

import (
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/core/credential"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

const defaultChatRouteTimeout = 30 * time.Second

// NewChatRouter 创建当前 server 进程使用的 chat routing 组件。
func NewChatRouter(store routing.Store, masterKeyEncoded string) (*routing.Router, error) {
	if strings.TrimSpace(masterKeyEncoded) == "" {
		return nil, failure.New(
			failure.CodeConfigMissing,
			failure.WithMessage("CREDENTIAL_MASTER_KEY is required"),
		)
	}

	key, err := credential.ParseMasterKey(masterKeyEncoded)
	if err != nil {
		return nil, err
	}

	cipher, err := credential.NewAESGCMCipher(key)
	if err != nil {
		return nil, err
	}

	return routing.NewRouter(store, cipher, defaultChatRouteTimeout), nil
}
