package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *URLShortener {
	t.Helper()

	store, err := NewURLShortener(filepath.Join(t.TempDir(), "links.json"))
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	return store
}

func TestShortenURLNormalizesAndReusesCode(t *testing.T) {
	store := newTestStore(t)

	link1, err := store.ShortenURL("HTTPS://Example.COM/docs", "", nil)
	if err != nil {
		t.Fatalf("first shorten failed: %v", err)
	}

	link2, err := store.ShortenURL("https://example.com/docs", "", nil)
	if err != nil {
		t.Fatalf("second shorten failed: %v", err)
	}

	if link1.Code != link2.Code {
		t.Fatalf("expected normalized URLs to reuse the same code, got %q and %q", link1.Code, link2.Code)
	}

	if link2.URL != "https://example.com/docs" {
		t.Fatalf("unexpected normalized URL: %q", link2.URL)
	}
}

func TestShortenURLRejectsInvalidURL(t *testing.T) {
	store := newTestStore(t)

	if _, err := store.ShortenURL("example.com/no-scheme", "", nil); err != errInvalidURL {
		t.Fatalf("expected errInvalidURL, got %v", err)
	}
}

func TestShortenURLSupportsCustomCode(t *testing.T) {
	store := newTestStore(t)

	link, err := store.ShortenURL("https://example.com", "docs", nil)
	if err != nil {
		t.Fatalf("expected custom code to succeed, got %v", err)
	}

	if link.Code != "docs" {
		t.Fatalf("expected custom code to be preserved, got %q", link.Code)
	}
}

func TestShortenURLRejectsConflictingCustomCode(t *testing.T) {
	store := newTestStore(t)

	if _, err := store.ShortenURL("https://example.com/one", "team", nil); err != nil {
		t.Fatalf("setup shorten failed: %v", err)
	}

	if _, err := store.ShortenURL("https://example.com/two", "team", nil); err != errCodeInUse {
		t.Fatalf("expected errCodeInUse, got %v", err)
	}
}

func TestShortenURLPersistsData(t *testing.T) {
	dataFile := filepath.Join(t.TempDir(), "links.json")

	store, err := NewURLShortener(dataFile)
	if err != nil {
		t.Fatalf("failed to create store: %v", err)
	}

	if _, err := store.ShortenURL("https://example.com/persist", "keep", nil); err != nil {
		t.Fatalf("failed to shorten URL: %v", err)
	}

	reloaded, err := NewURLShortener(dataFile)
	if err != nil {
		t.Fatalf("failed to reload store: %v", err)
	}

	link, err := reloaded.GetLink("keep")
	if err != nil {
		t.Fatalf("expected persisted link, got %v", err)
	}

	if link.URL != "https://example.com/persist" {
		t.Fatalf("unexpected persisted URL: %q", link.URL)
	}
}

func TestResolveTracksAnalytics(t *testing.T) {
	store := newTestStore(t)

	link, err := store.ShortenURL("https://example.com/metrics", "metrics", nil)
	if err != nil {
		t.Fatalf("failed to shorten URL: %v", err)
	}

	resolved, err := store.Resolve(link.Code, "https://referrer.test", "go-test")
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}

	if resolved.Clicks != 1 {
		t.Fatalf("expected one click, got %d", resolved.Clicks)
	}

	if resolved.LastReferrer != "https://referrer.test" {
		t.Fatalf("unexpected referrer: %q", resolved.LastReferrer)
	}

	if resolved.LastUserAgent != "go-test" {
		t.Fatalf("unexpected user agent: %q", resolved.LastUserAgent)
	}

	if resolved.LastAccessedAt == nil {
		t.Fatalf("expected last accessed timestamp to be recorded")
	}
}

func TestResolveReturnsExpiredForExpiredLink(t *testing.T) {
	store := newTestStore(t)
	expiresAt := time.Now().UTC().Add(50 * time.Millisecond)

	link, err := store.ShortenURL("https://example.com/soon", "soon", &expiresAt)
	if err != nil {
		t.Fatalf("failed to create expiring link: %v", err)
	}

	time.Sleep(80 * time.Millisecond)

	if _, err := store.Resolve(link.Code, "", ""); err != errExpired {
		t.Fatalf("expected errExpired, got %v", err)
	}
}

func TestDeleteLinkRemovesLink(t *testing.T) {
	store := newTestStore(t)

	if _, err := store.ShortenURL("https://example.com/delete", "gone", nil); err != nil {
		t.Fatalf("failed to create link: %v", err)
	}

	if err := store.DeleteLink("gone"); err != nil {
		t.Fatalf("delete failed: %v", err)
	}

	if _, err := store.GetLink("gone"); err != errNotFound {
		t.Fatalf("expected errNotFound after delete, got %v", err)
	}
}

func TestServerUIAndManagementEndpoints(t *testing.T) {
	store := newTestStore(t)
	server := NewServer(store, "https://sho.rt")

	postBody := `{"url":"https://example.com/app","custom_code":"app"}`
	postReq := httptest.NewRequest(http.MethodPost, "/shorten", strings.NewReader(postBody))
	postReq.Header.Set("Content-Type", "application/json")
	postRec := httptest.NewRecorder()
	server.ServeHTTP(postRec, postReq)

	if postRec.Code != http.StatusCreated {
		t.Fatalf("expected create status %d, got %d: %s", http.StatusCreated, postRec.Code, postRec.Body.String())
	}

	var created linkResponse
	if err := json.Unmarshal(postRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("failed to decode create response: %v", err)
	}

	if created.ShortURL != "https://sho.rt/app" {
		t.Fatalf("unexpected short URL: %q", created.ShortURL)
	}

	listReq := httptest.NewRequest(http.MethodGet, "/links", nil)
	listRec := httptest.NewRecorder()
	server.ServeHTTP(listRec, listReq)

	if listRec.Code != http.StatusOK {
		t.Fatalf("expected list status %d, got %d", http.StatusOK, listRec.Code)
	}

	var links []linkResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &links); err != nil {
		t.Fatalf("failed to decode list response: %v", err)
	}

	if len(links) != 1 || links[0].Code != "app" {
		t.Fatalf("unexpected list payload: %+v", links)
	}

	uiReq := httptest.NewRequest(http.MethodGet, "/", nil)
	uiRec := httptest.NewRecorder()
	server.ServeHTTP(uiRec, uiReq)

	if uiRec.Code != http.StatusOK {
		t.Fatalf("expected UI status %d, got %d", http.StatusOK, uiRec.Code)
	}

	if !strings.Contains(uiRec.Body.String(), "Compact URL Control Room") {
		t.Fatalf("expected UI markup in response")
	}
}

func TestServerRedirectAndAnalyticsEndpoint(t *testing.T) {
	store := newTestStore(t)
	server := NewServer(store, "https://sho.rt")

	if _, err := store.ShortenURL("https://example.com/report", "report", nil); err != nil {
		t.Fatalf("failed to create link: %v", err)
	}

	redirectReq := httptest.NewRequest(http.MethodGet, "/report", nil)
	redirectReq.Header.Set("Referer", "https://dashboard.test")
	redirectReq.Header.Set("User-Agent", "browser-test")
	redirectRec := httptest.NewRecorder()
	server.ServeHTTP(redirectRec, redirectReq)

	if redirectRec.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect status %d, got %d", http.StatusSeeOther, redirectRec.Code)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/links/report", nil)
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusOK {
		t.Fatalf("expected get status %d, got %d", http.StatusOK, getRec.Code)
	}

	var link linkResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &link); err != nil {
		t.Fatalf("failed to decode link response: %v", err)
	}

	if link.Clicks != 1 {
		t.Fatalf("expected one click after redirect, got %d", link.Clicks)
	}

	if link.LastReferrer != "https://dashboard.test" {
		t.Fatalf("unexpected referrer: %q", link.LastReferrer)
	}

	if link.LastUserAgent != "browser-test" {
		t.Fatalf("unexpected user agent: %q", link.LastUserAgent)
	}
}

func TestServerReturnsGoneForExpiredLink(t *testing.T) {
	store := newTestStore(t)
	server := NewServer(store, "https://sho.rt")
	expiresAt := time.Now().UTC().Add(50 * time.Millisecond)

	if _, err := store.ShortenURL("https://example.com/expired", "expired", &expiresAt); err != nil {
		t.Fatalf("failed to create expiring link: %v", err)
	}

	time.Sleep(80 * time.Millisecond)

	req := httptest.NewRequest(http.MethodGet, "/expired", nil)
	rec := httptest.NewRecorder()
	server.ServeHTTP(rec, req)

	if rec.Code != http.StatusGone {
		t.Fatalf("expected gone status %d, got %d", http.StatusGone, rec.Code)
	}
}

func TestServerDeleteEndpoint(t *testing.T) {
	store := newTestStore(t)
	server := NewServer(store, "https://sho.rt")

	if _, err := store.ShortenURL("https://example.com/delete-me", "delete-me", nil); err != nil {
		t.Fatalf("failed to create link: %v", err)
	}

	deleteReq := httptest.NewRequest(http.MethodDelete, "/links/delete-me", nil)
	deleteRec := httptest.NewRecorder()
	server.ServeHTTP(deleteRec, deleteReq)

	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("expected delete status %d, got %d", http.StatusNoContent, deleteRec.Code)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/links/delete-me", nil)
	getRec := httptest.NewRecorder()
	server.ServeHTTP(getRec, getReq)

	if getRec.Code != http.StatusNotFound {
		t.Fatalf("expected not found after delete, got %d", getRec.Code)
	}
}

func TestGettersUseEnv(t *testing.T) {
	oldBase, hadBase := os.LookupEnv("BASE_URL")
	oldData, hadData := os.LookupEnv("DATA_FILE")
	oldBind, hadBind := os.LookupEnv("BIND_ADDR")

	t.Cleanup(func() {
		restoreEnv("BASE_URL", oldBase, hadBase)
		restoreEnv("DATA_FILE", oldData, hadData)
		restoreEnv("BIND_ADDR", oldBind, hadBind)
	})

	if err := os.Setenv("BASE_URL", "https://sho.rt/"); err != nil {
		t.Fatalf("failed to set BASE_URL: %v", err)
	}
	if err := os.Setenv("DATA_FILE", "state/links.json"); err != nil {
		t.Fatalf("failed to set DATA_FILE: %v", err)
	}
	if err := os.Setenv("BIND_ADDR", ":9090"); err != nil {
		t.Fatalf("failed to set BIND_ADDR: %v", err)
	}

	if got := getBaseURL(); got != "https://sho.rt" {
		t.Fatalf("unexpected base URL: %q", got)
	}
	if got := getDataFile(); got != "state/links.json" {
		t.Fatalf("unexpected data file: %q", got)
	}
	if got := getBindAddr(); got != ":9090" {
		t.Fatalf("unexpected bind address: %q", got)
	}
}

func restoreEnv(key, value string, hadValue bool) {
	if hadValue {
		_ = os.Setenv(key, value)
		return
	}
	_ = os.Unsetenv(key)
}
