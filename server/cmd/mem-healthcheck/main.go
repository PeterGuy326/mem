// Command mem-healthcheck probes memd from inside its minimal runtime image.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

func main() {
	target := strings.TrimSpace(os.Getenv("MEM_HEALTHCHECK_URL"))
	if target == "" {
		target = "http://127.0.0.1:8080/readyz"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid healthcheck URL")
		os.Exit(1)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		fmt.Fprintln(os.Stderr, "memd healthcheck request failed")
		os.Exit(1)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		fmt.Fprintln(os.Stderr, "memd healthcheck returned a non-success status")
		os.Exit(1)
	}
}
