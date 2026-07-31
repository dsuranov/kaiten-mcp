// Package config validates environment and .env based runtime configuration.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"math"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	defaultPrefix       = "/api/v1"
	defaultRateRPS      = 3.0
	defaultCacheTTL     = 60 * time.Second
	defaultConcurrency  = 5
	defaultTimeout      = 20 * time.Second
	defaultMCPTransport = "stdio"
	defaultMCPHost      = "127.0.0.1"
	defaultMCPPort      = 8000
	defaultMCPPath      = "/mcp"
)

// Config is the validated process configuration shared by both binaries.
type Config struct {
	Token                  string
	BaseURL                *url.URL
	APIPrefix              string
	RateLimitRPS           float64
	CacheTTL               time.Duration
	MaxConcurrency         int
	Timeout                time.Duration
	EnableWriteTools       bool
	MCPTransport           string
	MCPHost                string
	MCPPort                int
	MCPPath                string
	FlightRecorder         bool
	FlightRecorderMinAge   time.Duration
	FlightRecorderMaxBytes int64
	DotEnvPath             string
}

// Overrides are command-line transport settings. Zero values mean no override.
type Overrides struct {
	Transport string
	Host      string
	Port      int
	Path      string
}

// Load reads process variables and at most one .env file. The .env search order
// is current working directory, then the executable directory.
func Load(requireCredentials bool, overrides Overrides) (Config, error) {
	values := processEnvironment()
	dotEnvPath := ""
	for _, candidate := range dotEnvCandidates() {
		if candidate == "" {
			continue
		}
		parsed, err := readDotEnv(candidate)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return Config{}, fmt.Errorf("load configuration file: %w", err)
		}
		dotEnvPath = candidate
		for key, value := range parsed {
			if _, set := values[key]; !set {
				values[key] = value
			}
		}
		break
	}

	token := firstNonEmpty(values["KAITEN_API_TOKEN"], values["KAITEN_TOKEN"])
	baseRaw := firstNonEmpty(values["KAITEN_URL"], values["KAITEN_BASE_URL"])
	if requireCredentials && token == "" {
		return Config{}, errors.New("KAITEN_API_TOKEN is required")
	}
	var base *url.URL
	if baseRaw != "" {
		var err error
		base, err = validateTenantURL(baseRaw)
		if err != nil {
			return Config{}, err
		}
	} else if requireCredentials {
		return Config{}, errors.New("KAITEN_URL is required")
	}

	prefix := normalizePrefix(valueOr(values, "KAITEN_API_PREFIX", defaultPrefix))
	rateRaw := values["KAITEN_RATE_LIMIT_RPS"]
	if _, set := values["KAITEN_RATE_LIMIT_RPS"]; !set {
		rateRaw = values["KAITEN_RATE_LIMIT"]
	}
	rate, err := positiveFloat("KAITEN_RATE_LIMIT_RPS", rateRaw, defaultRateRPS)
	if err != nil {
		return Config{}, err
	}
	ttl, err := nonNegativeFloat("KAITEN_CACHE_TTL_SECONDS", values["KAITEN_CACHE_TTL_SECONDS"], defaultCacheTTL.Seconds())
	if err != nil {
		return Config{}, err
	}
	concurrency, err := boundedInt("KAITEN_MAX_CONCURRENCY", values["KAITEN_MAX_CONCURRENCY"], defaultConcurrency, 1, math.MaxInt32)
	if err != nil {
		return Config{}, err
	}
	timeout, err := positiveFloat("KAITEN_TIMEOUT_SECONDS", values["KAITEN_TIMEOUT_SECONDS"], defaultTimeout.Seconds())
	if err != nil {
		return Config{}, err
	}
	transport := valueOr(values, "KAITEN_MCP_TRANSPORT", defaultMCPTransport)
	host := valueOr(values, "KAITEN_MCP_HOST", defaultMCPHost)
	port, err := boundedInt("KAITEN_MCP_PORT", values["KAITEN_MCP_PORT"], defaultMCPPort, 1, 65535)
	if err != nil {
		return Config{}, err
	}
	path := normalizeMCPPath(valueOr(values, "KAITEN_MCP_STREAMABLE_HTTP_PATH", defaultMCPPath))
	if overrides.Transport != "" {
		transport = overrides.Transport
	}
	if overrides.Host != "" {
		host = overrides.Host
	}
	if overrides.Port != 0 {
		if overrides.Port < 1 || overrides.Port > 65535 {
			return Config{}, errors.New("--port must be between 1 and 65535")
		}
		port = overrides.Port
	}
	if overrides.Path != "" {
		path = normalizeMCPPath(overrides.Path)
	}
	if transport != "stdio" && transport != "streamable-http" {
		return Config{}, errors.New("MCP transport must be stdio or streamable-http")
	}
	if strings.TrimSpace(host) == "" {
		host = defaultMCPHost
	}
	minAge, err := nonNegativeFloat("KAITEN_TRACE_FLIGHT_RECORDER_MIN_AGE_SECONDS", values["KAITEN_TRACE_FLIGHT_RECORDER_MIN_AGE_SECONDS"], 5)
	if err != nil {
		return Config{}, err
	}
	maxBytes, err := positiveInt64("KAITEN_TRACE_FLIGHT_RECORDER_MAX_BYTES", values["KAITEN_TRACE_FLIGHT_RECORDER_MAX_BYTES"], 8388608)
	if err != nil {
		return Config{}, err
	}

	return Config{
		Token: token, BaseURL: base, APIPrefix: prefix,
		RateLimitRPS: rate, CacheTTL: time.Duration(ttl * float64(time.Second)),
		MaxConcurrency: concurrency, Timeout: time.Duration(timeout * float64(time.Second)),
		EnableWriteTools: truth(values["KAITEN_ENABLE_WRITE_TOOLS"]),
		MCPTransport:     transport, MCPHost: host, MCPPort: port, MCPPath: path,
		FlightRecorder:         truth(values["KAITEN_TRACE_FLIGHT_RECORDER"]),
		FlightRecorderMinAge:   time.Duration(minAge * float64(time.Second)),
		FlightRecorderMaxBytes: maxBytes, DotEnvPath: dotEnvPath,
	}, nil
}

func processEnvironment() map[string]string {
	out := make(map[string]string)
	for _, entry := range os.Environ() {
		key, value, ok := strings.Cut(entry, "=")
		if ok {
			out[key] = value
		}
	}
	return out
}

func dotEnvCandidates() []string {
	var result []string
	if cwd, err := os.Getwd(); err == nil {
		result = append(result, filepath.Join(cwd, ".env"))
	}
	if executable, err := os.Executable(); err == nil {
		candidate := filepath.Join(filepath.Dir(executable), ".env")
		if len(result) == 0 || candidate != result[0] {
			result = append(result, candidate)
		}
	}
	return result
}

func readDotEnv(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	out := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("invalid line in %s", path)
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if len(value) >= 2 && ((value[0] == '\'' && value[len(value)-1] == '\'') || (value[0] == '"' && value[len(value)-1] == '"')) {
			value = value[1 : len(value)-1]
		}
		out[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func validateTenantURL(raw string) (*url.URL, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u == nil || !u.IsAbs() || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("KAITEN_URL must be an absolute http or https URL")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, errors.New("KAITEN_URL must not contain credentials, query, or fragment")
	}
	u.Path = strings.TrimRight(u.Path, "/")
	return u, nil
}

func normalizePrefix(value string) string {
	value = strings.Trim(value, " /")
	if value == "" {
		return "/"
	}
	return "/" + value
}

func normalizeMCPPath(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return defaultMCPPath
	}
	if !strings.HasPrefix(value, "/") {
		return "/" + value
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func valueOr(values map[string]string, key, fallback string) string {
	if value, ok := values[key]; ok && strings.TrimSpace(value) != "" {
		return strings.TrimSpace(value)
	}
	return fallback
}

func truth(value string) bool { return strings.EqualFold(strings.TrimSpace(value), "true") }

func positiveFloat(name, raw string, fallback float64) (float64, error) {
	value, err := nonNegativeFloat(name, raw, fallback)
	if err != nil || value <= 0 {
		if err == nil {
			err = fmt.Errorf("%s must be a positive finite number", name)
		}
		return 0, err
	}
	return value, nil
}

func nonNegativeFloat(name, raw string, fallback float64) (float64, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0, fmt.Errorf("%s must be a non-negative finite number", name)
	}
	return value, nil
}

func boundedInt(name, raw string, fallback, min, max int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || value < min || value > max {
		return 0, fmt.Errorf("%s must be an integer from %d through %d", name, min, max)
	}
	return value, nil
}

func positiveInt64(name, raw string, fallback int64) (int64, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}
	value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || value <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", name)
	}
	return value, nil
}
