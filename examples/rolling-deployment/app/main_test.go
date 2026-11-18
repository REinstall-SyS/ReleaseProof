package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUserIDValidation(t *testing.T) {
	for _, value := range []string{"0", "-1", "abc", "9223372036854775808"} {
		t.Run(value, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/users/"+value, nil)
			r.SetPathValue("id", value)
			if _, err := userID(r); err == nil {
				t.Fatal("invalid identifier accepted")
			}
		})
	}
	r := httptest.NewRequest(http.MethodGet, "/users/7", nil)
	r.SetPathValue("id", "7")
	id, err := userID(r)
	if err != nil || id != 7 {
		t.Fatalf("valid identifier: got %d, %v", id, err)
	}
}

func TestDecodeRejectsUnknownFieldsAndTrailingDocuments(t *testing.T) {
	for _, body := range []string{
		`{"full_name":"Ada Lovelace","first_name":"Ada"}`,
		`{"full_name":"Ada Lovelace"} {"full_name":"Grace Hopper"}`,
		`{"full_name":"Ada Lovelace"} null`,
		`{"full_name":`,
		`{"full_name":"` + strings.Repeat("a", 8192) + `"}`,
	} {
		var target struct {
			FullName string `json:"full_name"`
		}
		r := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(body))
		if err := decode(httptest.NewRecorder(), r, &target); err == nil {
			t.Fatalf("invalid body accepted: %.100s", body)
		}
	}
}

func TestInvalidRequestsDoNotReachTheDatabase(t *testing.T) {
	for _, version := range []string{"old", "new"} {
		s := &service{version: version}
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/users", strings.NewReader(`{}`))
		s.create(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s empty create status = %d", version, w.Code)
		}
		w = httptest.NewRecorder()
		r = httptest.NewRequest(http.MethodGet, "/users/nope", nil)
		r.SetPathValue("id", "nope")
		s.get(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("%s invalid identifier status = %d", version, w.Code)
		}
	}
}
