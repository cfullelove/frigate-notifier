package frigate

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/example/frigate-notifier/internal/config"
)

const maxMedia = 50 << 20

type LocalMedia struct {
	Path, Name, MIME string
	Size             int64
}

func (m LocalMedia) Cleanup() { _ = os.Remove(m.Path) }

type Client struct {
	c    config.Frigate
	http *http.Client
}

func New(c config.Frigate) *Client {
	return &Client{c: c, http: &http.Client{Timeout: c.RequestTimeout}}
}

func (c *Client) Snapshot(ctx context.Context, id string) ([]byte, string, error) {
	query := ""
	if c.c.Snapshot.Quality != nil {
		query = "?quality=" + strconv.Itoa(*c.c.Snapshot.Quality)
	}
	var data []byte
	var mime string
	err := c.retry(ctx, c.c.Snapshot.Retries, c.c.Snapshot.RetryDelay, func() (bool, error) {
		resp, err := c.request(ctx, "/api/events/"+url.PathEscape(id)+"/snapshot.jpg"+query)
		if err != nil {
			return true, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return transient(resp.StatusCode), fmt.Errorf("frigate returned HTTP %d", resp.StatusCode)
		}
		mime = mediaType(resp.Header.Get("Content-Type"))
		if !strings.HasPrefix(mime, "image/") {
			return false, fmt.Errorf("unexpected snapshot content type %q", mime)
		}
		data, err = readLimited(resp.Body)
		if err != nil {
			return false, fmt.Errorf("frigate snapshot read failed")
		}
		return false, nil
	})
	return data, mime, err
}

func (c *Client) Clip(ctx context.Context, camera, start, end string) (LocalMedia, error) {
	ctx, cancel := context.WithTimeout(ctx, c.c.Clip.Timeout)
	defer cancel()
	var media LocalMedia
	err := c.retry(ctx, c.c.Clip.Retries, c.c.Clip.RetryDelay, func() (bool, error) {
		resp, err := c.request(ctx, "/api/"+url.PathEscape(camera)+"/start/"+url.PathEscape(start)+"/end/"+url.PathEscape(end)+"/clip.mp4")
		if err != nil {
			return true, err
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return transient(resp.StatusCode), fmt.Errorf("frigate returned HTTP %d", resp.StatusCode)
		}
		mime := mediaType(resp.Header.Get("Content-Type"))
		if !strings.HasPrefix(mime, "video/") && mime != "application/octet-stream" {
			return false, fmt.Errorf("unexpected clip content type %q", mime)
		}
		if resp.ContentLength > maxMedia {
			return false, fmt.Errorf("media too large")
		}
		file, err := os.CreateTemp("", "frigate-*.mp4")
		if err != nil {
			return false, fmt.Errorf("frigate clip temporary file creation failed")
		}
		name := file.Name()
		size, copyErr := io.Copy(file, io.LimitReader(resp.Body, maxMedia+1))
		closeErr := file.Close()
		if copyErr != nil || closeErr != nil || size > maxMedia {
			_ = os.Remove(name)
			if copyErr != nil {
				return false, fmt.Errorf("frigate clip download failed")
			}
			if closeErr != nil {
				return false, fmt.Errorf("frigate clip temporary file close failed")
			}
			return false, fmt.Errorf("media too large")
		}
		media = LocalMedia{Path: name, Name: path.Base(name), MIME: mime, Size: size}
		return false, nil
	})
	return media, err
}

func (c *Client) request(ctx context.Context, suffix string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.c.BaseURL+suffix, nil)
	if err != nil {
		return nil, fmt.Errorf("frigate request creation failed")
	}
	if c.c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.c.Token)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("frigate request failed")
	}
	return resp, nil
}

func (c *Client) retry(ctx context.Context, retries int, delay time.Duration, attempt func() (bool, error)) error {
	var last error
	for i := 0; i <= retries; i++ {
		if i > 0 {
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		shouldRetry, err := attempt()
		if err == nil {
			return nil
		}
		last = err
		if !shouldRetry {
			return err
		}
	}
	return last
}

func readLimited(r io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(r, maxMedia+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxMedia {
		return nil, fmt.Errorf("media too large")
	}
	return data, nil
}

func mediaType(value string) string {
	return strings.ToLower(strings.TrimSpace(strings.Split(value, ";")[0]))
}
func transient(status int) bool {
	return status == http.StatusRequestTimeout || status == http.StatusTooEarly || status == http.StatusTooManyRequests || status >= 500
}
