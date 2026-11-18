package assertions

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/REinstall-SyS/ReleaseProof/internal/config"
)

func TestRunCapturesAndTypedAssertions(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("unexpected request: %s %s", r.Method, r.Header.Get("Content-Type"))
		}
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"id":7,"user":{"name":"Ada Lovelace"},"active":true,"none":null}`)
	}))
	defer server.Close()
	vars := Variables{"name": "Ada Lovelace", "active": true}
	step := config.Step{Name: "create", App: "old", Method: "POST", Path: "/users", Body: map[string]any{"name": "{{name}}", "active": "{{active}}"}, Expect: config.Expect{Status: 201, JSON: map[string]any{"/id": 7, "/user/name": "{{name}}", "/active": true, "/none": nil}}, Capture: map[string]string{"id": "/id"}}
	result, err := Run(context.Background(), server.Client(), server.URL, step, vars)
	if err != nil || vars["id"] != json.Number("7") || len(result.Assertions) != 5 {
		t.Fatalf("result=%+v vars=%v err=%v", result, vars, err)
	}
}

func TestFailureKeepsActualEvidenceAndDoesNotCapture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `{"id":8,"name":"stale"}`) }))
	defer server.Close()
	vars := Variables{}
	result, err := Run(context.Background(), server.Client(), server.URL, config.Step{Method: "GET", Path: "/users/8", Expect: config.Expect{Status: 200, JSON: map[string]any{"/name": "fresh", "/missing": nil}}, Capture: map[string]string{"id": "/id"}}, vars)
	if err == nil || !strings.Contains(err.Error(), `expected "fresh", got "stale"`) || !strings.Contains(err.Error(), `key "missing" is missing`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(vars) != 0 || !strings.Contains(result.Body, "stale") || result.Assertions[2].Actual != "stale" || result.Error == "" {
		t.Fatalf("lost evidence or published failed capture: %+v %v", result, vars)
	}
}

func TestRedirectNotFollowedAndWritesNotRetried(t *testing.T) {
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		http.Redirect(w, r, "/danger", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	result, err := Run(context.Background(), server.Client(), server.URL, config.Step{Method: "POST", Path: "/start", Expect: config.Expect{Status: 200}}, Variables{})
	if err == nil || result.Status != 307 || requests != 1 {
		t.Fatalf("requests=%d result=%+v error=%v", requests, result, err)
	}
}

func TestPathCaptureIsEscaped(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.RawPath != "/users/a%2Fb" {
			t.Errorf("path not escaped: %q", r.URL.RawPath)
		}
		fmt.Fprint(w, `ok`)
	}))
	defer server.Close()
	_, err := Run(context.Background(), server.Client(), server.URL, config.Step{Method: "GET", Path: "/users/{{id}}", Expect: config.Expect{Status: 200}}, Variables{"id": "a/b"})
	if err != nil {
		t.Fatal(err)
	}
}

func TestResponseBound(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, strings.Repeat("x", MaxResponseBytes+1)) }))
	defer server.Close()
	result, err := Run(context.Background(), server.Client(), server.URL, config.Step{Method: "GET", Path: "/", Expect: config.Expect{Status: 200}}, Variables{})
	if err == nil || !strings.Contains(err.Error(), "exceeds") || len(result.Body) != MaxResponseBytes {
		t.Fatalf("body length=%d err=%v", len(result.Body), err)
	}
}

func TestCancelledRequest(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, nil, "http://127.0.0.1:1", config.Step{Method: "GET", Path: "/", Expect: config.Expect{Status: 200}}, Variables{})
	if err == nil || !strings.Contains(err.Error(), "context canceled") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLookupPointers(t *testing.T) {
	data := map[string]any{"a/b": map[string]any{"~": []any{nil, "value"}}, "": true}
	value, err := Lookup(data, "/a~1b/~0/1")
	if err != nil || value != "value" {
		t.Fatalf("%v %v", value, err)
	}
	for _, pointer := range []string{"no-slash", "/a~2b", "/a~", "/a~1b/~0/01", "/a~1b/~0/-", "/a~1b/~0/2", "/missing", "/a~1b/~0/0/key"} {
		if _, err := Lookup(data, pointer); err == nil {
			t.Errorf("accepted invalid pointer %q", pointer)
		}
	}
	if value, err := Lookup(data, "/a~1b/~0/0"); err != nil || value != nil {
		t.Errorf("null differs from missing: %v %v", value, err)
	}
}

func TestResolvePreservesTypesAndErrors(t *testing.T) {
	vars := Variables{"id": float64(7), "object": map[string]any{"ok": true}}
	resolved, err := Resolve(map[string]any{"id": "{{id}}", "nested": []any{"item {{id}}", "{{object}}"}}, vars)
	if err != nil || resolved.(map[string]any)["id"] != float64(7) {
		t.Fatalf("%v %v", resolved, err)
	}
	for _, value := range []any{"{{missing}}", "x {{object}}", "{{malformed", map[any]any{1: "bad"}} {
		if _, err := Resolve(value, vars); err == nil {
			t.Errorf("accepted invalid template %#v", value)
		}
	}
}

func TestLargeIntegerCaptureAndAssertionStayExact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/create" && r.URL.Path != "/users/9007199254740993" {
			t.Errorf("captured identifier lost precision: %s", r.URL.Path)
		}
		fmt.Fprint(w, `{"id":9007199254740993,"balance":12345678901234567890.125}`)
	}))
	defer server.Close()
	vars := Variables{}
	step := config.Step{Method: "GET", Path: "/create", Expect: config.Expect{Status: 200, JSON: map[string]any{"/id": int64(9007199254740993), "/balance": json.Number("12345678901234567890.125")}}, Capture: map[string]string{"id": "/id"}}
	if result, err := Run(context.Background(), server.Client(), server.URL, step, vars); err != nil || vars["id"] != json.Number("9007199254740993") {
		t.Fatalf("capture or assertion lost precision: %+v vars=%v err=%v", result, vars, err)
	}
	step.Path, step.Capture = "/users/{{id}}", nil
	step.Expect.JSON["/id"] = "{{id}}"
	if result, err := Run(context.Background(), server.Client(), server.URL, step, vars); err != nil {
		t.Fatalf("cannot reuse exact identifier: %+v %v", result, err)
	}
	step.Expect.JSON["/id"] = int64(9007199254740992)
	if _, err := Run(context.Background(), server.Client(), server.URL, step, vars); err == nil {
		t.Fatal("adjacent large integers incorrectly compared equal")
	}
}

func TestJSONNumericEqualityUsesExactDecimals(t *testing.T) {
	for _, pair := range [][2]any{{7, json.Number("7.0")}, {json.Number("7e0"), 7}, {json.Number("0.100"), json.Number("1e-1")}, {json.Number("-0"), json.Number("0e999999999")}, {json.Number("1e1000000000"), json.Number("10e999999999")}, {map[string]any{"n": int64(9007199254740993)}, map[string]any{"n": json.Number("9007199254740993")}}} {
		if !equalJSON(pair[0], pair[1]) {
			t.Errorf("equivalent JSON numbers compare unequal: %#v %#v", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]any{{json.Number("9007199254740992"), json.Number("9007199254740993")}, {json.Number("0.100000000000000000001"), json.Number("0.1")}, {7, "7"}} {
		if equalJSON(pair[0], pair[1]) {
			t.Errorf("different JSON values compare equal: %#v %#v", pair[0], pair[1])
		}
	}
}
