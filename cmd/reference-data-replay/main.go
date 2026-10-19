package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/code-sena/design-software-reference-data-service/internal/adapter/out/messaging"
)

func main() {
	limit := flag.Int("limit", 10, "maximum failed messages to replay (1-1000)")
	flag.Parse()
	url := os.Getenv("REFERENCE_DATA_AMQP_URL")
	if url == "" {
		log.Fatal("REFERENCE_DATA_AMQP_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	count, err := messaging.ReplayDLQ(ctx, url, *limit)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Printf("Replayed %d messages; event IDs preserved.\n", count)
}
