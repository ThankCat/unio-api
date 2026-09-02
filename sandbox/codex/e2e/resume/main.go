package main

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

func main() {
	client := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6380"})
	store := breakerstore.NewStore(client, "unio:dev")
	if err := store.ResumeAccountUsage(context.Background(), 169); err != nil {
		fmt.Println("resume:", err)
		return
	}
	fmt.Println("account 169 usage pause cleared")
}
