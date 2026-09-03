package frigate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example/frigate-notifier/internal/config"
)

func testClient(url string) *Client {
	return New(config.Frigate{BaseURL: url, RequestTimeout: time.Second, Snapshot: config.Retry{RetryDelay: time.Millisecond}, Clip: config.Clip{Source: "clip", RetryDelay: time.Millisecond, Timeout: time.Second}})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSnapshotErrorDoesNotExposeToken(t *testing.T) {
	c := testClient("http://example.invalid")
	c.c.Token = "frigate-secret"
	c.http.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("frigate-secret request details")
	})
	_, _, err := c.Snapshot(context.Background(), "event")
	if err == nil || strings.Contains(err.Error(), "frigate-secret") {
		t.Fatalf("unsafe error: %v", err)
	}
}

func TestSnapshotRequiresImageAndEscapesID(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.EscapedPath(), "a%2Fb") {
			t.Fatalf("event ID was not escaped: %s", r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte("not an image"))
	}))
	defer server.Close()
	if _, _, err := testClient(server.URL).Snapshot(context.Background(), "a/b"); err == nil {
		t.Fatal("accepted non-image snapshot")
	}
}

func TestClipUsesPreviewSource(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/camera/start/1/end/2/preview.mp4" {
			t.Fatalf("unexpected preview path: %s", r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write([]byte("preview"))
	}))
	defer server.Close()
	c := testClient(server.URL)
	c.c.Clip.Source = "preview"
	media, err := c.Clip(context.Background(), "camera", "1", "2")
	if err != nil {
		t.Fatal(err)
	}
	defer media.Cleanup()
}

func TestClipStreamsToTemporaryFileAndCleansUp(t *testing.T) {
	payload := []byte("video data")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.EscapedPath() != "/api/front%20door/start/1.25/end/2.5/clip.mp4" {
			t.Fatalf("unexpected clip path: %s", r.URL.EscapedPath())
		}
		w.Header().Set("Content-Type", "video/mp4; charset=binary")
		_, _ = w.Write(payload)
	}))
	defer server.Close()
	media, err := testClient(server.URL).Clip(context.Background(), "front door", "1.25", "2.5")
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(media.Path)
	if err != nil || string(data) != string(payload) {
		t.Fatalf("temporary clip = %q, %v", data, err)
	}
	media.Cleanup()
	if _, err := os.Stat(media.Path); !os.IsNotExist(err) {
		t.Fatalf("temporary clip was not deleted: %v", err)
	}
}
