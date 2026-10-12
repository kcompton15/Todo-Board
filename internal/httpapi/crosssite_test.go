package httpapi

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kcompton15/Todo-Board/internal/store"
)

func TestCrossSiteWritesRejected(t *testing.T) {
	s, err := store.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	handler := New(s, NewHub(), nil, nil).Handler()
	body := `{"kind":"weekly","start":"2026-10-05","end":"2026-10-11"}`
	cases := []struct {
		name    string
		method  string
		headers map[string]string
		want    int
	}{
		{"cli without browser headers", http.MethodPost, nil, http.StatusAccepted},
		{"same-origin browser", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": "http://127.0.0.1:7337"}, http.StatusAccepted},
		{"cross-site fetch", http.MethodPost, map[string]string{"Sec-Fetch-Site": "cross-site", "Origin": "https://evil.example"}, http.StatusForbidden},
		{"same-site fetch", http.MethodPost, map[string]string{"Sec-Fetch-Site": "same-site"}, http.StatusForbidden},
		{"foreign origin without fetch metadata", http.MethodPost, map[string]string{"Origin": "https://evil.example"}, http.StatusForbidden},
		{"opaque origin", http.MethodPost, map[string]string{"Origin": "null"}, http.StatusForbidden},
		{"cross-site read allowed", http.MethodGet, map[string]string{"Sec-Fetch-Site": "cross-site"}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path, reqBody := "/api/reports/jobs", body
			if tc.method == http.MethodGet {
				path, reqBody = "/api/reports/worker", ""
			}
			req := httptest.NewRequest(tc.method, "http://127.0.0.1:7337"+path, strings.NewReader(reqBody))
			req.Header.Set("Content-Type", "application/json")
			for key, value := range tc.headers {
				req.Header.Set(key, value)
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, req)
			if w.Code != tc.want {
				t.Fatalf("got %d want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestLoopbackHostsOnly(t *testing.T) {
	handler := LoopbackHosts(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	for host, want := range map[string]int{
		"127.0.0.1:7337":        http.StatusNoContent,
		"localhost:7337":        http.StatusNoContent,
		"LOCALHOST":             http.StatusNoContent,
		"[::1]:7337":            http.StatusNoContent,
		"evil.example:7337":     http.StatusForbidden,
		"127.0.0.1.nip.io:7337": http.StatusForbidden,
		"":                      http.StatusForbidden,
	} {
		req := httptest.NewRequest(http.MethodGet, "/api/tasks", nil)
		req.Host = host
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, req)
		if w.Code != want {
			t.Fatalf("host %q: got %d want %d", host, w.Code, want)
		}
	}
}
