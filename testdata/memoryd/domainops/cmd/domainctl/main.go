package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/JayWanX/memory/testdata/memoryd/domainops/world"
)

const (
	maxActionRequestBytes   = world.MaxRequestBodyBytes
	maxControlResponseBytes = world.MaxResponseBodyBytes
)

type configuration struct {
	role     string
	endpoint string
	timeout  time.Duration
	args     []string
}

func main() {
	config, err := parseConfiguration()
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), config.timeout)
	defer cancel()

	response, err := execute(ctx, config)
	if err != nil {
		log.Fatalf("domainctl %s: %v", config.role, err)
	}
	if err := render(config.role, response); err != nil {
		log.Fatalf("domainctl %s: %v", config.role, err)
	}
}

func parseConfiguration() (configuration, error) {
	return parseConfigurationArgs(os.Args[1:], os.Getenv)
}

func parseConfigurationArgs(arguments []string, getenv func(string) string) (configuration, error) {
	if getenv == nil {
		return configuration{}, errors.New("domainctl environment reader is required")
	}
	options, operands, err := splitClosedOptions(arguments)
	if err != nil {
		return configuration{}, err
	}
	config := configuration{}
	flags := flag.NewFlagSet("domainctl", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.StringVar(&config.role, "role", getenv("DOMAIN_ROLE"), "local domain role label")
	flags.StringVar(&config.endpoint, "endpoint", getenv("DOMAIN_ENDPOINT"),
		"base URL for the local domain service")
	flags.DurationVar(&config.timeout, "timeout", 5*time.Second, "bounded request timeout")
	if err := flags.Parse(options); err != nil {
		return configuration{}, fmt.Errorf("domainctl options: %w", err)
	}
	if config.role == "" || config.endpoint == "" || config.timeout < time.Second ||
		config.timeout > 30*time.Second {
		return configuration{}, errors.New(
			"domainctl requires role, endpoint, and timeout between 1s and 30s")
	}
	if strings.ContainsAny(config.role, "\r\n\t") {
		return configuration{}, errors.New("domainctl role is invalid")
	}
	config.args = operands
	if len(config.args) == 0 {
		usage()
	}
	return config, nil
}

func splitClosedOptions(arguments []string) ([]string, []string, error) {
	known := map[string]struct{}{"role": {}, "endpoint": {}, "timeout": {}}
	seen := make(map[string]struct{}, len(known))
	options := make([]string, 0, len(known))
	operands := make([]string, 0, len(arguments))
	for index := 0; index < len(arguments); index++ {
		token := arguments[index]
		trimmed := strings.TrimLeft(token, "-")
		name, value, inline := strings.Cut(trimmed, "=")
		_, recognized := known[name]
		if token == trimmed || !recognized {
			if strings.HasPrefix(token, "-") {
				return nil, nil, fmt.Errorf("domainctl option %q is not supported", token)
			}
			operands = append(operands, token)
			continue
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, nil, fmt.Errorf("domainctl option %q is repeated", name)
		}
		seen[name] = struct{}{}
		if !inline {
			index++
			if index >= len(arguments) {
				return nil, nil, fmt.Errorf("domainctl option %q requires a value", name)
			}
			value = arguments[index]
		}
		options = append(options, "--"+name+"="+value)
	}
	return options, operands, nil
}

func execute(ctx context.Context, config configuration) (json.RawMessage, error) {
	arguments := config.args
	var response json.RawMessage
	var err error
	switch arguments[0] {
	case "status":
		if len(arguments) > 2 {
			usage()
		}
		path := "/status"
		if len(arguments) == 2 {
			path += "?prefix=" + url.QueryEscape(arguments[1])
		}
		response, err = request(ctx, http.MethodGet, config.endpoint, path, nil)
	case "read":
		if len(arguments) != 2 {
			usage()
		}
		response, err = request(ctx, http.MethodGet, config.endpoint, arguments[1], nil)
	case "probe":
		if len(arguments) != 1 {
			usage()
		}
		response, err = request(ctx, http.MethodPost, config.endpoint, "/probe", []byte("{}"))
	case "action":
		if len(arguments) != 3 {
			usage()
		}
		payload := []byte(arguments[2])
		if !json.Valid(payload) || len(payload) > maxActionRequestBytes {
			return nil, errors.New("action body must be bounded valid JSON")
		}
		response, err = request(ctx, http.MethodPost, config.endpoint, arguments[1], payload)
	default:
		usage()
	}
	if err != nil {
		return nil, err
	}
	return response, nil
}

func render(role string, response json.RawMessage) error {
	var output any
	if err := json.Unmarshal(response, &output); err != nil {
		return fmt.Errorf("invalid JSON response: %w", err)
	}
	encoded, err := json.Marshal(map[string]any{"role": role, "result": output})
	if err != nil {
		return fmt.Errorf("encode response: %w", err)
	}
	fmt.Println(string(encoded))
	return nil
}

func request(ctx context.Context, method, endpoint, path string, payload []byte) (json.RawMessage, error) {
	if ctx == nil {
		return nil, errors.New("context is required")
	}
	target, err := resolve(endpoint, path)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	httpRequest, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	if payload != nil {
		httpRequest.Header.Set("Content-Type", "application/json")
	}
	response, err := http.DefaultClient.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("send request: %w", err)
	}
	defer response.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(response.Body, maxControlResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}
	if len(responseBody) > maxControlResponseBytes {
		return nil, errors.New("response exceeds control bound")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, fmt.Errorf("response status %d: %s", response.StatusCode,
			strings.TrimSpace(string(responseBody)))
	}
	if !json.Valid(responseBody) {
		return nil, errors.New("response is not JSON")
	}
	return json.RawMessage(responseBody), nil
}

func resolve(endpoint, path string) (string, error) {
	base, err := url.Parse(endpoint)
	if err != nil || base.Scheme == "" || base.Host == "" {
		return "", errors.New("endpoint must be an absolute HTTP URL")
	}
	reference, err := url.Parse(path)
	if err != nil || !strings.HasPrefix(path, "/") || reference.IsAbs() || reference.Host != "" {
		return "", errors.New("path must be an absolute-path reference")
	}
	if reference.Path != "/status" && reference.Path != "/history" &&
		reference.Path != "/probe" &&
		!strings.HasPrefix(reference.Path, "/admin/") && reference.Path != "/charges" {
		return "", errors.New("path is outside the status, history, probe, charges, and admin surfaces")
	}
	return base.ResolveReference(reference).String(), nil
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: domainctl [flags] status [prefix]")
	fmt.Fprintln(os.Stderr, "       domainctl [flags] read /status|/history[?query]|/charges[?query]")
	fmt.Fprintln(os.Stderr, "       domainctl [flags] probe")
	fmt.Fprintln(os.Stderr, "       domainctl [flags] action /admin/<action> '<json>'")
	os.Exit(2)
}
