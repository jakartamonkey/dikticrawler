package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

const (
	BaseURL              = "https://sekolah.data.kemendikdasmen.go.id"
	ListPath             = "/v1/sekolah-service/sekolah/cari-sekolah"
	DetailPath           = "/v1/sekolah-service/sekolah/full-detail/"
	BentukPendidikanPath = "/v1/sekolah-service/referensi/bentuk-pendidikan"
	MaxPageSize          = 100   // server-enforced cap; larger values return a validation error
	MaxResultWindow      = 10000 // Elasticsearch's default max_result_window: from+size must be <= this
)

// RateLimitedError means the server returned HTTP 429.
type RateLimitedError struct{ RetryAfter string }

func (e *RateLimitedError) Error() string { return "rate limited (429)" }

// ServerError means the server returned a 5xx status — transient, worth retrying.
type ServerError struct {
	StatusCode int
	Body       string
}

func (e *ServerError) Error() string { return fmt.Sprintf("server error %d", e.StatusCode) }

// ClientError means the server returned a non-429 4xx status — retrying won't
// help (e.g. a malformed sekolah_id or a validation error on the request body).
type ClientError struct {
	StatusCode int
	Body       string
}

func (e *ClientError) Error() string {
	return fmt.Sprintf("client error %d: %s", e.StatusCode, e.Body)
}

type Client struct {
	HTTP      *http.Client
	UserAgent string
}

func NewClient(timeout time.Duration) *Client {
	return &Client{
		HTTP:      &http.Client{Timeout: timeout},
		UserAgent: "Mozilla/5.0 (compatible; sekolah-crawler/1.0; research/export tool)",
	}
}

func (c *Client) fetchRaw(ctx context.Context, method, url string, body []byte) ([]byte, error) {
	var reqBody io.Reader
	if body != nil {
		reqBody = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reqBody)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json, text/plain, */*")
	req.Header.Set("User-Agent", c.UserAgent)
	req.Header.Set("Referer", BaseURL+"/sekolah")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &RateLimitedError{RetryAfter: resp.Header.Get("Retry-After")}
	case resp.StatusCode >= 500:
		return nil, &ServerError{StatusCode: resp.StatusCode, Body: string(data)}
	case resp.StatusCode >= 400:
		return nil, &ClientError{StatusCode: resp.StatusCode, Body: string(data)}
	}
	return data, nil
}

func (c *Client) SearchSchools(ctx context.Context, req CariSekolahRequest) (*CariSekolahResponse, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	raw, err := c.fetchRaw(ctx, http.MethodPost, BaseURL+ListPath, body)
	if err != nil {
		return nil, err
	}
	var out CariSekolahResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	return &out, nil
}

// ListBentukPendidikan returns every education-level category the search
// filter accepts (SD, SMP, SMA, SMK, TK, PKBM, ...). Used to auto-shard a
// query whose plain kabupaten_kota split still isn't enough.
func (c *Client) ListBentukPendidikan(ctx context.Context) ([]BentukPendidikanItem, error) {
	raw, err := c.fetchRaw(ctx, http.MethodGet, BaseURL+BentukPendidikanPath, nil)
	if err != nil {
		return nil, err
	}
	var out BentukPendidikanResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode json: %w", err)
	}
	return out.Data, nil
}

// FullDetail returns both the parsed struct and the raw response bytes, so
// callers that want lossless output (--raw-jsonl) can persist the untouched
// body without going through the (intentionally partial) FullDetailData model.
func (c *Client) FullDetail(ctx context.Context, sekolahID string) (*FullDetailResponse, []byte, error) {
	raw, err := c.fetchRaw(ctx, http.MethodGet, BaseURL+DetailPath+sekolahID, nil)
	if err != nil {
		return nil, nil, err
	}
	var out FullDetailResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, nil, fmt.Errorf("decode json: %w", err)
	}
	return &out, raw, nil
}
