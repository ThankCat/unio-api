package main

import (
	"fmt"

	"github.com/ThankCat/unio-api/internal/core/apikey"
)

func main() {
	k, err := apikey.Generate()
	if err != nil {
		panic(err)
	}
	fmt.Println("PLAIN:", k.Plaintext)
	fmt.Println("PREFIX:", k.Prefix)
	fmt.Println("HASH:", k.Hash)
}
