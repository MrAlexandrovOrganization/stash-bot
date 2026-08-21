package stash

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// defaultTimeout bounds metadata operations (search, get, delete, update).
	defaultTimeout = 30 * time.Second
	// fileTimeout bounds bulk transfers (upload/download of the media itself).
	fileTimeout = 5 * time.Minute
)

type Client struct {
	baseURL     string
	http        *http.Client
	timeout     time.Duration
	fileTimeout time.Duration
}

func NewClient(baseURL string) *Client {
	return &Client{
		baseURL:     strings.TrimRight(baseURL, "/"),
		http:        &http.Client{},
		timeout:     defaultTimeout,
		fileTimeout: fileTimeout,
	}
}

// withTimeout derives a deadline-bounded context from ctx. If ctx already has a
// deadline (or is cancelled) it is reused unchanged, otherwise a new timeout is
// applied so a slow/hanging backend cannot block the bot forever.
func (c *Client) withTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}

func (c *Client) Upload(ctx context.Context, r io.Reader, fileName, contentType string, size int64, meta UploadMeta) (*Item, error) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)

	go func() {
		defer pw.Close()

		fw, err := mw.CreateFormFile("file", fileName)
		if err != nil {
			pw.CloseWithError(err)
			return
		}
		if _, err := io.Copy(fw, r); err != nil {
			pw.CloseWithError(err)
			return
		}
		if meta.Description != "" {
			_ = mw.WriteField("description", meta.Description)
		}
		if len(meta.Tags) > 0 {
			_ = mw.WriteField("tags", strings.Join(meta.Tags, ","))
		}
		if meta.Source != "" {
			_ = mw.WriteField("source", meta.Source)
		}
		if meta.OriginalCaption != "" {
			_ = mw.WriteField("original_caption", meta.OriginalCaption)
		}
		mw.Close()
	}()

	ctx, cancel := c.withTimeout(ctx, c.fileTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/items", pr)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())

	return decodeItem(c.http.Do(req))
}

func (c *Client) Search(ctx context.Context, q SearchQuery) ([]*Item, error) {
	params := url.Values{}
	if q.Text != "" {
		params.Set("q", q.Text)
	}
	if len(q.Tags) > 0 {
		params.Set("tags", strings.Join(q.Tags, ","))
	}

	ctx, cancel := c.withTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/items?"+params.Encode(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("search: status %d", resp.StatusCode)
	}

	var items []*Item
	return items, json.NewDecoder(resp.Body).Decode(&items)
}

func (c *Client) Get(ctx context.Context, id string) (*Item, error) {
	ctx, cancel := c.withTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/items/"+id, nil)
	if err != nil {
		return nil, err
	}
	return decodeItem(c.http.Do(req))
}

func (c *Client) GetFile(ctx context.Context, id string) (io.ReadCloser, *Item, error) {
	ctx, cancel := c.withTimeout(ctx, c.fileTimeout)
	defer cancel()

	item, err := c.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/items/"+id+"/file", nil)
	if err != nil {
		return nil, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("get file: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, nil, fmt.Errorf("get file: status %d", resp.StatusCode)
	}
	return resp.Body, item, nil
}

func (c *Client) Delete(ctx context.Context, id string) error {
	ctx, cancel := c.withTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, c.baseURL+"/items/"+id, nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		return fmt.Errorf("delete: status %d", resp.StatusCode)
	}
	return nil
}

func (c *Client) Update(ctx context.Context, id string, meta UpdateMeta) (*Item, error) {
	ctx, cancel := c.withTimeout(ctx, c.timeout)
	defer cancel()

	body, err := json.Marshal(meta)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPatch, c.baseURL+"/items/"+id, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return decodeItem(c.http.Do(req))
}

func decodeItem(resp *http.Response, err error) (*Item, error) {
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("not found")
	}
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	var item Item
	return &item, json.NewDecoder(resp.Body).Decode(&item)
}
