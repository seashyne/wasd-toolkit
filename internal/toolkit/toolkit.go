package toolkit

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type Config struct {
	BaseURL        string            `json:"base_url"`
	TimeoutSeconds int               `json:"timeout_seconds"`
	Headers        map[string]string `json:"headers"`
	Auth           *AuthConfig       `json:"auth,omitempty"`
}

type AuthConfig struct {
	Path          string         `json:"path"`
	Method        string         `json:"method"`
	Payload       map[string]any `json:"payload"`
	TokenJSONPath string         `json:"token_json_path"`
}

func (a *AuthConfig) MethodOrDefault() string {
	if a.Method == "" {
		return http.MethodPost
	}
	return strings.ToUpper(a.Method)
}

type RequestSpec struct {
	Method      string
	Path        string
	Body        []byte
	ContentType string
	BearerToken string
}

type Client struct {
	baseURL string
	headers map[string]string
	http    *http.Client
}

type Scenario struct {
	Name  string         `json:"name"`
	Steps []ScenarioStep `json:"steps"`
}

type ScenarioStep struct {
	Name         string `json:"name"`
	Method       string `json:"method"`
	Path         string `json:"path"`
	Body         string `json:"body,omitempty"`
	ContentType  string `json:"content_type,omitempty"`
	Auth         bool   `json:"auth,omitempty"`
	ExpectStatus int    `json:"expect_status,omitempty"`
	Contains     string `json:"contains,omitempty"`
	SaveJSON     string `json:"save_json,omitempty"`
	SaveAs       string `json:"save_as,omitempty"`
}

type StepResult struct {
	Name        string `json:"name"`
	Passed      bool   `json:"passed"`
	StatusCode  int    `json:"status_code,omitempty"`
	DurationMS  int64  `json:"duration_ms"`
	Error       string `json:"error,omitempty"`
	BodySnippet string `json:"body_snippet,omitempty"`
}

type ScenarioReport struct {
	Name        string       `json:"name"`
	GeneratedAt string       `json:"generated_at"`
	Results     []StepResult `json:"results"`
}

type LoadReport struct {
	Requests    int     `json:"requests"`
	Concurrency int     `json:"concurrency"`
	Successes   int64   `json:"successes"`
	Failures    int64   `json:"failures"`
	P50MS       int64   `json:"p50_ms"`
	P95MS       int64   `json:"p95_ms"`
	P99MS       int64   `json:"p99_ms"`
	AverageMS   float64 `json:"average_ms"`
	FastestMS   int64   `json:"fastest_ms"`
	SlowestMS   int64   `json:"slowest_ms"`
	StartedAt   string  `json:"started_at"`
	CompletedAt string  `json:"completed_at"`
}

func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = []byte(expandEnv(string(raw)))
	var cfg Config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, err
	}
	if cfg.BaseURL == "" {
		return nil, errors.New("config.base_url is required")
	}
	if cfg.TimeoutSeconds <= 0 {
		cfg.TimeoutSeconds = 15
	}
	if cfg.Headers == nil {
		cfg.Headers = map[string]string{}
	}
	return &cfg, nil
}

func NewClient(cfg *Config) *Client {
	return &Client{
		baseURL: strings.TrimRight(cfg.BaseURL, "/"),
		headers: cfg.Headers,
		http:    &http.Client{Timeout: time.Duration(cfg.TimeoutSeconds) * time.Second},
	}
}

func (c *Client) Do(spec RequestSpec) (*http.Response, []byte, error) {
	var bodyReader io.Reader
	if len(spec.Body) > 0 {
		bodyReader = bytes.NewReader(spec.Body)
	}
	req, err := http.NewRequest(spec.Method, c.baseURL+spec.Path, bodyReader)
	if err != nil {
		return nil, nil, err
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	if spec.ContentType != "" {
		req.Header.Set("Content-Type", spec.ContentType)
	}
	if spec.BearerToken != "" {
		req.Header.Set("Authorization", "Bearer "+spec.BearerToken)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, err
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, nil, err
	}
	resp.Body = io.NopCloser(bytes.NewReader(body))
	return resp, body, nil
}

func LoadScenario(path string) (*Scenario, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	raw = []byte(expandEnv(string(raw)))
	var scenario Scenario
	if err := json.Unmarshal(raw, &scenario); err != nil {
		return nil, err
	}
	return &scenario, nil
}

func ExtractJSONPath(body []byte, path string) (string, error) {
	if path == "" {
		return "", errors.New("json path is required")
	}
	var data any
	if err := json.Unmarshal(body, &data); err != nil {
		return "", err
	}
	current := data
	for _, part := range strings.Split(strings.TrimPrefix(path, "$."), ".") {
		obj, ok := current.(map[string]any)
		if !ok {
			return "", fmt.Errorf("path %q not found", path)
		}
		current, ok = obj[part]
		if !ok {
			return "", fmt.Errorf("path %q not found", path)
		}
	}
	switch v := current.(type) {
	case string:
		return v, nil
	case float64, bool, int:
		return fmt.Sprint(v), nil
	default:
		encoded, _ := json.Marshal(v)
		return string(encoded), nil
	}
}

func ReplaceVars(input string, vars map[string]string) string {
	out := input
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{{"+k+"}}", v)
	}
	return out
}

func Snippet(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func RunLoadTest(client *Client, spec RequestSpec, requests int, concurrency int) (*LoadReport, error) {
	if requests <= 0 || concurrency <= 0 {
		return nil, errors.New("requests and concurrency must be > 0")
	}
	start := time.Now()
	jobs := make(chan int)
	latencies := make([]int64, 0, requests)
	var mux sync.Mutex
	var successes int64
	var failures int64
	var wg sync.WaitGroup

	worker := func() {
		defer wg.Done()
		for range jobs {
			t0 := time.Now()
			resp, _, err := client.Do(spec)
			latency := time.Since(t0).Milliseconds()
			mux.Lock()
			latencies = append(latencies, latency)
			mux.Unlock()
			if err != nil {
				atomic.AddInt64(&failures, 1)
				continue
			}
			_ = resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 400 {
				atomic.AddInt64(&successes, 1)
			} else {
				atomic.AddInt64(&failures, 1)
			}
		}
	}

	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go worker()
	}
	for i := 0; i < requests; i++ {
		jobs <- i
	}
	close(jobs)
	wg.Wait()

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var total int64
	for _, v := range latencies {
		total += v
	}
	fastest, slowest := int64(0), int64(0)
	if len(latencies) > 0 {
		fastest = latencies[0]
		slowest = latencies[len(latencies)-1]
	}
	report := &LoadReport{
		Requests:    requests,
		Concurrency: concurrency,
		Successes:   successes,
		Failures:    failures,
		P50MS:       percentile(latencies, 50),
		P95MS:       percentile(latencies, 95),
		P99MS:       percentile(latencies, 99),
		AverageMS:   float64(total) / float64(max(1, len(latencies))),
		FastestMS:   fastest,
		SlowestMS:   slowest,
		StartedAt:   start.Format(time.RFC3339),
		CompletedAt: time.Now().Format(time.RFC3339),
	}
	return report, nil
}

func percentile(values []int64, p int) int64 {
	if len(values) == 0 {
		return 0
	}
	idx := (len(values) - 1) * p / 100
	return values[idx]
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func expandEnv(input string) string {
	re := regexp.MustCompile(`\$\{([A-Za-z0-9_]+)\}`)
	return re.ReplaceAllStringFunc(input, func(match string) string {
		key := re.FindStringSubmatch(match)[1]
		return os.Getenv(key)
	})
}
