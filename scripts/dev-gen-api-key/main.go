//go:build ignore

package main

import (
	"fmt"
	"os"

	"github.com/ThankCat/unio-api/internal/core/apikey"
)

func main() {
	key, err := apikey.Generate()
	if err != nil {
		fmt.Fprintf(os.Stderr, "generate api key: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("%s\t%s\t%s\n", key.Plaintext, key.Prefix, key.Hash)
}
