package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"
)

type URLShortener struct {
	mu    sync.RWMutex
	urls  map[string]string // short -> long
	codes map[string]string //long -> short
}

func NewURLShortener() *URLShortener {
	return &URLShortener{
		urls:  make(map[string]string),
		codes: make(map[string]string),
	}
}

const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
const codeLength = 7

func (s *URLShortener) generateRandomCode() string {
	rand.Seed(time.Now().UnixNano())
	b := make([]byte, codeLength)
	for i := range b {
		b[i] = charset[rand.Intn(len(charset))]
	}
	return string(b)
}

func (s *URLShortener) ShortenURL(longURL string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if code, exists := s.codes[longURL]; exists {
		return code, nil
	}

	//generate unique code
	var code string
	for {
		code = s.generateRandomCode()
		if _, exists := s.urls[code]; !exists {
			break
		}
	}

	s.urls[code] = longURL
	s.codes[longURL] = code
	return code, nil
}
func (s *URLShortener) Resolve(code string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	longURL, exists := s.urls[code]
	return longURL, exists
}

type ShortenRequest struct {
	URL string `json:"url"`
}

type ShortenResponse struct {
	ShortURL string `json:"short_url"`
}

func main() {
	shortener := NewURLShortener()
	baseURL := "http://localhost:8080"

	http.HandleFunc("/shorten", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "POST" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var req ShortenRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		code, err := shortener.ShortenURL(req.URL)
		if err != nil {
			http.Error(w, "Failed to shorten URL", http.StatusInternalServerError)
			return
		}

		resp := ShortenResponse{
			ShortURL: baseURL + "/" + code,
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	})

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		code := strings.TrimPrefix(r.URL.Path, "/")
		if code == "" {
			http.Error(w, "Invalid request", http.StatusBadRequest)
			return
		}

		longURL, exists := shortener.Resolve(code)
		if !exists {
			http.Error(w, "URL not found", http.StatusNotFound)
			return
		}

		http.Redirect(w, r, longURL, http.StatusSeeOther)
	})

	fmt.Println("Starting server on :8080")
	if err := http.ListenAndServe(":8080", nil); err != nil {
		log.Fatal("Error starting server: ", err)
	}
}
