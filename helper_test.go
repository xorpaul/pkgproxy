package main

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHandleErrorNilResponse(t *testing.T) {
	w := httptest.NewRecorder()
	handleError(nil, errors.New("dial tcp: lookup badhost.invalid: no such host"), w)

	resp := w.Result()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Errorf("status: want 500, got %d", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Pkgproxy-Error"); got != "remote-unreachable" {
		t.Errorf("X-Pkgproxy-Error: want %q, got %q", "remote-unreachable", got)
	}
	body, _ := io.ReadAll(resp.Body)
	bodyStr := string(body)
	if !strings.Contains(bodyStr, "pkgproxy: could not reach the requested remote URL") {
		t.Errorf("body missing helpful message, got: %s", bodyStr)
	}
	if !strings.Contains(bodyStr, "no such host") {
		t.Errorf("body missing original error text, got: %s", bodyStr)
	}
}

func TestHandleErrorWithResponse(t *testing.T) {
	w := httptest.NewRecorder()
	fakeResp := &http.Response{
		StatusCode: http.StatusNotFound,
		Body:       io.NopCloser(strings.NewReader("upstream: not found")),
	}
	handleError(fakeResp, errors.New("GET https://example.com/foo returned 404"), w)

	resp := w.Result()

	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status: want 404, got %d", resp.StatusCode)
	}
	want := "remote returned HTTP 404"
	if got := resp.Header.Get("X-Pkgproxy-Error"); got != want {
		t.Errorf("X-Pkgproxy-Error: want %q, got %q", want, got)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != "upstream: not found" {
		t.Errorf("body: want upstream body forwarded, got: %s", body)
	}
}
