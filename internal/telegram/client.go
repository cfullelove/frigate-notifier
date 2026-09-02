package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/example/frigate-notifier/internal/config"
	"github.com/example/frigate-notifier/internal/frigate"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"strconv"
	"unicode/utf8"
)

type Notifier interface {
	SendPhoto(context.Context, []byte, string, string) (int64, error)
	SendVideo(context.Context, frigate.LocalMedia, string, int64) (int64, error)
}
type Client struct {
	c    config.Telegram
	http *http.Client
}

func New(c config.Telegram) *Client { return &Client{c, &http.Client{Timeout: c.Timeout}} }
func caption(s string) string {
	if utf8.RuneCountInString(s) <= 1024 {
		return s
	}
	r := []rune(s)
	return string(r[:1021]) + "..."
}
func (c *Client) SendPhoto(ctx context.Context, data []byte, mime, cap string) (int64, error) {
	return c.send(ctx, "sendPhoto", "photo", "snapshot.jpg", bytes.NewReader(data), cap, 0)
}
func (c *Client) SendVideo(ctx context.Context, m frigate.LocalMedia, cap string, reply int64) (int64, error) {
	f, e := os.Open(m.Path)
	if e != nil {
		return 0, e
	}
	defer f.Close()
	return c.send(ctx, "sendVideo", "video", m.Name, f, cap, reply)
}
func (c *Client) send(ctx context.Context, method, field, name string, r io.Reader, cap string, reply int64) (int64, error) {
	var b bytes.Buffer
	w := multipart.NewWriter(&b)
	_ = w.WriteField("chat_id", c.c.ChatID)
	_ = w.WriteField("caption", caption(cap))
	if reply > 0 {
		_ = w.WriteField("reply_to_message_id", strconv.FormatInt(reply, 10))
	}
	p, e := w.CreateFormFile(field, name)
	if e != nil {
		return 0, e
	}
	buf := make([]byte, 32*1024)
	for {
		n, x := r.Read(buf)
		if n > 0 {
			if _, e = p.Write(buf[:n]); e != nil {
				return 0, e
			}
		}
		if x != nil {
			if x != io.EOF {
				return 0, x
			}
			break
		}
	}
	if e = w.Close(); e != nil {
		return 0, e
	}
	req, e := http.NewRequestWithContext(ctx, "POST", "https://api.telegram.org/bot"+c.c.BotToken+"/"+method, &b)
	if e != nil {
		return 0, e
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, e := c.http.Do(req)
	if e != nil {
		return 0, e
	}
	defer resp.Body.Close()
	var x struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
		Result      struct {
			MessageID int64 `json:"message_id"`
		} `json:"result"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&x); err != nil {
		return 0, fmt.Errorf("invalid telegram response: %w", err)
	}
	if resp.StatusCode/100 != 2 || !x.OK {
		if x.Description != "" {
			return 0, fmt.Errorf("telegram request failed: HTTP %d: %s", resp.StatusCode, x.Description)
		}
		return 0, fmt.Errorf("telegram request failed: HTTP %d", resp.StatusCode)
	}
	if x.Result.MessageID <= 0 {
		return 0, fmt.Errorf("invalid telegram success response")
	}
	return x.Result.MessageID, nil
}
