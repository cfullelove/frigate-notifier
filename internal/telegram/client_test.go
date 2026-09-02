package telegram

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/example/frigate-notifier/internal/config"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func testClient(body string, status int) *Client {
	c := New(config.Telegram{BotToken: "token", ChatID: "chat", Timeout: time.Second})
	c.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header), Request: r}, nil
	})
	return c
}

func TestSendPhotoErrorDoesNotExposeTokenOrMedia(t *testing.T) {
	c := New(config.Telegram{BotToken: "bot-secret", ChatID: "chat", Timeout: time.Second})
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	})
	_, err := c.SendPhoto(context.Background(), []byte("private-image"), "image/jpeg", "caption")
	if err == nil || strings.Contains(err.Error(), "bot-secret") || strings.Contains(err.Error(), "private-image") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestSendPhotoRejectsUnreadableAndAPIFailureResponses(t *testing.T) {
	for _, c := range []*Client{
		testClient("not json", http.StatusOK),
		testClient(`{"ok":false,"description":"bad request"}`, http.StatusOK),
		testClient(`{"ok":true,"result":{}}`, http.StatusOK),
	} {
		if _, err := c.SendPhoto(context.Background(), []byte("image"), "image/jpeg", "caption"); err == nil {
			t.Fatal("accepted invalid Telegram response")
		}
	}
}

func TestSendPhotoRequiresHTTPAndOK(t *testing.T) {
	c := testClient(`{"ok":true,"result":{"message_id":42}}`, http.StatusInternalServerError)
	if _, err := c.SendPhoto(context.Background(), []byte("image"), "image/jpeg", "caption"); err == nil {
		t.Fatal("accepted non-2xx Telegram response")
	}
}
