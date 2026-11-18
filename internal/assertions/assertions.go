// Package assertions executes developer-authored HTTP checks and JSON assertions.
package assertions

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/REinstall-SyS/ReleaseProof/internal/config"
)

const MaxResponseBytes = 1 << 20

type Variables map[string]any

type Assertion struct {
	Pointer  string `json:"pointer"`
	Expected any    `json:"expected"`
	Actual   any    `json:"actual"`
	Passed   bool   `json:"passed"`
	Error    string `json:"error,omitempty"`
}

type Result struct {
	Name       string      `json:"name"`
	App        string      `json:"app"`
	Method     string      `json:"method"`
	Path       string      `json:"path"`
	Status     int         `json:"status"`
	Body       string      `json:"body,omitempty"`
	DurationMS int64       `json:"duration_ms"`
	Assertions []Assertion `json:"assertions"`
	Error      string      `json:"error,omitempty"`
}

var token = regexp.MustCompile(`\{\{([a-z][a-z0-9_-]*)\}\}`)

// Run sends exactly one request. Assertions never retry writes or follow redirects.
// Captures are published only after every assertion for this step succeeds.
func Run(ctx context.Context, client *http.Client, baseURL string, step config.Step, vars Variables) (result Result, runErr error) {
	started := time.Now()
	result = Result{Name: step.Name, App: step.App, Method: step.Method, Path: step.Path, Assertions: []Assertion{}}
	defer func() {
		result.DurationMS = time.Since(started).Milliseconds()
		if runErr != nil {
			result.Error = runErr.Error()
		}
	}()
	path, err := resolveString(step.Path, vars, true)
	if err != nil {
		return result, err
	}
	result.Path = path
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") || strings.ContainsAny(path, "\r\n\\#") {
		return result, errors.New("request path must be an absolute local HTTP path")
	}
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil {
		return result, errors.New("invalid application base URL")
	}
	var body io.Reader
	if step.Body != nil {
		resolved, err := Resolve(step.Body, vars)
		if err != nil {
			return result, fmt.Errorf("request body: %w", err)
		}
		data, err := json.Marshal(resolved)
		if err != nil {
			return result, fmt.Errorf("request JSON: %w", err)
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, step.Method, strings.TrimRight(baseURL, "/")+path, body)
	if err != nil {
		return result, fmt.Errorf("create HTTP request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	isolatedClient := *client
	isolatedClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	response, err := isolatedClient.Do(req)
	if err != nil {
		return result, fmt.Errorf("%s %s: %w", step.Method, path, err)
	}
	defer response.Body.Close()
	result.Status = response.StatusCode
	data, err := io.ReadAll(io.LimitReader(response.Body, MaxResponseBytes+1))
	if err != nil {
		return result, fmt.Errorf("read HTTP response: %w", err)
	}
	if len(data) > MaxResponseBytes {
		result.Body = string(data[:MaxResponseBytes])
		return result, fmt.Errorf("HTTP response exceeds %d bytes", MaxResponseBytes)
	}
	result.Body = string(data)
	status := Assertion{Pointer: "HTTP status", Expected: step.Expect.Status, Actual: response.StatusCode, Passed: response.StatusCode == step.Expect.Status}
	result.Assertions = append(result.Assertions, status)
	var failures []error
	if !status.Passed {
		failures = append(failures, fmt.Errorf("%s %s returned HTTP %d; expected %d", step.Method, path, response.StatusCode, step.Expect.Status))
	}
	var decoded any
	if len(step.Expect.JSON) > 0 || len(step.Capture) > 0 {
		if err := decodeJSON(data, &decoded); err != nil {
			failures = append(failures, fmt.Errorf("response is not valid JSON: %w", err))
			return result, errors.Join(failures...)
		}
	}
	for _, pointer := range sortedKeys(step.Expect.JSON) {
		expected, err := Resolve(step.Expect.JSON[pointer], vars)
		assertion := Assertion{Pointer: pointer, Expected: expected}
		if err != nil {
			assertion.Error = "resolve expected value: " + err.Error()
		} else {
			actual, lookupErr := Lookup(decoded, pointer)
			assertion.Actual = actual
			if lookupErr != nil {
				assertion.Error = lookupErr.Error()
			} else {
				assertion.Passed = equalJSON(expected, actual)
				if !assertion.Passed {
					assertion.Error = fmt.Sprintf("expected %s, got %s", jsonText(expected), jsonText(actual))
				}
			}
		}
		result.Assertions = append(result.Assertions, assertion)
		if !assertion.Passed {
			failures = append(failures, fmt.Errorf("JSON pointer %q: %s", pointer, assertion.Error))
		}
	}
	captured := Variables{}
	for _, name := range sortedKeys(step.Capture) {
		value, err := Lookup(decoded, step.Capture[name])
		if err != nil {
			failures = append(failures, fmt.Errorf("capture %s at %s: %w", name, step.Capture[name], err))
		} else {
			captured[name] = value
		}
	}
	if len(failures) > 0 {
		return result, errors.Join(failures...)
	}
	for name, value := range captured {
		vars[name] = value
	}
	return result, nil
}

// Lookup evaluates an RFC 6901 JSON pointer without confusing missing and null.
func Lookup(value any, pointer string) (any, error) {
	if pointer == "" {
		return value, nil
	}
	if !strings.HasPrefix(pointer, "/") {
		return nil, errors.New("JSON pointer must be empty or begin with /")
	}
	for _, encoded := range strings.Split(pointer[1:], "/") {
		for i := 0; i < len(encoded); i++ {
			if encoded[i] == '~' {
				if i+1 == len(encoded) || (encoded[i+1] != '0' && encoded[i+1] != '1') {
					return nil, errors.New("invalid JSON pointer escape")
				}
				i++
			}
		}
		key := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		switch current := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = current[key]
			if !ok {
				return nil, fmt.Errorf("object key %q is missing", key)
			}
		case []any:
			index, err := strconv.Atoi(key)
			if err != nil || index < 0 || index >= len(current) || strconv.Itoa(index) != key {
				return nil, fmt.Errorf("invalid array index %q", key)
			}
			value = current[index]
		default:
			return nil, fmt.Errorf("cannot descend into %s at %q", jsonText(value), key)
		}
	}
	return value, nil
}

// Resolve substitutes captures recursively. A whole token retains its JSON type;
// embedded tokens must contain scalar values and become text.
func Resolve(value any, vars Variables) (any, error) {
	switch current := value.(type) {
	case string:
		matches := token.FindStringSubmatch(current)
		if len(matches) == 2 && matches[0] == current {
			resolved, ok := vars[matches[1]]
			if !ok {
				return nil, fmt.Errorf("undefined response variable %q", matches[1])
			}
			return resolved, nil
		}
		return resolveString(current, vars, false)
	case map[string]any:
		result := make(map[string]any, len(current))
		for _, key := range sortedKeys(current) {
			resolved, err := Resolve(current[key], vars)
			if err != nil {
				return nil, err
			}
			result[key] = resolved
		}
		return result, nil
	case map[any]any:
		result := make(map[string]any, len(current))
		for key, item := range current {
			text, ok := key.(string)
			if !ok {
				return nil, fmt.Errorf("JSON object key must be a string, got %T", key)
			}
			resolved, err := Resolve(item, vars)
			if err != nil {
				return nil, err
			}
			result[text] = resolved
		}
		return result, nil
	case []any:
		result := make([]any, len(current))
		for i, item := range current {
			resolved, err := Resolve(item, vars)
			if err != nil {
				return nil, err
			}
			result[i] = resolved
		}
		return result, nil
	default:
		return value, nil
	}
}

func resolveString(value string, vars Variables, escapePath bool) (string, error) {
	var resolveErr error
	resolved := token.ReplaceAllStringFunc(value, func(match string) string {
		name := token.FindStringSubmatch(match)[1]
		item, ok := vars[name]
		if !ok {
			resolveErr = fmt.Errorf("undefined response variable %q", name)
			return match
		}
		var text string
		switch item.(type) {
		case string:
			text = item.(string)
		case bool, float64, float32, int, int64, int32, uint, uint64, json.Number:
			text = fmt.Sprint(item)
		default:
			resolveErr = fmt.Errorf("response variable %q is not scalar", name)
			return match
		}
		if escapePath {
			return url.PathEscape(text)
		}
		return text
	})
	if strings.Contains(resolved, "{{") || strings.Contains(resolved, "}}") {
		if resolveErr == nil {
			resolveErr = errors.New("malformed response variable token")
		}
	}
	return resolved, resolveErr
}

func equalJSON(left, right any) bool {
	normalize := func(value any) (any, error) {
		data, err := json.Marshal(value)
		if err != nil {
			return nil, err
		}
		var normalized any
		if err := decodeJSON(data, &normalized); err != nil {
			return nil, err
		}
		return normalized, nil
	}
	l, err := normalize(left)
	if err != nil {
		return false
	}
	r, err := normalize(right)
	if err != nil {
		return false
	}
	return equivalent(l, r)
}

// Numbers are compared as exact decimals so 7, 7.0 and 7e0 are equivalent,
// while adjacent large integers remain distinguishable. Neither assertion
// comparison nor captures pass through float64.
func equivalent(left, right any) bool {
	switch l := left.(type) {
	case json.Number:
		r, ok := right.(json.Number)
		if !ok {
			return false
		}
		return canonicalNumber(l) == canonicalNumber(r)
	case []any:
		r, ok := right.([]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for i := range l {
			if !equivalent(l[i], r[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		r, ok := right.(map[string]any)
		if !ok || len(l) != len(r) {
			return false
		}
		for key, value := range l {
			actual, present := r[key]
			if !present || !equivalent(value, actual) {
				return false
			}
		}
		return true
	default:
		return reflect.DeepEqual(left, right)
	}
}

// Keep an unexpanded coefficient and base-ten exponent. Unlike conversion to a
// rational, a response such as 1e1000000000 does not allocate a billion digits.
func canonicalNumber(number json.Number) string {
	text := number.String()
	sign := ""
	if strings.HasPrefix(text, "-") {
		sign, text = "-", text[1:]
	}
	exponent := new(big.Int)
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		exponent.SetString(text[index+1:], 10)
		text = text[:index]
	}
	if index := strings.IndexByte(text, '.'); index >= 0 {
		exponent.Sub(exponent, big.NewInt(int64(len(text)-index-1)))
		text = text[:index] + text[index+1:]
	}
	text = strings.TrimLeft(text, "0")
	if text == "" {
		return "0"
	}
	coefficient := strings.TrimRight(text, "0")
	exponent.Add(exponent, big.NewInt(int64(len(text)-len(coefficient))))
	return sign + coefficient + "e" + exponent.String()
}

func decodeJSON(data []byte, output any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(output); err != nil {
		return err
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return errors.New("response must contain one JSON document")
	}
	return nil
}

func jsonText(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

func sortedKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
