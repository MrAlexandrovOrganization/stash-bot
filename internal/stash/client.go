package stash

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
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
		http:        &http.Client{Transport: otelhttp.NewTransport(http.DefaultTransport)},
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

		ct := contentType
		if ct == "" {
			ct = "application/octet-stream"
		}
		partHeader := make(textproto.MIMEHeader)
		partHeader.Set("Content-Disposition",
			fmt.Sprintf(`form-data; name="file"; filename="%s"`, fileName))
		partHeader.Set("Content-Type", ct)
		fw, err := mw.CreatePart(partHeader)
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
		if meta.FileUniqueID != "" {
			_ = mw.WriteField("file_unique_id", meta.FileUniqueID)
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

// Similar runs a similarity search against the backend. It uses a multipart
// POST when ImageBytes is provided (searching for media visually similar to the
// uploaded bytes), otherwise a GET with q= and/or item= query parameters.
func (c *Client) Similar(ctx context.Context, req SimilarRequest) ([]*Item, error) {
	if len(req.ImageBytes) > 0 {
		return c.similarByImage(ctx, req)
	}
	return c.similarByQuery(ctx, req)
}

func (c *Client) similarByQuery(ctx context.Context, req SimilarRequest) ([]*Item, error) {
	params := url.Values{}
	if req.Text != "" {
		params.Set("q", req.Text)
	}
	if req.ItemID != "" {
		params.Set("item", req.ItemID)
	}
	if req.Limit > 0 {
		params.Set("limit", strconv.Itoa(req.Limit))
	}
	if req.Offset > 0 {
		params.Set("offset", strconv.Itoa(req.Offset))
	}

	ctx, cancel := c.withTimeout(ctx, c.timeout)
	defer cancel()

	reqURL := c.baseURL + "/similar"
	if len(params) > 0 {
		reqURL += "?" + params.Encode()
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("similar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("similar: status %d", resp.StatusCode)
	}

	var items []*Item
	return items, json.NewDecoder(resp.Body).Decode(&items)
}

func (c *Client) similarByImage(ctx context.Context, req SimilarRequest) ([]*Item, error) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile("image", "image.jpg")
	if err != nil {
		return nil, err
	}
	if _, err := fw.Write(req.ImageBytes); err != nil {
		return nil, err
	}
	if req.Limit > 0 {
		_ = mw.WriteField("limit", strconv.Itoa(req.Limit))
	}
	mw.Close()

	ctx, cancel := c.withTimeout(ctx, c.fileTimeout)
	defer cancel()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/similar", &buf)
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())

	resp, err := c.http.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("similar: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("similar: status %d", resp.StatusCode)
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

// cancelReadCloser ties the request-context cancellation to body closing, so
// the caller can stream the body AFTER GetFile has returned (a plain defer
// cancel() would kill the transfer mid-read).
type cancelReadCloser struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (c *cancelReadCloser) Close() error {
	err := c.ReadCloser.Close()
	c.cancel()
	return err
}

func (c *Client) GetFile(ctx context.Context, id string) (io.ReadCloser, *Item, error) {
	ctx, cancel := c.withTimeout(ctx, c.fileTimeout)

	item, err := c.Get(ctx, id)
	if err != nil {
		cancel()
		return nil, nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/items/"+id+"/file", nil)
	if err != nil {
		cancel()
		return nil, nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("get file: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, nil, fmt.Errorf("get file: status %d", resp.StatusCode)
	}
	return &cancelReadCloser{ReadCloser: resp.Body, cancel: cancel}, item, nil
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

// Describe asks the backend to (re)generate the AI description for one item.
// Generation runs asynchronously on the backend; poll Get until the item has
// either AIDescription or AIDescriptionError set.
func (c *Client) Describe(ctx context.Context, id string) (*Item, error) {
	ctx, cancel := c.withTimeout(ctx, c.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/items/"+id+"/describe", nil)
	if err != nil {
		return nil, err
	}
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
