package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// Max posts to a MAX channel through a bot that is its admin
// (dev.max.ru/docs-api, checked 22.10.2026).
type Max struct {
	Token  string
	ChatID int64
	API    string // maxAPI; a fake server in tests
	Route  routeFunc
}

const (
	maxAPI     = "https://platform-api2.max.ru"
	maxTextMax = 4000
)

func (m *Max) Name() string { return "max" }

func (m *Max) Publish(ctx context.Context, p *Post) (Result, error) {
	text := p.PublicText()
	if n := textLen(text); n > maxTextMax {
		return Result{}, fmt.Errorf("text %d chars > %d", n, maxTextMax)
	}
	if err := m.Route(ctx, m.API, viaDirect); err != nil {
		return Result{}, err
	}

	attachments := make([]maxAttachment, 0, len(p.Media))
	for _, path := range p.Media {
		a, err := m.upload(ctx, path)
		if err != nil {
			return Result{}, fmt.Errorf("upload %s: %w", path, err)
		}
		attachments = append(attachments, a)
	}

	body, err := json.Marshal(map[string]any{
		"text": toHTML(text), "format": "html", "attachments": attachments,
	})
	if err != nil {
		return Result{}, err
	}
	// Uploaded media is processed asynchronously; until it is, sending
	// answers attachment.not.ready.
	var reply struct {
		Message struct {
			URL  string `json:"url"`
			Body struct {
				Mid string `json:"mid"`
			} `json:"body"`
		} `json:"message"`
	}
	for wait := 2 * time.Second; ; wait *= 2 {
		err = m.do(ctx, http.MethodPost, "/messages?chat_id="+strconv.FormatInt(m.ChatID, 10), body, &reply)
		if !isNotReady(err) || wait > time.Minute {
			break
		}
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		case <-time.After(wait):
		}
	}
	if err != nil {
		return Result{}, err
	}
	return Result{ID: reply.Message.Body.Mid, URL: reply.Message.URL}, nil
}

func isNotReady(err error) bool {
	he, ok := errors.AsType[*httpError](err)
	return ok && strings.Contains(he.Body, "attachment.not.ready")
}

type maxAttachment struct {
	Type    string          `json:"type"`
	Payload json.RawMessage `json:"payload"`
}

// upload: POST /uploads gives an upload URL (and, for video, the token);
// the file goes there as multipart field "data". For images the token
// comes back from the upload server instead, as {"photos": {...}} — that
// reply is the attachment payload as is.
func (m *Max) upload(ctx context.Context, path string) (maxAttachment, error) {
	kind := kindOf(path)
	var slot struct {
		URL   string `json:"url"`
		Token string `json:"token"`
	}
	if err := m.do(ctx, http.MethodPost, "/uploads?type="+string(kind), nil, &slot); err != nil {
		return maxAttachment{}, err
	}
	if err := m.Route(ctx, slot.URL, viaDirect); err != nil {
		return maxAttachment{}, err
	}
	body, ctype, err := multipartBody(nil, []formFile{{"data", path}})
	if err != nil {
		return maxAttachment{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, slot.URL, body)
	if err != nil {
		return maxAttachment{}, err
	}
	req.Header.Set("Content-Type", ctype)
	req.Header.Set("Authorization", m.Token)
	uploaded, err := doRaw(req)
	if err != nil {
		return maxAttachment{}, err
	}
	// Video: the slot token is the payload, the upload reply may be any body.
	if slot.Token != "" {
		payload, err := json.Marshal(map[string]string{"token": slot.Token})
		if err != nil {
			return maxAttachment{}, err
		}
		return maxAttachment{Type: string(kind), Payload: payload}, nil
	}
	if !json.Valid(uploaded) || !bytes.Contains(uploaded, []byte(`"token"`)) {
		return maxAttachment{}, fmt.Errorf("upload reply has no token: %.300s", uploaded)
	}
	return maxAttachment{Type: string(kind), Payload: uploaded}, nil
}

func (m *Max) do(ctx context.Context, method, path string, body []byte, out any) error {
	req, err := http.NewRequestWithContext(ctx, method, m.API+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", m.Token)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	return doJSON(req, out)
}

// FindPost looks for the post among the channel's latest 100 messages
// (the API's maximum; sinceID is not needed, MAX lists history itself).
func (m *Max) FindPost(ctx context.Context, _ int64, hash string) (Result, bool, error) {
	var reply struct {
		Messages []struct {
			URL  string `json:"url"`
			Body struct {
				Mid  string `json:"mid"`
				Text string `json:"text"`
			} `json:"body"`
		} `json:"messages"`
	}
	path := "/messages?count=100&chat_id=" + strconv.FormatInt(m.ChatID, 10)
	if err := m.do(ctx, http.MethodGet, path, nil, &reply); err != nil {
		return Result{}, false, err
	}
	for _, msg := range reply.Messages {
		if textHash(msg.Body.Text) == hash {
			return Result{ID: msg.Body.Mid, URL: msg.URL}, true, nil
		}
	}
	return Result{}, false, nil
}

// Chats lists the chats and channels the bot is in, to find MAX_CHAT_ID.
func (m *Max) Chats(ctx context.Context) ([]maxChat, error) {
	var reply struct {
		Chats []maxChat `json:"chats"`
	}
	if err := m.do(ctx, http.MethodGet, "/chats", nil, &reply); err != nil {
		return nil, err
	}
	return reply.Chats, nil
}

type maxChat struct {
	ChatID int64  `json:"chat_id"`
	Type   string `json:"type"`
	Title  string `json:"title"`
	Link   string `json:"link"`
}
