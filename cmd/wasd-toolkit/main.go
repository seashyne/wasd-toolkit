package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/seashyne/wasd-toolkit/internal/toolkit"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "health":
		runHealth(os.Args[2:])
	case "login":
		runLogin(os.Args[2:])
	case "call":
		runCall(os.Args[2:])
	case "scenario":
		runScenario(os.Args[2:])
	case "load":
		runLoad(os.Args[2:])
	case "version", "--version", "-v":
		fmt.Println("wasd-toolkit dev")
	case "help", "--help", "-h":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n", os.Args[1])
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Print(`WASD Toolkit - backend API smoke/load testing CLI

Usage:
  wasd-toolkit <command> [options]

Commands:
  health    Check service health endpoint
  login     Login and persist bearer token to .wasd-token
  call      Perform a single API call with assertions
  scenario  Run a JSON scenario file
  load      Run a lightweight concurrent smoke/load test

Examples:
  wasd-toolkit health -config examples/config.local.json -contains ok
  wasd-toolkit login -config examples/config.local.json
  wasd-toolkit call -config examples/config.local.json -method GET -path /v1/profile -expect-status 200 -auth
  wasd-toolkit scenario -config examples/config.local.json -file examples/scenario.basic.json
  wasd-toolkit load -config examples/config.local.json -path /health -requests 200 -concurrency 20
`)
}

func loadConfig(configPath string) (*toolkit.Config, *toolkit.Client, error) {
	if configPath == "" {
		return nil, nil, errors.New("-config is required")
	}
	cfg, err := toolkit.LoadConfig(configPath)
	if err != nil {
		return nil, nil, err
	}
	client := toolkit.NewClient(cfg)
	return cfg, client, nil
}

func runHealth(args []string) {
	fs := flag.NewFlagSet("health", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config JSON")
	path := fs.String("path", "/health", "Health endpoint path")
	contains := fs.String("contains", "", "Required substring in body")
	fs.Parse(args)

	_, client, err := loadConfig(*configPath)
	must(err)

	resp, body, err := client.Do(toolkit.RequestSpec{Method: http.MethodGet, Path: *path})
	must(err)
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		fail("health check failed with status %d body=%s", resp.StatusCode, string(body))
	}
	if *contains != "" && !strings.Contains(strings.ToLower(string(body)), strings.ToLower(*contains)) {
		fail("health check body did not contain %q. body=%s", *contains, string(body))
	}

	fmt.Printf("OK health status=%d body=%s\n", resp.StatusCode, strings.TrimSpace(string(body)))
}

func runLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config JSON")
	fs.Parse(args)

	cfg, client, err := loadConfig(*configPath)
	must(err)
	if cfg.Auth == nil {
		fail("config.auth is required for login")
	}

	payload, err := json.Marshal(cfg.Auth.Payload)
	must(err)

	spec := toolkit.RequestSpec{
		Method:      cfg.Auth.MethodOrDefault(),
		Path:        cfg.Auth.Path,
		Body:        payload,
		ContentType: "application/json",
	}

	resp, body, err := client.Do(spec)
	must(err)
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		fail("login failed with status %d body=%s", resp.StatusCode, string(body))
	}

	token, err := toolkit.ExtractJSONPath(body, cfg.Auth.TokenJSONPath)
	must(err)
	if token == "" {
		fail("token not found at json path %q", cfg.Auth.TokenJSONPath)
	}

	tokenFile := tokenFilePath(*configPath)
	must(os.WriteFile(tokenFile, []byte(token), 0o600))
	fmt.Printf("OK login token saved to %s\n", tokenFile)
}

func runCall(args []string) {
	fs := flag.NewFlagSet("call", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config JSON")
	method := fs.String("method", http.MethodGet, "HTTP method")
	path := fs.String("path", "/", "API path")
	body := fs.String("body", "", "Raw JSON body")
	auth := fs.Bool("auth", false, "Attach bearer token from .wasd-token")
	expectStatus := fs.Int("expect-status", 200, "Expected HTTP status")
	contains := fs.String("contains", "", "Required substring in response body")
	fs.Parse(args)

	_, client, err := loadConfig(*configPath)
	must(err)

	spec := toolkit.RequestSpec{Method: strings.ToUpper(*method), Path: *path}
	if *body != "" {
		spec.Body = []byte(*body)
		spec.ContentType = "application/json"
	}
	if *auth {
		token, err := os.ReadFile(tokenFilePath(*configPath))
		must(err)
		spec.BearerToken = strings.TrimSpace(string(token))
	}

	resp, respBody, err := client.Do(spec)
	must(err)
	defer resp.Body.Close()

	if resp.StatusCode != *expectStatus {
		fail("unexpected status=%d expected=%d body=%s", resp.StatusCode, *expectStatus, string(respBody))
	}
	if *contains != "" && !strings.Contains(strings.ToLower(string(respBody)), strings.ToLower(*contains)) {
		fail("response body did not contain %q. body=%s", *contains, string(respBody))
	}

	fmt.Printf("OK call status=%d\n%s\n", resp.StatusCode, string(respBody))
}

func runScenario(args []string) {
	fs := flag.NewFlagSet("scenario", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config JSON")
	file := fs.String("file", "", "Path to scenario JSON")
	fs.Parse(args)

	cfg, client, err := loadConfig(*configPath)
	must(err)
	if *file == "" {
		fail("-file is required")
	}

	scenario, err := toolkit.LoadScenario(*file)
	must(err)

	tokenBytes, _ := os.ReadFile(tokenFilePath(*configPath))
	token := strings.TrimSpace(string(tokenBytes))

	vars := map[string]string{"base_url": cfg.BaseURL, "token": token}
	results := make([]toolkit.StepResult, 0, len(scenario.Steps))

	for _, step := range scenario.Steps {
		body := toolkit.ReplaceVars(step.Body, vars)
		spec := toolkit.RequestSpec{
			Method:      strings.ToUpper(step.Method),
			Path:        toolkit.ReplaceVars(step.Path, vars),
			Body:        []byte(body),
			ContentType: step.ContentType,
		}
		if step.Auth {
			spec.BearerToken = token
		}
		start := time.Now()
		resp, respBody, err := client.Do(spec)
		duration := time.Since(start)
		result := toolkit.StepResult{Name: step.Name, DurationMS: duration.Milliseconds()}
		if err != nil {
			result.Passed = false
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		_ = resp.Body.Close()
		result.StatusCode = resp.StatusCode
		result.BodySnippet = toolkit.Snippet(string(respBody), 300)
		result.Passed = true
		if step.ExpectStatus != 0 && resp.StatusCode != step.ExpectStatus {
			result.Passed = false
			result.Error = fmt.Sprintf("status=%d expected=%d", resp.StatusCode, step.ExpectStatus)
		}
		if step.Contains != "" && !strings.Contains(strings.ToLower(string(respBody)), strings.ToLower(step.Contains)) {
			result.Passed = false
			result.Error = fmt.Sprintf("body missing substring %q", step.Contains)
		}
		if step.SaveJSON != "" {
			value, err := toolkit.ExtractJSONPath(respBody, step.SaveJSON)
			if err == nil && value != "" {
				vars[strings.TrimPrefix(step.SaveAs, "$.")] = value
				vars[step.SaveAs] = value
			}
		}
		results = append(results, result)
	}

	report := toolkit.ScenarioReport{Name: scenario.Name, GeneratedAt: time.Now().Format(time.RFC3339), Results: results}
	out, err := json.MarshalIndent(report, "", "  ")
	must(err)
	fmt.Println(string(out))

	for _, r := range results {
		if !r.Passed {
			os.Exit(2)
		}
	}
}

func runLoad(args []string) {
	fs := flag.NewFlagSet("load", flag.ExitOnError)
	configPath := fs.String("config", "", "Path to config JSON")
	method := fs.String("method", http.MethodGet, "HTTP method")
	path := fs.String("path", "/health", "API path")
	requests := fs.Int("requests", 100, "Total requests")
	concurrency := fs.Int("concurrency", 10, "Concurrent workers")
	auth := fs.Bool("auth", false, "Attach bearer token from .wasd-token")
	body := fs.String("body", "", "Raw JSON body")
	fs.Parse(args)

	_, client, err := loadConfig(*configPath)
	must(err)

	spec := toolkit.RequestSpec{Method: strings.ToUpper(*method), Path: *path}
	if *body != "" {
		spec.Body = []byte(*body)
		spec.ContentType = "application/json"
	}
	if *auth {
		token, err := os.ReadFile(tokenFilePath(*configPath))
		must(err)
		spec.BearerToken = strings.TrimSpace(string(token))
	}

	report, err := toolkit.RunLoadTest(client, spec, *requests, *concurrency)
	must(err)

	out, err := json.MarshalIndent(report, "", "  ")
	must(err)
	fmt.Println(string(out))
	if report.Failures > 0 {
		os.Exit(2)
	}
}

func tokenFilePath(configPath string) string {
	return filepath.Join(filepath.Dir(configPath), ".wasd-token")
}

func must(err error) {
	if err != nil {
		fail("%v", err)
	}
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "ERROR: "+format+"\n", args...)
	os.Exit(1)
}

func _unused() {
	_, _, _, _, _ = bytes.NewBuffer(nil), json.NewEncoder(io.Discard), errors.New(""), fmt.Sprintf(""), strings.Builder{}
}
