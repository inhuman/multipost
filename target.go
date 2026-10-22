package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Target publishes a post on one platform and returns where it landed.
type Target interface {
	Name() string
	Publish(ctx context.Context, p *Post) (Result, error)
}

type Result struct {
	ID  string `json:"id,omitzero"`
	URL string `json:"url,omitzero"`
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
		return fmt.Errorf("decode %s reply: %w (%.300s)", req.URL.Host, err, body)
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
