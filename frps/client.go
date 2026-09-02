// Package frps is a client for the planned whitelist API documented in
// docs/api.md. The server-side API is implemented in the custom frp fork.
package frps

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"frps-gateway/duration"
)

// Entry is one whitelist entry on frps.
type Entry struct {
	IP       string `json:"ip"`
	ExpireAt int64  `json:"expireAt"` // unix seconds
}

// v2Response is the standard envelope used by frps dashboard API v2.
type v2Response struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

type Client struct {
	baseURL  string
	user     string
	password string
	hc       *http.Client
}

func New(apiAddr, user, password string) *Client {
	return &Client{
		baseURL:  strings.TrimRight(apiAddr, "/"),
		user:     user,
		password: password,
		hc:       &http.Client{Timeout: 15 * time.Second},
	}
}

// List returns all whitelist entries.
func (c *Client) List(ctx context.Context) ([]Entry, error) {
	var entries []Entry
	if err := c.do(ctx, http.MethodGet, "/api/v2/whitelist", nil, &entries); err != nil {
		return nil, err
	}
	return entries, nil
}

// Add adds ip with the given ttl. It returns the stored entry; if frps replies
// without a body the expiry is computed locally.
func (c *Client) Add(ctx context.Context, ip string, ttl time.Duration) (Entry, error) {
	if ttl <= 0 {
		return Entry{}, fmt.Errorf("ttl must be positive")
	}
	body := map[string]string{"ip": ip, "ttl": duration.Format(ttl)}
	var entry Entry
	if err := c.do(ctx, http.MethodPost, "/api/v2/whitelist", body, &entry); err != nil {
		return Entry{}, err
	}
	if entry.IP == "" {
		entry = Entry{IP: ip, ExpireAt: time.Now().Add(ttl).Unix()}
	}
	return entry, nil
}

// Remove deletes ip from the whitelist; a 404 means it was not present.
func (c *Client) Remove(ctx context.Context, ip string) error {
	body := map[string]string{"ip": ip}
	return c.do(ctx, http.MethodDelete, "/api/v2/whitelist", body, nil)
}

func (c *Client) do(ctx context.Context, method, path string, body any, out any) error {
	var rd io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, rd)
	if err != nil {
		return err
	}
	req.SetBasicAuth(c.user, c.password)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(data))}
	}
	if len(data) == 0 {
		return fmt.Errorf("decode response: empty API v2 envelope")
	}
	var envelope v2Response
	if err := json.Unmarshal(data, &envelope); err != nil {
		return fmt.Errorf("decode API v2 envelope: %w", err)
	}
	if envelope.Code < 200 || envelope.Code >= 300 {
		return &APIError{Status: envelope.Code, Body: envelope.Msg}
	}
	if out != nil && len(envelope.Data) > 0 && string(envelope.Data) != "null" {
		if err := json.Unmarshal(envelope.Data, out); err != nil {
			return fmt.Errorf("decode API v2 data: %w", err)
		}
	}
	return nil
}

// APIError carries a non-2xx frps response.
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("frps API status %d: %s", e.Status, e.Body)
}

// NotFound reports whether frps answered 404, which for Remove means
// the IP is not in the whitelist.
func (e *APIError) NotFound() bool { return e.Status == http.StatusNotFound }
