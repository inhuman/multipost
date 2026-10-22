package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"unicode/utf16"
)

// Bot API limits: caption 1024, message 4096, upload 50 MB, album 10.
const (
	tgCaptionMax = 1024
	tgTextMax    = 4096
	tgUploadMax  = 50 << 20
	tgAlbumMax   = 10

	tgAPI = "https://api.telegram.org"

	tgScanMisses = 20
	tgScanMax    = 500
)

// tgLen is a text's length as Telegram counts it against its limits:
// visible characters in UTF-16 code units, so an emoji counts twice.
func tgLen(md string) int {
	return len(utf16.Encode([]rune(toPlainNoURL(md))))
}

// Telegram posts to a channel through a bot that is its admin. Chat is
// @channelname or the numeric -100… id.
type Telegram struct {
	Token string
	Chat  string
	// LogChat is a private chat where the bot is admin: a bot can't read
	// its channel's history, but a channel post forwarded there comes back
	// with its text. Empty: unknown outcomes can't be checked.
	LogChat string
	API     string // tgAPI; a fake server in tests
	Route   routeFunc
}

func (t *Telegram) Name() string { return "tg" }

func (t *Telegram) Publish(ctx context.Context, p *Post) (Result, error) {
	text := p.PublicText()
	if n := tgLen(text); n > tgTextMax {
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
	if tgLen(text) > tgCaptionMax {
		caption = ""
	}
	res, err := t.sendMedia(ctx, p.Media, caption)
	if err != nil || caption != "" || strings.TrimSpace(text) == "" {
		return res, err
	}
	msg, err := t.sendText(ctx, htmlText)
	if err == nil {
		return msg, nil
	}
	// The media went out without its text: take it back, so the retry
	// posts the whole thing once. What can't be removed is reported.
	if cerr := t.Cleanup(ctx, res.sent); cerr != nil {
		return Result{}, &halfSentError{Sent: res.sent, Err: errors.Join(err, cerr)}
	}
	return Result{}, err
}

// Cleanup deletes channel messages by id.
func (t *Telegram) Cleanup(ctx context.Context, ids []string) error {
	var errs []error
	for _, id := range ids {
		var r tgReply[bool]
		if err := t.call(ctx, "deleteMessage", map[string]string{"chat_id": t.Chat, "message_id": id}, nil, &r); err != nil {
			errs = append(errs, fmt.Errorf("delete %s: %w", id, err))
		}
	}
	return errors.Join(errs...)
}

// FindPost scans the channel from the message after sinceID: each id is
// forwarded to LogChat, its text compared, the forward deleted. Ids are
// sequential per channel with gaps for deleted messages, so the scan ends
// after tgScanMisses missing ids in a row.
func (t *Telegram) FindPost(ctx context.Context, sinceID int64, hash string) (Result, bool, error) {
	if t.LogChat == "" {
		return Result{}, false, errors.New("TG_LOG_CHAT is not set: can't read the channel")
	}
	misses := 0
	for id := sinceID + 1; misses < tgScanMisses && id <= sinceID+tgScanMax; id++ {
		var r tgReply[struct {
			MessageID int64  `json:"message_id"`
			Text      string `json:"text"`
			Caption   string `json:"caption"`
		}]
		sid := strconv.FormatInt(id, 10)
		err := t.call(ctx, "forwardMessage", map[string]string{
			"chat_id": t.LogChat, "from_chat_id": t.Chat, "message_id": sid, "disable_notification": "true",
		}, nil, &r)
		if he, ok := errors.AsType[*httpError](err); ok && he.Status == http.StatusBadRequest {
			misses++
			continue
		}
		if err != nil {
			return Result{}, false, err
		}
		misses = 0
		var del tgReply[bool]
		if err := t.call(ctx, "deleteMessage", map[string]string{
			"chat_id": t.LogChat, "message_id": strconv.FormatInt(r.Result.MessageID, 10),
		}, nil, &del); err != nil {
			return Result{}, false, fmt.Errorf("delete forward from log chat: %w", err)
		}
		if textHash(r.Result.Text+r.Result.Caption) == hash {
			return t.result(id), true, nil
		}
	}
	return Result{}, false, nil
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
	err = doJSON(req, out)
	// The URL carries the token; keep it out of logs, keep the error's type:
	// a transport error means the outcome is unknown.
	if ue, ok := errors.AsType[*url.Error](err); ok {
		ue.URL = strings.ReplaceAll(ue.URL, t.Token, "<token>")
	}
	return err
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
		return Result{}, fmt.Errorf("%w: sendMediaGroup: empty result", errAmbiguous)
	}
	ids := make([]int64, 0, len(r.Result)-1)
	for _, m := range r.Result[1:] {
		ids = append(ids, m.MessageID)
	}
	return t.result(r.Result[0].MessageID, ids...), nil
}

func (t *Telegram) result(id int64, more ...int64) Result {
	r := Result{ID: strconv.FormatInt(id, 10)}
	for _, m := range append([]int64{id}, more...) {
		r.sent = append(r.sent, strconv.FormatInt(m, 10))
	}
	if name, ok := strings.CutPrefix(t.Chat, "@"); ok {
		r.URL = "https://t.me/" + name + "/" + r.ID
	}
	return r
}
