package gemini

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/example/frigate-notifier/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestAnalyseErrorDoesNotExposeAPIKeyOrMedia(t *testing.T) {
	c := New(config.Gemini{APIKey: "api-secret", Model: "model", Prompt: "prompt"})
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("api-secret image-bytes")
	})
	_, err := c.Analyse(context.Background(), []byte("image-bytes"), "image/jpeg")
	if err == nil || strings.Contains(err.Error(), "api-secret") || strings.Contains(err.Error(), "image-bytes") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestAnalyseHTTPErrorIncludesSafeAPIMessage(t *testing.T) {
	c := New(config.Gemini{APIKey: "api-secret", Model: "model", Prompt: "prompt"})
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"error":{"message":"unsupported schema field"}}`)), Header: make(http.Header)}, nil
	})
	_, err := c.Analyse(context.Background(), []byte("image-bytes"), "image/jpeg")
	if err == nil || !strings.Contains(err.Error(), "unsupported schema field") || strings.Contains(err.Error(), "api-secret") || strings.Contains(err.Error(), "image-bytes") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParse(t *testing.T) {
	r, e := Parse(`{"has_person":true,"description":"visitor"}`)
	if e != nil || !r.HasPerson {
		t.Fatal(r, e)
	}
	for _, s := range []string{`{"has_person":true}`, `{"has_person":"true","description":"x"}`, `{"has_person":null,"description":"x"}`, `{"has_person":true,"description":"x","extra":1}`, "```json\n{}\n```"} {
		if _, e := Parse(s); e == nil {
			t.Fatal(s)
		}
	}
}
