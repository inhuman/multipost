package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"
)

// Target publishes a post on one platform and returns where it landed.
type Target interface {
	Name() string
	Publish(ctx context.Context, p *Post) (Result, error)
}

// historyFinder is a platform that can look at its own recent posts: after
// a crash or a lost answer the journal says pending, and only the platform
// knows whether the post went out. sinceID is the newest message id the
// journal knows for this platform.
type historyFinder interface {
	FindPost(ctx context.Context, sinceID int64, hash string) (Result, bool, error)
}

// cleaner removes the part of a half-sent post before it is sent again.
type cleaner interface {
	Cleanup(ctx context.Context, ids []string) error
}

// halfSentError: part of the post went out; Sent lists its message ids.
type halfSentError struct {
	Sent []string
	Err  error
}

func (e *halfSentError) Error() string { return fmt.Sprintf("half sent %v: %v", e.Sent, e.Err) }
func (e *halfSentError) Unwrap() error { return e.Err }

// errAmbiguous: the platform said 2xx but the answer is unreadable — the
// post most likely went out.
var errAmbiguous = errors.New("answer unreadable after success")

type attemptResult string

const (
	resultOK       attemptResult = "ok"
	resultRejected attemptResult = "rejected"
	resultLost     attemptResult = "lost"
	resultHalfSent attemptResult = "halfSent"
)

// classify tells what an error from Publish says about the platform: a
// transport error or an unreadable success may hide a post that went out,
// anything else was refused before or by the platform.
func classify(err error) attemptResult {
	switch {
	case err == nil:
		return resultOK
	case errors.Is(err, errAmbiguous):
		return resultLost
	}
	if _, ok := errors.AsType[*halfSentError](err); ok {
		return resultHalfSent
	}
	if _, ok := errors.AsType[*url.Error](err); ok {
		return resultLost
	}
	return resultRejected
}

type Result struct {
	ID  string `json:"id,omitzero"`
	URL string `json:"url,omitzero"`

	sent []string // every message id the post consists of (an album is several)
}

// Uploads of a 50 MB video over a home uplink take minutes.
var httpClient = &http.Client{Timeout: 10 * time.Minute}

type formFile struct {
	field, path string
}

func multipartBody(fields map[string]string, files []formFile) (*bytes.Buffer, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := w.WriteField(k, v); err != nil {
			return nil, "", err
		}
	}
	for _, f := range files {
		part, err := w.CreateFormFile(f.field, filepath.Base(f.path))
		if err != nil {
			return nil, "", err
		}
		src, err := os.Open(f.path)
		if err != nil {
			return nil, "", err
		}
		_, err = io.Copy(part, src)
		src.Close()
		if err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// doRaw sends req and returns the reply body; a non-2xx status becomes an
// error carrying the body, which is where the platforms explain themselves.
func doRaw(req *http.Request) ([]byte, error) {
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &httpError{Status: resp.StatusCode, Body: string(body)}
	}
	return body, nil
}

func doJSON(req *http.Request, out any) error {
	body, err := doRaw(req)
	if err != nil || out == nil {
		return err
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("%w: decode %s reply: %w (%.300s)", errAmbiguous, req.URL.Host, err, body)
	}
	return nil
}

type httpError struct {
	Status int
	Body   string
}

func (e *httpError) Error() string {
	return fmt.Sprintf("HTTP %d: %.500s", e.Status, e.Body)
}
