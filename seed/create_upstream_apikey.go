package main

import (
	"encoding/hex"
	"fmt"
	"os"

	"github.com/ThankCat/unio-api/internal/core/credential"
)

func main() {
	c, err := credential.NewAESGCMCipher(mustParseKey())
	if err != nil {
		panic(err)
	}
	encrypted, err := c.Encrypt(os.Getenv("DEEPSEEK_API_KEY"))
	if err != nil {
		panic(err)
	}
	fmt.Println("\\x" + hex.EncodeToString(encrypted))
}
func mustParseKey() []byte {
	key, err := credential.ParseMasterKey(os.Getenv("CREDENTIAL_MASTER_KEY"))
	if err != nil {
		panic(err)
	}
	return key
}
