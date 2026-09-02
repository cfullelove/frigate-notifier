package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"github.com/example/frigate-notifier/internal/config"
	"io"
	"net/http"
	"strings"
	"unicode/utf8"
)

type Result struct {
	HasPerson   bool   `json:"has_person"`
	Description string `json:"description"`
}
type Analyser interface {
	Analyse(context.Context, []byte, string) (Result, error)
}
type Client struct {
	c    config.Gemini
	http *http.Client
}

func New(c config.Gemini) *Client { return &Client{c, &http.Client{Timeout: c.Timeout}} }
func Parse(s string) (Result, error) {
	var raw map[string]json.RawMessage
	if json.Unmarshal([]byte(s), &raw) != nil {
		return Result{}, fmt.Errorf("analysis is not strict JSON")
	}
	if len(raw) != 2 {
		return Result{}, fmt.Errorf("analysis must contain exactly the required fields")
	}
	var r Result
	v, ok := raw["has_person"]
	if !ok || (string(v) != "true" && string(v) != "false") || json.Unmarshal(v, &r.HasPerson) != nil {
		return r, fmt.Errorf("missing boolean has_person")
	}
	v, ok = raw["description"]
	if !ok || json.Unmarshal(v, &r.Description) != nil || strings.TrimSpace(r.Description) == "" {
		return r, fmt.Errorf("missing description")
	}
	if utf8.RuneCountInString(r.Description) > 4096 {
		return r, fmt.Errorf("description too long")
	}
	return r, nil
}
func (c *Client) Analyse(ctx context.Context, image []byte, mime string) (Result, error) {
	body := map[string]any{"contents": []any{map[string]any{"parts": []any{map[string]any{"text": c.c.Prompt}, map[string]any{"inline_data": map[string]string{"mime_type": mime, "data": base64.StdEncoding.EncodeToString(image)}}}}}, "generationConfig": map[string]any{"responseMimeType": "application/json", "responseSchema": map[string]any{"type": "OBJECT", "properties": map[string]any{"has_person": map[string]string{"type": "BOOLEAN"}, "description": map[string]string{"type": "STRING"}}, "required": []string{"has_person", "description"}, "additionalProperties": false}}}
	b, _ := json.Marshal(body)
	u := "https://generativelanguage.googleapis.com/v1beta/models/" + c.c.Model + ":generateContent?key=" + c.c.APIKey
	r, e := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(b))
	if e != nil {
		return Result{}, e
	}
	r.Header.Set("Content-Type", "application/json")
	resp, e := c.http.Do(r)
	if e != nil {
		return Result{}, e
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return Result{}, fmt.Errorf("gemini returned HTTP %d", resp.StatusCode)
	}
	var out struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text string `json:"text"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if e = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&out); e != nil || len(out.Candidates) != 1 || len(out.Candidates[0].Content.Parts) != 1 || out.Candidates[0].Content.Parts[0].Text == "" {
		return Result{}, fmt.Errorf("invalid gemini response")
	}
	return Parse(out.Candidates[0].Content.Parts[0].Text)
}
