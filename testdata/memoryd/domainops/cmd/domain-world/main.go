package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/JayWanX/memory/testdata/memoryd/domainops/world"
)

const shutdownTimeout = 5 * time.Second

type configuration struct {
	role          string
	listen        string
	ledgerURL     string
	callbackURL   string
	eastURL       string
	westURL       string
	gatewayURL    string
	latency       time.Duration
	timeout       time.Duration
	stableKeys    bool
	retries       int
	route         string
	requestWindow time.Duration
}

func main() {
	config, err := parseConfiguration()
	if err != nil {
		log.Fatalf("domain-world configuration: %v", err)
	}
	handler, err := buildHandler(config)
	if err != nil {
		log.Fatalf("domain-world configuration: %v", err)
	}
	server := &http.Server{
		Addr:              config.listen,
		Handler:           handler,
		ReadHeaderTimeout: config.requestWindow,
		ReadTimeout:       config.requestWindow,
		WriteTimeout:      config.requestWindow,
		IdleTimeout:       30 * time.Second,
	}

	runContext, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	serveErrors := make(chan error, 1)
	go func() {
		serveErrors <- server.ListenAndServe()
	}()

	log.Printf("domain-world role=%s listening=%s", config.role, config.listen)
	select {
	case err = <-serveErrors:
		if !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("domain-world serve: %v", err)
		}
		return
	case <-runContext.Done():
	}

	shutdownContext, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownContext); err != nil {
		_ = server.Close()
		log.Fatalf("domain-world shutdown: %v", err)
	}
	if err := <-serveErrors; !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("domain-world serve: %v", err)
	}
}

func parseConfiguration() (configuration, error) {
	config := configuration{}
	flag.StringVar(&config.role, "role", environment("DOMAIN_ROLE", ""),
		"service role: ledger, callback, payment, gateway, or monitor")
	flag.StringVar(&config.listen, "listen", environment("DOMAIN_LISTEN", ":8080"),
		"HTTP listen address")
	flag.StringVar(&config.ledgerURL, "ledger-url", environment("LEDGER_URL", ""),
		"ledger service base URL")
	flag.StringVar(&config.callbackURL, "callback-url", environment("CALLBACK_URL", ""),
		"callback service base URL")
	flag.StringVar(&config.eastURL, "east-url", environment("EAST_PAYMENT_URL", ""),
		"east payment service base URL")
	flag.StringVar(&config.westURL, "west-url", environment("WEST_PAYMENT_URL", ""),
		"west payment service base URL")
	flag.StringVar(&config.gatewayURL, "gateway-url", environment("GATEWAY_URL", ""),
		"gateway service base URL")
	flag.DurationVar(&config.latency, "latency", environmentDuration("CALLBACK_LATENCY", 0),
		"callback processing latency")
	flag.DurationVar(&config.timeout, "timeout", environmentDuration("PAYMENT_TIMEOUT", time.Second),
		"payment callback timeout")
	flag.BoolVar(&config.stableKeys, "stable-keys", environmentBool("PAYMENT_STABLE_KEYS", true),
		"reuse an attempt key across payment retries")
	flag.IntVar(&config.retries, "retries", environmentInt("PAYMENT_RETRIES", 1),
		"bounded payment attempts")
	flag.StringVar(&config.route, "route", environment("GATEWAY_ROUTE", "east"),
		"initial gateway route")
	flag.DurationVar(&config.requestWindow, "request-window",
		environmentDuration("DOMAIN_REQUEST_WINDOW", 10*time.Second),
		"server request timeout")
	flag.Parse()

	if config.listen == "" || config.requestWindow < time.Second || config.requestWindow > time.Minute {
		return configuration{}, errors.New("listen and a request window between 1s and 1m are required")
	}
	return config, nil
}

func buildHandler(config configuration) (http.Handler, error) {
	switch config.role {
	case "ledger":
		return world.NewLedger().Handler(), nil
	case "callback":
		return buildCallback(config)
	case "payment":
		return buildPayment(config)
	case "gateway":
		return buildGateway(config)
	case "monitor":
		return buildMonitor(config)
	default:
		return nil, fmt.Errorf("unsupported role %q", config.role)
	}
}

func buildCallback(config configuration) (http.Handler, error) {
	if config.ledgerURL == "" || config.latency < 0 || config.latency > 5*time.Second {
		return nil, errors.New("callback requires ledger-url and latency between 0 and 5s")
	}
	return world.NewCallback(config.latency, config.ledgerURL).Handler(), nil
}

func buildPayment(config configuration) (http.Handler, error) {
	if config.callbackURL == "" || config.timeout < 50*time.Millisecond ||
		config.timeout > 5*time.Second || config.retries < 1 || config.retries > 4 {
		return nil, errors.New("payment requires callback-url, timeout from 50ms to 5s, and 1-4 retries")
	}
	paymentConfig := world.PaymentConfig{TimeoutMillis: config.timeout.Milliseconds(),
		StableKeys: config.stableKeys, Retries: config.retries}
	return world.NewPayment(paymentConfig, config.callbackURL).Handler(), nil
}

func buildGateway(config configuration) (http.Handler, error) {
	if config.eastURL == "" || config.westURL == "" ||
		(config.route != "east" && config.route != "west") {
		return nil, errors.New("gateway requires east-url, west-url, and route east or west")
	}
	return world.NewGateway(config.route, config.eastURL, config.westURL).Handler(), nil
}

func buildMonitor(config configuration) (http.Handler, error) {
	if config.gatewayURL == "" || config.ledgerURL == "" {
		return nil, errors.New("monitor requires gateway-url and ledger-url")
	}
	return world.NewMonitor(config.gatewayURL, config.ledgerURL).Handler(), nil
}

func environment(name, fallback string) string {
	if value, ok := os.LookupEnv(name); ok {
		return value
	}
	return fallback
}

func environmentDuration(name string, fallback time.Duration) time.Duration {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func environmentBool(name string, fallback bool) bool {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return fallback
	}
	return parsed
}

func environmentInt(name string, fallback int) int {
	value, ok := os.LookupEnv(name)
	if !ok {
		return fallback
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fallback
	}
	return parsed
}
