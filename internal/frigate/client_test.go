package frigate

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/example/frigate-notifier/internal/config"
)

func testClient(url string) *Client {
	return New(config.Frigate{BaseURL: url, RequestTimeout: time.Second, Snapshot: config.Retry{RetryDelay: time.Millisecond}, Clip: config.Clip{RetryDelay: time.Millisecond, Timeout: time.Second}})
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

func TestClipStreamsToTemporaryFileAndCleansUp(t *testing.T) {
	payload := []byte("video data")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
