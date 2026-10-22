package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
)

// Bot API limits: caption 1024, message 4096, upload 50 MB, album 10.
const (
	tgCaptionMax = 1024
	tgTextMax    = 4096
	tgUploadMax  = 50 << 20
	tgAlbumMax   = 10

	tgAPI = "https://api.telegram.org"
)

// Telegram posts to a channel through a bot that is its admin. Chat is
// @channelname or the numeric -100… id.
type Telegram struct {
	Token string
	Chat  string
	API   string // tgAPI; a fake server in tests
	Route routeFunc
}

func (t *Telegram) Name() string { return "tg" }

func (t *Telegram) Publish(ctx context.Context, p *Post) (Result, error) {
	text := p.PublicText()
	if n := textLen(text); n > tgTextMax {
		return Result{}, fmt.Errorf("text %d chars > %d", n, tgTextMax)
	}
	if len(p.Media) > tgAlbumMax {
		return Result{}, fmt.Errorf("%d media > album limit %d", len(p.Media), tgAlbumMax)
	}
	for _, m := range p.Media {
		st, err := os.Stat(m)
		if err != nil {
			return Result{}, err
		}
		if st.Size() > tgUploadMax {
			return Result{}, fmt.Errorf("%s: %d MB > 50 MB bot upload limit", m, st.Size()>>20)
		}
	}

	if err := t.Route(ctx, t.API, viaVPN); err != nil {
		return Result{}, err
	}

	htmlText := toHTML(text)
	if len(p.Media) == 0 {
		return t.sendText(ctx, htmlText)
	}
	// A long text can't be a caption: the album goes first, the text
	// follows as its own message, and the text is what the result points at.
	caption := htmlText
	if textLen(text) > tgCaptionMax {
		caption = ""
	}
	res, err := t.sendMedia(ctx, p.Media, caption)
	if err != nil || caption != "" {
		return res, err
	}
	return t.sendText(ctx, htmlText)
}

type tgMessage struct {
	MessageID int64 `json:"message_id"`
}

type tgReply[T any] struct {
	OK          bool   `json:"ok"`
	Description string `json:"description"`
	Result      T      `json:"result"`
}

func (t *Telegram) call(ctx context.Context, method string, fields map[string]string, files []formFile, out any) error {
	body, ctype, err := multipartBody(fields, files)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		t.API+"/bot"+t.Token+"/"+method, body)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", ctype)
	if err := doJSON(req, out); err != nil {
		// The URL carries the token; keep it out of logs.
		return errors.New(strings.ReplaceAll(err.Error(), t.Token, "<token>"))
	}
	return nil
}

func (t *Telegram) sendText(ctx context.Context, htmlText string) (Result, error) {
	var r tgReply[tgMessage]
	err := t.call(ctx, "sendMessage", map[string]string{
		"chat_id": t.Chat, "text": htmlText, "parse_mode": "HTML",
	}, nil, &r)
	if err != nil {
		return Result{}, err
	}
	return t.result(r.Result.MessageID), nil
}

func (t *Telegram) sendMedia(ctx context.Context, media []string, caption string) (Result, error) {
	if len(media) == 1 {
		method, field := "sendPhoto", "photo"
		if kindOf(media[0]) == mediaVideo {
			method, field = "sendVideo", "video"
		}
		fields := map[string]string{"chat_id": t.Chat}
		if caption != "" {
			fields["caption"], fields["parse_mode"] = caption, "HTML"
		}
		if field == "video" {
			fields["supports_streaming"] = "true"
		}
		var r tgReply[tgMessage]
		if err := t.call(ctx, method, fields, []formFile{{field, media[0]}}, &r); err != nil {
			return Result{}, err
		}
		return t.result(r.Result.MessageID), nil
	}

	type item struct {
		Type      string `json:"type"`
		Media     string `json:"media"`
		Caption   string `json:"caption,omitzero"`
		ParseMode string `json:"parse_mode,omitzero"`
	}
	items := make([]item, len(media))
	files := make([]formFile, len(media))
	for i, m := range media {
		name := "f" + strconv.Itoa(i)
		items[i] = item{Type: map[mediaKind]string{mediaImage: "photo", mediaVideo: "video"}[kindOf(m)], Media: "attach://" + name}
		files[i] = formFile{name, m}
	}
	if caption != "" {
		items[0].Caption, items[0].ParseMode = caption, "HTML"
	}
	js, err := json.Marshal(items)
	if err != nil {
		return Result{}, err
	}
	var r tgReply[[]tgMessage]
	if err := t.call(ctx, "sendMediaGroup", map[string]string{"chat_id": t.Chat, "media": string(js)}, files, &r); err != nil {
		return Result{}, err
	}
	if len(r.Result) == 0 {
		return Result{}, errors.New("sendMediaGroup: empty result")
	}
	return t.result(r.Result[0].MessageID), nil
}

func (t *Telegram) result(id int64) Result {
	r := Result{ID: strconv.FormatInt(id, 10)}
	if name, ok := strings.CutPrefix(t.Chat, "@"); ok {
		r.URL = "https://t.me/" + name + "/" + r.ID
	}
	return r
}
