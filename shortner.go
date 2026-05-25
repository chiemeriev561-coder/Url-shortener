package main

import (
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"log"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	charset          = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	codeLength       = 7
	defaultBaseURL   = "http://localhost:8080"
	defaultDataFile  = "data/links.json"
	defaultBindAddr  = ":8080"
	expiryTimeLayout = time.RFC3339
)

var (
	errInvalidURL    = errors.New("invalid URL")
	errCodeInUse     = errors.New("short code already in use")
	errInvalidAlias  = errors.New("custom code may only contain letters, numbers, hyphens, and underscores")
	errEmptyAlias    = errors.New("custom code cannot be empty")
	errReservedAlias = errors.New("custom code is reserved")
	errInvalidExpiry = errors.New("expires_at must be a future RFC3339 timestamp")
	errNotFound      = errors.New("short code not found")
	errExpired       = errors.New("short code has expired")
	reservedPaths    = map[string]struct{}{
		"shorten": {},
		"links":   {},
	}
)

type Link struct {
	Code           string     `json:"code"`
	URL            string     `json:"url"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Clicks         int        `json:"clicks"`
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty"`
	LastReferrer   string     `json:"last_referrer,omitempty"`
	LastUserAgent  string     `json:"last_user_agent,omitempty"`
}

type storedData struct {
	Links map[string]*Link `json:"links"`
}

type URLShortener struct {
	mu       sync.RWMutex
	links    map[string]*Link
	codes    map[string]string
	dataFile string
}

type ShortenRequest struct {
	URL        string  `json:"url"`
	CustomCode string  `json:"custom_code,omitempty"`
	ExpiresAt  *string `json:"expires_at,omitempty"`
}

type linkResponse struct {
	Code           string     `json:"code"`
	ShortURL       string     `json:"short_url"`
	OriginalURL    string     `json:"original_url"`
	CreatedAt      time.Time  `json:"created_at"`
	ExpiresAt      *time.Time `json:"expires_at,omitempty"`
	Expired        bool       `json:"expired"`
	Clicks         int        `json:"clicks"`
	LastAccessedAt *time.Time `json:"last_accessed_at,omitempty"`
	LastReferrer   string     `json:"last_referrer,omitempty"`
	LastUserAgent  string     `json:"last_user_agent,omitempty"`
}

type pageData struct {
	BaseURL string
}

type Server struct {
	store   *URLShortener
	baseURL string
	mux     *http.ServeMux
	ui      *template.Template
}

func NewURLShortener(dataFile string) (*URLShortener, error) {
	store := &URLShortener{
		links:    make(map[string]*Link),
		codes:    make(map[string]string),
		dataFile: dataFile,
	}

	if err := store.load(); err != nil {
		return nil, err
	}

	return store, nil
}

func (s *URLShortener) load() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	content, err := os.ReadFile(s.dataFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}

	if len(content) == 0 {
		return nil
	}

	var data storedData
	if err := json.Unmarshal(content, &data); err != nil {
		return err
	}

	if data.Links != nil {
		s.links = data.Links
	}

	s.rebuildCodesLocked(time.Now())
	return nil
}

func (s *URLShortener) rebuildCodesLocked(now time.Time) {
	s.codes = make(map[string]string)
	for code, link := range s.links {
		if link == nil {
			delete(s.links, code)
			continue
		}
		if !isExpired(link, now) {
			if _, exists := s.codes[link.URL]; !exists {
				s.codes[link.URL] = code
			}
		}
	}
}

func (s *URLShortener) saveLocked() error {
	if err := os.MkdirAll(filepath.Dir(s.dataFile), 0o755); err != nil {
		return err
	}

	payload, err := json.MarshalIndent(storedData{Links: s.links}, "", "  ")
	if err != nil {
		return err
	}

	tempFile := s.dataFile + ".tmp"
	if err := os.WriteFile(tempFile, payload, 0o644); err != nil {
		return err
	}

	return os.Rename(tempFile, s.dataFile)
}

func normalizeURL(raw string) (string, error) {
	parsed, err := url.ParseRequestURI(strings.TrimSpace(raw))
	if err != nil {
		return "", errInvalidURL
	}

	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return "", errInvalidURL
	}

	if parsed.Host == "" {
		return "", errInvalidURL
	}

	parsed.Scheme = strings.ToLower(parsed.Scheme)
	parsed.Host = strings.ToLower(parsed.Host)
	return parsed.String(), nil
}

func validateCustomCode(code string) error {
	code = strings.TrimSpace(code)
	if code == "" {
		return errEmptyAlias
	}

	if _, reserved := reservedPaths[code]; reserved {
		return errReservedAlias
	}

	for _, r := range code {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return errInvalidAlias
	}

	return nil
}

func parseExpiry(raw *string, now time.Time) (*time.Time, error) {
	if raw == nil || strings.TrimSpace(*raw) == "" {
		return nil, nil
	}

	expiresAt, err := time.Parse(expiryTimeLayout, strings.TrimSpace(*raw))
	if err != nil || !expiresAt.After(now) {
		return nil, errInvalidExpiry
	}

	return &expiresAt, nil
}

func generateRandomCode() (string, error) {
	b := make([]byte, codeLength)
	charsetLen := big.NewInt(int64(len(charset)))

	for i := range b {
		n, err := rand.Int(rand.Reader, charsetLen)
		if err != nil {
			return "", err
		}
		b[i] = charset[n.Int64()]
	}

	return string(b), nil
}

func isExpired(link *Link, now time.Time) bool {
	return link.ExpiresAt != nil && !link.ExpiresAt.After(now)
}

func cloneLink(link *Link) *Link {
	if link == nil {
		return nil
	}
	copy := *link
	return &copy
}

func (s *URLShortener) ShortenURL(longURL, customCode string, expiresAt *time.Time) (*Link, error) {
	normalizedURL, err := normalizeURL(longURL)
	if err != nil {
		return nil, err
	}

	customCode = strings.TrimSpace(customCode)
	now := time.Now()

	if expiresAt != nil && !expiresAt.After(now) {
		return nil, errInvalidExpiry
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if existingCode, exists := s.codes[normalizedURL]; exists && customCode == "" {
		return cloneLink(s.links[existingCode]), nil
	}

	if customCode != "" {
		if err := validateCustomCode(customCode); err != nil {
			return nil, err
		}

		if existingLink, exists := s.links[customCode]; exists {
			if existingLink.URL == normalizedURL {
				return cloneLink(existingLink), nil
			}
			return nil, errCodeInUse
		}

		if existingCode, exists := s.codes[normalizedURL]; exists {
			return cloneLink(s.links[existingCode]), nil
		}
	}

	code := customCode
	if code == "" {
		for {
			code, err = generateRandomCode()
			if err != nil {
				return nil, err
			}
			if _, exists := s.links[code]; !exists {
				break
			}
		}
	}

	link := &Link{
		Code:      code,
		URL:       normalizedURL,
		CreatedAt: now.UTC(),
		ExpiresAt: expiresAt,
	}

	s.links[code] = link
	s.codes[normalizedURL] = code

	if err := s.saveLocked(); err != nil {
		delete(s.links, code)
		delete(s.codes, normalizedURL)
		return nil, err
	}

	return cloneLink(link), nil
}

func (s *URLShortener) Resolve(code, referrer, userAgent string) (*Link, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	link, exists := s.links[code]
	if !exists {
		return nil, errNotFound
	}

	now := time.Now().UTC()
	if isExpired(link, now) {
		delete(s.codes, link.URL)
		return nil, errExpired
	}

	link.Clicks++
	link.LastAccessedAt = &now
	link.LastReferrer = referrer
	link.LastUserAgent = userAgent

	if err := s.saveLocked(); err != nil {
		return nil, err
	}

	return cloneLink(link), nil
}

func (s *URLShortener) GetLink(code string) (*Link, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	link, exists := s.links[code]
	if !exists {
		return nil, errNotFound
	}

	return cloneLink(link), nil
}

func (s *URLShortener) DeleteLink(code string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	link, exists := s.links[code]
	if !exists {
		return errNotFound
	}

	delete(s.links, code)
	if mappedCode, ok := s.codes[link.URL]; ok && mappedCode == code {
		delete(s.codes, link.URL)
	}

	return s.saveLocked()
}

func (s *URLShortener) ListLinks() []*Link {
	s.mu.RLock()
	defer s.mu.RUnlock()

	links := make([]*Link, 0, len(s.links))
	for _, link := range s.links {
		links = append(links, cloneLink(link))
	}

	sort.Slice(links, func(i, j int) bool {
		if links[i].CreatedAt.Equal(links[j].CreatedAt) {
			return links[i].Code < links[j].Code
		}
		return links[i].CreatedAt.After(links[j].CreatedAt)
	})

	return links
}

func getBaseURL() string {
	baseURL := strings.TrimSpace(os.Getenv("BASE_URL"))
	if baseURL == "" {
		return defaultBaseURL
	}
	return strings.TrimRight(baseURL, "/")
}

func getDataFile() string {
	dataFile := strings.TrimSpace(os.Getenv("DATA_FILE"))
	if dataFile == "" {
		return defaultDataFile
	}
	return dataFile
}

func getBindAddr() string {
	bindAddr := strings.TrimSpace(os.Getenv("BIND_ADDR"))
	if bindAddr == "" {
		return defaultBindAddr
	}
	return bindAddr
}

func NewServer(store *URLShortener, baseURL string) *Server {
	server := &Server{
		store:   store,
		baseURL: baseURL,
		mux:     http.NewServeMux(),
		ui:      template.Must(template.New("index").Parse(indexHTML)),
	}

	server.routes()
	return server
}

func (s *Server) routes() {
	s.mux.HandleFunc("/shorten", s.handleShorten)
	s.mux.HandleFunc("/links", s.handleLinks)
	s.mux.HandleFunc("/links/", s.handleLinkByCode)
	s.mux.HandleFunc("/", s.handleRoot)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.mux.ServeHTTP(w, r)
}

func (s *Server) handleShorten(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	s.createLink(w, r)
}

func (s *Server) handleLinks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		links := s.store.ListLinks()
		resp := make([]linkResponse, 0, len(links))
		now := time.Now().UTC()

		for _, link := range links {
			resp = append(resp, buildLinkResponse(link, s.baseURL, now))
		}

		writeJSON(w, http.StatusOK, resp)
	case http.MethodPost:
		s.createLink(w, r)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleLinkByCode(w http.ResponseWriter, r *http.Request) {
	code := strings.TrimPrefix(r.URL.Path, "/links/")
	if code == "" || strings.Contains(code, "/") {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	switch r.Method {
	case http.MethodGet:
		link, err := s.store.GetLink(code)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		writeJSON(w, http.StatusOK, buildLinkResponse(link, s.baseURL, time.Now().UTC()))
	case http.MethodDelete:
		if err := s.store.DeleteLink(code); err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.URL.Path == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.ui.Execute(w, pageData{BaseURL: s.baseURL}); err != nil {
			http.Error(w, "Failed to render page", http.StatusInternalServerError)
		}
		return
	}

	code := strings.TrimPrefix(r.URL.Path, "/")
	link, err := s.store.Resolve(code, r.Referer(), r.UserAgent())
	if err != nil {
		switch err {
		case errNotFound:
			http.Error(w, "URL not found", http.StatusNotFound)
		case errExpired:
			http.Error(w, "URL has expired", http.StatusGone)
		default:
			http.Error(w, "Failed to resolve URL", http.StatusInternalServerError)
		}
		return
	}

	http.Redirect(w, r, link.URL, http.StatusSeeOther)
}

func (s *Server) createLink(w http.ResponseWriter, r *http.Request) {
	var req ShortenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "Invalid request", http.StatusBadRequest)
		return
	}

	expiresAt, err := parseExpiry(req.ExpiresAt, time.Now().UTC())
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	link, err := s.store.ShortenURL(req.URL, req.CustomCode, expiresAt)
	if err != nil {
		switch err {
		case errInvalidURL, errInvalidAlias, errEmptyAlias, errReservedAlias, errInvalidExpiry:
			http.Error(w, err.Error(), http.StatusBadRequest)
		case errCodeInUse:
			http.Error(w, err.Error(), http.StatusConflict)
		default:
			http.Error(w, "Failed to shorten URL", http.StatusInternalServerError)
		}
		return
	}

	writeJSON(w, http.StatusCreated, buildLinkResponse(link, s.baseURL, time.Now().UTC()))
}

func buildLinkResponse(link *Link, baseURL string, now time.Time) linkResponse {
	return linkResponse{
		Code:           link.Code,
		ShortURL:       baseURL + "/" + link.Code,
		OriginalURL:    link.URL,
		CreatedAt:      link.CreatedAt,
		ExpiresAt:      link.ExpiresAt,
		Expired:        isExpired(link, now),
		Clicks:         link.Clicks,
		LastAccessedAt: link.LastAccessedAt,
		LastReferrer:   link.LastReferrer,
		LastUserAgent:  link.LastUserAgent,
	}
}

func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}

func main() {
	store, err := NewURLShortener(getDataFile())
	if err != nil {
		log.Fatal("Error loading data: ", err)
	}

	server := NewServer(store, getBaseURL())
	bindAddr := getBindAddr()

	fmt.Println("Starting server on", bindAddr)
	if err := http.ListenAndServe(bindAddr, server); err != nil {
		log.Fatal("Error starting server: ", err)
	}
}

const indexHTML = `<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Compact URL Control Room</title>
  <style>
    :root {
      --sand: #f5efe4;
      --paper: rgba(255, 251, 245, 0.88);
      --ink: #11231c;
      --fern: #1f5c46;
      --moss: #2f7a5d;
      --sun: #f3b562;
      --line: rgba(17, 35, 28, 0.14);
      --muted: rgba(17, 35, 28, 0.68);
      --danger: #8f2d2d;
      --shadow: 0 24px 60px rgba(17, 35, 28, 0.14);
    }

    * { box-sizing: border-box; }

    body {
      margin: 0;
      min-height: 100vh;
      font-family: "Georgia", "Times New Roman", serif;
      color: var(--ink);
      background:
        radial-gradient(circle at top left, rgba(243, 181, 98, 0.35), transparent 30%),
        radial-gradient(circle at right, rgba(47, 122, 93, 0.25), transparent 28%),
        linear-gradient(145deg, #efe6d8 0%, #f7f3ea 48%, #efe4d3 100%);
      padding: 32px 18px 56px;
    }

    .shell {
      max-width: 1100px;
      margin: 0 auto;
      display: grid;
      gap: 20px;
    }

    .hero, .panel {
      background: var(--paper);
      backdrop-filter: blur(14px);
      border: 1px solid rgba(255, 255, 255, 0.5);
      border-radius: 24px;
      box-shadow: var(--shadow);
    }

    .hero {
      padding: 28px;
      display: grid;
      gap: 14px;
      overflow: hidden;
      position: relative;
    }

    .hero::after {
      content: "";
      position: absolute;
      inset: auto -80px -80px auto;
      width: 180px;
      height: 180px;
      border-radius: 999px;
      background: rgba(31, 92, 70, 0.12);
    }

    h1, h2 {
      margin: 0;
      font-weight: 600;
      letter-spacing: 0.02em;
    }

    h1 {
      font-size: clamp(2rem, 4vw, 3.8rem);
      line-height: 0.95;
      max-width: 10ch;
    }

    .eyebrow, .meta, .empty {
      font-family: "Helvetica Neue", Arial, sans-serif;
    }

    .eyebrow {
      text-transform: uppercase;
      letter-spacing: 0.18em;
      font-size: 0.76rem;
      color: var(--moss);
    }

    .meta {
      color: var(--muted);
      max-width: 54ch;
      line-height: 1.5;
      margin: 0;
    }

    .grid {
      display: grid;
      grid-template-columns: 1.15fr 1fr;
      gap: 20px;
    }

    .panel {
      padding: 22px;
    }

    form {
      display: grid;
      gap: 14px;
    }

    label {
      display: grid;
      gap: 6px;
      font-family: "Helvetica Neue", Arial, sans-serif;
      font-size: 0.95rem;
      color: var(--muted);
    }

    input {
      width: 100%;
      padding: 13px 14px;
      border-radius: 14px;
      border: 1px solid var(--line);
      background: rgba(255, 255, 255, 0.7);
      color: var(--ink);
      font: inherit;
    }

    button {
      border: 0;
      border-radius: 999px;
      padding: 14px 18px;
      font: 600 0.95rem/1 "Helvetica Neue", Arial, sans-serif;
      cursor: pointer;
      transition: transform 140ms ease, opacity 140ms ease;
    }

    button:hover { transform: translateY(-1px); }
    button:disabled { opacity: 0.65; cursor: wait; transform: none; }

    .primary {
      background: linear-gradient(135deg, var(--fern), var(--moss));
      color: white;
    }

    .ghost {
      background: rgba(17, 35, 28, 0.06);
      color: var(--ink);
    }

    .toolbar {
      display: flex;
      justify-content: space-between;
      align-items: center;
      gap: 12px;
      margin-bottom: 14px;
    }

    .status {
      min-height: 24px;
      font-family: "Helvetica Neue", Arial, sans-serif;
      font-size: 0.94rem;
    }

    .status.error { color: var(--danger); }
    .status.ok { color: var(--fern); }

    .result {
      padding: 14px;
      border-radius: 16px;
      background: rgba(31, 92, 70, 0.07);
      border: 1px solid rgba(31, 92, 70, 0.12);
      display: none;
      gap: 8px;
      font-family: "Helvetica Neue", Arial, sans-serif;
    }

    .result.show { display: grid; }

    .result a {
      color: var(--fern);
      overflow-wrap: anywhere;
    }

    table {
      width: 100%;
      border-collapse: collapse;
      font-family: "Helvetica Neue", Arial, sans-serif;
      font-size: 0.93rem;
    }

    th, td {
      text-align: left;
      padding: 12px 10px;
      border-bottom: 1px solid var(--line);
      vertical-align: top;
    }

    th {
      color: var(--muted);
      font-weight: 600;
      font-size: 0.78rem;
      text-transform: uppercase;
      letter-spacing: 0.08em;
    }

    td code, .base-url {
      font-family: "Courier New", monospace;
      font-size: 0.9em;
    }

    .link-cell {
      display: grid;
      gap: 4px;
    }

    .link-cell a {
      color: var(--fern);
      overflow-wrap: anywhere;
    }

    .pill {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      border-radius: 999px;
      padding: 5px 10px;
      background: rgba(17, 35, 28, 0.08);
      font-size: 0.8rem;
    }

    .pill.expired {
      background: rgba(143, 45, 45, 0.12);
      color: var(--danger);
    }

    .actions {
      display: flex;
      gap: 8px;
    }

    .empty {
      color: var(--muted);
      padding: 16px 0 4px;
    }

    @media (max-width: 860px) {
      .grid {
        grid-template-columns: 1fr;
      }

      .toolbar {
        align-items: flex-start;
        flex-direction: column;
      }

      table, thead, tbody, th, td, tr {
        display: block;
      }

      thead {
        display: none;
      }

      tr {
        padding: 14px 0;
        border-bottom: 1px solid var(--line);
      }

      td {
        border: 0;
        padding: 6px 0;
      }

      td::before {
        content: attr(data-label);
        display: block;
        font-size: 0.72rem;
        text-transform: uppercase;
        letter-spacing: 0.08em;
        color: var(--muted);
        margin-bottom: 4px;
      }
    }
  </style>
</head>
<body>
  <main class="shell">
    <section class="hero">
      <div class="eyebrow">Compact URL Control Room</div>
      <h1>Short links with memory.</h1>
      <p class="meta">Create validated short URLs, assign custom aliases, set expirations, and review click activity from a single page. Base URL: <span class="base-url">{{.BaseURL}}</span></p>
    </section>

    <section class="grid">
      <div class="panel">
        <div class="toolbar">
          <div>
            <h2>Create Link</h2>
            <p class="meta">Use RFC3339-aware scheduling through the browser picker. Leave optional fields empty when you do not need them.</p>
          </div>
        </div>
        <form id="shorten-form">
          <label>
            Original URL
            <input id="url" name="url" type="url" placeholder="https://example.com/docs" required>
          </label>
          <label>
            Custom code
            <input id="custom_code" name="custom_code" type="text" placeholder="docs">
          </label>
          <label>
            Expiry
            <input id="expires_at" name="expires_at" type="datetime-local">
          </label>
          <button class="primary" id="submit-button" type="submit">Create short URL</button>
        </form>
        <p id="form-status" class="status"></p>
        <section id="result" class="result">
          <strong>Short URL</strong>
          <a id="result-link" href="#" target="_blank" rel="noreferrer"></a>
        </section>
      </div>

      <div class="panel">
        <div class="toolbar">
          <div>
            <h2>Link Inventory</h2>
            <p class="meta">Analytics update on each redirect. Delete links that you no longer want to serve.</p>
          </div>
          <button id="refresh-button" class="ghost" type="button">Refresh</button>
        </div>
        <div id="table-wrap">
          <p class="empty">No links yet.</p>
        </div>
      </div>
    </section>
  </main>

  <script>
    const form = document.getElementById('shorten-form');
    const status = document.getElementById('form-status');
    const result = document.getElementById('result');
    const resultLink = document.getElementById('result-link');
    const submitButton = document.getElementById('submit-button');
    const refreshButton = document.getElementById('refresh-button');
    const tableWrap = document.getElementById('table-wrap');

    function setStatus(message, kind) {
      status.textContent = message;
      status.className = kind ? 'status ' + kind : 'status';
    }

    function toRFC3339(value) {
      if (!value) return null;
      return new Date(value).toISOString();
    }

    function formatDate(value) {
      if (!value) return 'Never';
      return new Date(value).toLocaleString();
    }

    function renderTable(rows) {
      if (!rows.length) {
        tableWrap.innerHTML = '<p class="empty">No links yet.</p>';
        return;
      }

      const body = rows.map((row) => {
        const expiry = row.expires_at ? formatDate(row.expires_at) : 'No expiry';
        const statusPill = row.expired
          ? '<span class="pill expired">Expired</span>'
          : '<span class="pill">Active</span>';

        return '<tr>' +
          '<td data-label="Code"><code>' + row.code + '</code><div>' + statusPill + '</div></td>' +
          '<td data-label="Links"><div class="link-cell">' +
            '<a href="' + row.short_url + '" target="_blank" rel="noreferrer">' + row.short_url + '</a>' +
            '<span>' + row.original_url + '</span>' +
          '</div></td>' +
          '<td data-label="Expiry">' + expiry + '</td>' +
          '<td data-label="Clicks">' + row.clicks + '</td>' +
          '<td data-label="Last Access">' + formatDate(row.last_accessed_at) + '</td>' +
          '<td data-label="Actions"><div class="actions">' +
            '<button class="ghost" type="button" data-delete="' + row.code + '">Delete</button>' +
          '</div></td>' +
        '</tr>';
      }).join('');

      tableWrap.innerHTML = '<table>' +
        '<thead><tr>' +
          '<th>Code</th>' +
          '<th>Links</th>' +
          '<th>Expiry</th>' +
          '<th>Clicks</th>' +
          '<th>Last Access</th>' +
          '<th>Actions</th>' +
        '</tr></thead>' +
        '<tbody>' + body + '</tbody>' +
      '</table>';
    }

    async function loadLinks() {
      const response = await fetch('/links');
      if (!response.ok) {
        throw new Error('Failed to load links');
      }
      const rows = await response.json();
      renderTable(rows);
    }

    async function deleteLink(code) {
      const response = await fetch('/links/' + encodeURIComponent(code), { method: 'DELETE' });
      if (!response.ok) {
        const message = await response.text();
        throw new Error(message || 'Delete failed');
      }
      await loadLinks();
    }

    form.addEventListener('submit', async (event) => {
      event.preventDefault();
      submitButton.disabled = true;
      setStatus('Creating short URL...');
      result.classList.remove('show');

      const payload = {
        url: document.getElementById('url').value.trim(),
        custom_code: document.getElementById('custom_code').value.trim()
      };

      const expiresAt = toRFC3339(document.getElementById('expires_at').value);
      if (expiresAt) {
        payload.expires_at = expiresAt;
      }

      try {
        const response = await fetch('/shorten', {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify(payload)
        });

        if (!response.ok) {
          const message = await response.text();
          throw new Error(message || 'Create failed');
        }

        const data = await response.json();
        resultLink.href = data.short_url;
        resultLink.textContent = data.short_url;
        result.classList.add('show');
        setStatus('Short URL created.', 'ok');
        form.reset();
        await loadLinks();
      } catch (error) {
        setStatus(error.message, 'error');
      } finally {
        submitButton.disabled = false;
      }
    });

    refreshButton.addEventListener('click', async () => {
      refreshButton.disabled = true;
      try {
        await loadLinks();
      } catch (error) {
        setStatus(error.message, 'error');
      } finally {
        refreshButton.disabled = false;
      }
    });

    tableWrap.addEventListener('click', async (event) => {
      const button = event.target.closest('[data-delete]');
      if (!button) return;
      try {
        await deleteLink(button.dataset.delete);
      } catch (error) {
        setStatus(error.message, 'error');
      }
    });

    loadLinks().catch((error) => setStatus(error.message, 'error'));
  </script>
</body>
</html>`
