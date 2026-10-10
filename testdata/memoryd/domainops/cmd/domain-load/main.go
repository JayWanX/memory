package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/url"
	"os"
	"time"

	"github.com/JayWanX/memory/testdata/memoryd/domainops/world"
)

const (
	maxRequests = 100
	maxSettle   = 15 * time.Second
)

type summary struct {
	Prefix       string              `json:"prefix"`
	Sent         int                 `json:"sent"`
	Accepted     int                 `json:"accepted"`
	Failed       int                 `json:"failed"`
	Receipts     []receipt           `json:"receipts"`
	ElapsedMS    int64               `json:"elapsed_ms"`
	Observed     world.MonitorStatus `json:"observed"`
	ObservedAtMS int64               `json:"observed_at_ms"`
}

type receipt struct {
	BusinessID string `json:"business_id"`
	CaptureID  int64  `json:"capture_id"`
}

func main() {
	var gatewayURL string
	var monitorURL string
	var prefix string
	var count int
	var settle time.Duration
	var requestTimeout time.Duration
	flag.StringVar(&gatewayURL, "gateway-url", os.Getenv("GATEWAY_URL"),
		"gateway service base URL")
	flag.StringVar(&monitorURL, "monitor-url", os.Getenv("MONITOR_URL"),
		"monitor service base URL")
	flag.StringVar(&prefix, "prefix", "probe", "bounded business ID prefix")
	flag.IntVar(&count, "count", 4, "number of unique checkout requests")
	flag.DurationVar(&settle, "settle", 2*time.Second,
		"bounded delay before observing eventual side effects")
	flag.DurationVar(&requestTimeout, "request-timeout", 6*time.Second,
		"timeout for each checkout and observation")
	flag.Parse()

	if gatewayURL == "" || monitorURL == "" || !world.ValidToken(prefix) {
		log.Fatal("domain-load requires gateway-url, monitor-url, and a valid prefix")
	}
	if count < 1 || count > maxRequests || settle < 0 || settle > maxSettle ||
		requestTimeout < time.Second || requestTimeout > 30*time.Second {
		log.Fatal("domain-load bounds violated")
	}

	client := world.DefaultClient(requestTimeout)
	result := summary{Prefix: prefix, Sent: count}
	started := time.Now()
	for index := 1; index <= count; index++ {
		businessID := fmt.Sprintf("%s-%03d", prefix, index)
		ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
		var checkout world.CheckoutResponse
		err := world.PostJSON(ctx, client, gatewayURL+"/checkout",
			world.PayRequest{BusinessID: businessID}, &checkout)
		cancel()
		if err != nil {
			result.Failed++
			continue
		}
		result.Accepted++
		result.Receipts = append(result.Receipts, receipt{BusinessID: businessID,
			CaptureID: checkout.CaptureID})
	}
	result.ElapsedMS = time.Since(started).Milliseconds()
	if err := wait(context.Background(), settle); err != nil {
		log.Fatalf("domain-load settle: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	statusTarget := monitorURL + "/status?prefix=" + url.QueryEscape(prefix+"-")
	err := world.GetJSON(ctx, client, statusTarget, &result.Observed)
	cancel()
	if err != nil {
		log.Fatalf("domain-load observe: %v", err)
	}
	result.ObservedAtMS = time.Since(started).Milliseconds()
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(result); err != nil {
		log.Fatalf("domain-load output: %v", err)
	}
}

func wait(parent context.Context, duration time.Duration) error {
	if parent == nil {
		return errors.New("parent context is required")
	}
	if duration == 0 {
		return nil
	}
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-parent.Done():
		return parent.Err()
	case <-timer.C:
		return nil
	}
}
