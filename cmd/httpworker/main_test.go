package main

import (
	"context"
	"encoding/base64"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestExecuteSingleHop(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test", "yes")
		_, _ = w.Write([]byte("hello"))
	}))
	defer srv.Close()

	got := execute(context.Background(), request{Method: http.MethodGet, URL: srv.URL, MaxBody: 1024})
	if got.Error != "" {
		t.Fatalf("error: %s", got.Error)
	}
	if got.Status != http.StatusOK {
		t.Fatalf("status = %d", got.Status)
	}
	body, err := base64.StdEncoding.DecodeString(got.BodyBase64)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "hello" {
		t.Fatalf("body = %q", body)
	}
	if got.Headers["X-Test"][0] != "yes" {
		t.Fatalf("header = %v", got.Headers)
	}
}

func TestExecuteDoesNotFollowRedirect(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/next", http.StatusFound)
	}))
	defer srv.Close()

	got := execute(context.Background(), request{Method: http.MethodGet, URL: srv.URL})
	if got.Status != http.StatusFound {
		t.Fatalf("status = %d, want 302", got.Status)
	}
	if got.Headers["Location"][0] != "/next" {
		t.Fatalf("location = %v", got.Headers["Location"])
	}
}

func TestExecuteTruncatesBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("a", 100)))
	}))
	defer srv.Close()

	got := execute(context.Background(), request{Method: http.MethodGet, URL: srv.URL, MaxBody: 10})
	if !got.Truncated {
		t.Fatal("expected truncation")
	}
	body, _ := base64.StdEncoding.DecodeString(got.BodyBase64)
	if len(body) != 10 {
		t.Fatalf("body length = %d, want 10", len(body))
	}
}

func TestExecuteTransportError(t *testing.T) {
	got := execute(context.Background(), request{Method: http.MethodGet, URL: "http://127.0.0.1:1/"})
	if got.Error == "" {
		t.Fatal("expected a transport error")
	}
}
