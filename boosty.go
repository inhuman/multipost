package main

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"maps"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"
	"unicode/utf16"
)

// Boosty has no public API; this repeats what its web editor sends (HAR of
// the editor, verified in github.com/k0te1ch/PodBoxBot app/publishers/Boosty,
// 25.10.2026; text styles from github.com/barsikus007/boosty):
//
//	POST upload.boosty.to/image {}                    → fileId
//	POST upload.boosty.to/upload/{fileId}             chunks of 5 MB, X-PartNumber from 1
//	POST upload.boosty.to/upload/{fileId}/complete
//	PUT  api.boosty.to/v1/blog/{blog}/post_draft      the editor's autosave
//	POST api.boosty.to/v1/blog/{blog}/post_draft/publish/
//
// A blog has ONE draft: filling it overwrites what the author keeps in the
// editor, so a foreign draft is refused, never overwritten.
type Boosty struct {
	Blog     string
	LevelID  string // subscription level that sees the post; empty: everyone
	AuthPath string // token file written by `login boosty`
	API      string // boostyAPI; a fake server in tests
	Upload   string // boostyUpload
	Route    routeFunc

	auth *boostyAuth
}

const (
	boostyAPI    = "https://api.boosty.to"
	boostyUpload = "https://upload.boosty.to"
	boostyImages = "https://images.boosty.to"
	boostyChunk  = 5 << 20
	// The editor refreshes ahead of expiry; a token dying mid-post would
	// split the upload from the publish.
	boostyRefreshMargin = 10 * time.Minute
)

// Boosty's own session from the browser: the refresh token is one-shot, so
// every refresh is written back before the next request.
type boostyAuth struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresAt    int64  `json:"expires_at"` // unix seconds
	DeviceID     string `json:"device_id"`
	UserAgent    string `json:"user_agent,omitzero"`
}

func (b *Boosty) Name() string { return "boosty" }

func (b *Boosty) Publish(ctx context.Context, p *Post) (Result, error) {
	if err := b.FillDraft(ctx, p); err != nil {
		return Result{}, err
	}
	var reply struct {
		Data struct {
			Post struct {
				ID string `json:"id"`
			} `json:"post"`
		} `json:"data"`
	}
	form := url.Values{"is_showcase_visible": {"true"}}
	err := b.call(ctx, http.MethodPost, b.API+"/v1/blog/"+b.Blog+"/post_draft/publish/", []byte(form.Encode()), formType, nil, &reply)
	// A server error on publish may come after the post went out: let the
	// history decide instead of calling it rejected.
	if he, ok := errors.AsType[*httpError](err); ok && he.Status >= 500 {
		return Result{}, fmt.Errorf("%w: publish: %w", errAmbiguous, err)
	}
	if err != nil {
		return Result{}, fmt.Errorf("publish: %w", err)
	}
	if reply.Data.Post.ID == "" {
		return Result{}, fmt.Errorf("%w: publish reply has no post id", errAmbiguous)
	}
	return b.result(reply.Data.Post.ID), nil
}

func (b *Boosty) result(id string) Result {
	return Result{ID: id, URL: "https://boosty.to/" + b.Blog + "/posts/" + id}
}

// FillDraft puts the post into the blog's draft without publishing it: the
// author can look at it in the editor (`multipost boosty-draft`).
func (b *Boosty) FillDraft(ctx context.Context, p *Post) error {
	for _, m := range p.Media {
		if kindOf(m) == mediaVideo {
			return fmt.Errorf("boosty: video is not supported yet (%s)", m)
		}
	}
	// Any route: the session must come from where the browser logged in,
	// and the browser takes the system's route like we do.
	for _, u := range []string{b.API, b.Upload} {
		if err := b.Route(ctx, u, viaAny); err != nil {
			return err
		}
	}
	text := p.TextFor(true)
	if err := b.checkDraft(ctx, p.Title, textHash(toPlainNoURL(text))); err != nil {
		return err
	}

	var images []boostyBlock
	for _, path := range p.Media {
		img, err := b.uploadImage(ctx, path)
		if err != nil {
			return fmt.Errorf("upload %s: %w", path, err)
		}
		images = append(images, img)
	}
	data := append(images, boostyBlocks(text)...)
	teaser := []boostyBlock{}
	if len(images) > 0 {
		teaser = append(teaser, images[0])
	}
	if p.Mode == modeTeaser {
		teaser = append(teaser, boostyBlocks(p.Teaser)...)
	}
	dataJSON, err := json.Marshal(data)
	if err != nil {
		return err
	}
	teaserJSON, err := json.Marshal(teaser)
	if err != nil {
		return err
	}
	form := url.Values{
		"title":                 {p.Title},
		"data":                  {string(dataJSON)},
		"teaser_data":           {string(teaserJSON)},
		"price":                 {"0"},
		"tags":                  {""},
		"deny_comments":         {"false"},
		"deny_reactions":        {"false"},
		"wait_video":            {"false"},
		"advertiser_info":       {""},
		"last_updated_at":       {fmt.Sprint(time.Now().Unix())},
		"bundle_ids":            {""},
	}
	// A post for everyone has no level at all, as the editor sends it: the
	// free level's id is refused (move_to_free_level).
	if b.LevelID != "" {
		form.Set("subscription_level_id", b.LevelID)
	}
	if err := b.call(ctx, http.MethodPut, b.API+"/v1/blog/"+b.Blog+"/post_draft", []byte(form.Encode()), formType, nil, nil); err != nil {
		return fmt.Errorf("fill draft: %w", err)
	}
	return nil
}

// checkDraft refuses when the editor holds someone's unfinished post. A
// draft with this very post (a run that failed after filling it) is ours.
func (b *Boosty) checkDraft(ctx context.Context, title, hash string) error {
	var reply struct {
		Data struct {
			PostDraft *struct {
				Title string          `json:"title"`
				Data  json.RawMessage `json:"data"`
			} `json:"postDraft"`
		} `json:"data"`
	}
	if err := b.call(ctx, http.MethodGet, b.API+"/v1/blog/"+b.Blog+"/post_draft", nil, "", nil, &reply); err != nil {
		return fmt.Errorf("read draft: %w", err)
	}
	d := reply.Data.PostDraft
	if d == nil {
		return nil
	}
	blocks, err := decodeBoostyBlocks(d.Data)
	if err != nil {
		return fmt.Errorf("read draft: %w", err)
	}
	plain := normText(boostyPlain(blocks))
	empty := plain == "" && d.Title == "" && !hasMedia(blocks)
	if empty || (textHash(plain) == hash && d.Title == title) {
		return nil
	}
	shown := []rune(cmp.Or(d.Title, plain))
	if len(shown) > 60 {
		shown = append(shown[:60], '…')
	}
	return fmt.Errorf("в редакторе Boosty лежит черновик «%s»: публикация его затрёт — допиши или удали его и запусти снова", string(shown))
}

// FindPost looks for the post among the blog's latest 20 posts.
func (b *Boosty) FindPost(ctx context.Context, _ int64, hash string) (Result, bool, error) {
	var reply struct {
		Data []struct {
			ID   string          `json:"id"`
			Data json.RawMessage `json:"data"`
		} `json:"data"`
	}
	if err := b.call(ctx, http.MethodGet, b.API+"/v1/blog/"+b.Blog+"/post/?limit=20", nil, "", nil, &reply); err != nil {
		return Result{}, false, err
	}
	for _, post := range reply.Data {
		blocks, err := decodeBoostyBlocks(post.Data)
		if err != nil {
			return Result{}, false, fmt.Errorf("post %s: %w", post.ID, err)
		}
		if textHash(boostyPlain(blocks)) == hash {
			return b.result(post.ID), true, nil
		}
	}
	return Result{}, false, nil
}

type boostyLevel struct {
	ID    int64  `json:"id"`
	Name  string `json:"name"`
	Price int    `json:"price"`
}

// Levels lists the blog's subscription levels, the free one included, to
// find BOOSTY_LEVEL_ID.
func (b *Boosty) Levels(ctx context.Context) ([]boostyLevel, error) {
	var reply struct {
		Data []boostyLevel `json:"data"`
	}
	err := b.call(ctx, http.MethodGet, b.API+"/v1/blog/"+b.Blog+"/subscription_level/?show_free_level=true", nil, "", nil, &reply)
	return reply.Data, err
}

func (b *Boosty) uploadImage(ctx context.Context, path string) (boostyBlock, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var slot struct {
		FileID string `json:"fileId"`
	}
	if err := b.call(ctx, http.MethodPost, b.Upload+"/image", []byte("{}"), "application/json", nil, &slot); err != nil {
		return nil, err
	}
	if slot.FileID == "" {
		return nil, errors.New("upload init: no fileId")
	}
	part := 1
	for off := 0; off < len(content); off += boostyChunk {
		h := http.Header{"X-Partnumber": {fmt.Sprint(part)}}
		chunk := content[off:min(off+boostyChunk, len(content))]
		if err := b.call(ctx, http.MethodPost, b.Upload+"/upload/"+slot.FileID, chunk, "application/octet-stream", h, nil); err != nil {
			return nil, fmt.Errorf("part %d: %w", part, err)
		}
		part++
	}
	if err := b.call(ctx, http.MethodPost, b.Upload+"/upload/"+slot.FileID+"/complete", nil, "", nil, nil); err != nil {
		return nil, fmt.Errorf("complete: %w", err)
	}
	// The editor sends the size; webp has no decoder here and goes as 0×0.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(content))
	if err != nil {
		cfg = image.Config{}
	}
	return boostyBlock{
		"type": "image", "id": slot.FileID, "uploadId": slot.FileID,
		"url":  boostyImages + "/image/" + slot.FileID,
		"data": map[string]any{}, "rendition": "",
		"size": len(content), "width": cfg.Width, "height": cfg.Height,
	}, nil
}

const formType = "application/x-www-form-urlencoded"

// call sends one request as the browser that logged in; a 401 refreshes
// the session once and repeats.
func (b *Boosty) call(ctx context.Context, method, rawURL string, body []byte, ctype string, h http.Header, out any) error {
	if err := b.ensureAuth(ctx); err != nil {
		return err
	}
	for refreshed := false; ; refreshed = true {
		req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
		if err != nil {
			return err
		}
		maps.Copy(req.Header, h)
		b.setHeaders(req)
		req.Header.Set("Authorization", "Bearer "+b.auth.AccessToken)
		if ctype != "" {
			req.Header.Set("Content-Type", ctype)
		}
		err = doJSON(req, out)
		if he, ok := errors.AsType[*httpError](err); ok && he.Status == http.StatusUnauthorized && !refreshed {
			if err := b.refresh(ctx); err != nil {
				return err
			}
			continue
		}
		return err
	}
}

func (b *Boosty) setHeaders(req *http.Request) {
	req.Header.Set("X-App", "web")
	req.Header.Set("X-Locale", "ru_RU")
	req.Header.Set("X-Currency", "RUB")
	if b.auth != nil && b.auth.UserAgent != "" {
		req.Header.Set("User-Agent", b.auth.UserAgent)
	}
}

var errBoostyLogin = errors.New("сессия Boosty недействительна: войди заново, multipost login boosty")

func (b *Boosty) ensureAuth(ctx context.Context) error {
	if b.auth == nil {
		raw, err := os.ReadFile(b.AuthPath)
		if err != nil {
			return fmt.Errorf("%w (%w)", errBoostyLogin, err)
		}
		var a boostyAuth
		if err := json.Unmarshal(raw, &a); err != nil {
			return fmt.Errorf("%s: %w", b.AuthPath, err)
		}
		b.auth = &a
	}
	if time.Until(time.Unix(b.auth.ExpiresAt, 0)) < boostyRefreshMargin {
		return b.refresh(ctx)
	}
	return nil
}

func (b *Boosty) refresh(ctx context.Context) error {
	form := url.Values{
		"device_id":     {b.auth.DeviceID},
		"device_os":     {"web"},
		"grant_type":    {"refresh_token"},
		"refresh_token": {b.auth.RefreshToken},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, b.API+"/oauth/token/", strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	b.setHeaders(req)
	req.Header.Set("Content-Type", formType)
	var reply struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
		ExpiresIn    int64  `json:"expires_in"`
	}
	err = doJSON(req, &reply)
	if he, ok := errors.AsType[*httpError](err); ok && he.Status/100 == 4 {
		return fmt.Errorf("%w: refresh HTTP %d", errBoostyLogin, he.Status)
	}
	if err != nil {
		return fmt.Errorf("refresh: %w", err)
	}
	if reply.AccessToken == "" || reply.ExpiresIn <= 0 {
		return fmt.Errorf("%w: refresh reply without a token", errAmbiguous)
	}
	b.auth.AccessToken = reply.AccessToken
	b.auth.RefreshToken = cmp.Or(reply.RefreshToken, b.auth.RefreshToken)
	b.auth.ExpiresAt = time.Now().Unix() + reply.ExpiresIn
	return saveBoostyAuth(b.AuthPath, b.auth)
}

// saveBoostyAuth writes via a temp file: a torn write would lose the only
// valid refresh token.
func saveBoostyAuth(path string, a *boostyAuth) error {
	raw, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// boostyLoginSnippet goes to the DevTools console of the logged-in tab: it
// copies the session cookies (auth, _clientId) and the user agent.
const boostyLoginSnippet = `copy(JSON.stringify({auth:document.cookie.match(/(?:^|; )auth=([^;]*)/)[1],clientId:document.cookie.match(/(?:^|; )_clientId=([^;]*)/)[1],ua:navigator.userAgent}))`

// importBoostyLogin turns the snippet's line into the token file.
func importBoostyLogin(r io.Reader, path string) error {
	line, err := bufio.NewReaderSize(r, 64<<10).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	var in struct {
		Auth     string `json:"auth"`
		ClientID string `json:"clientId"`
		UA       string `json:"ua"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(line)), &in); err != nil {
		return fmt.Errorf("это не строка из сниппета: %w", err)
	}
	// PathUnescape: a '+' in a token must stay a '+'.
	cookie, err := url.PathUnescape(in.Auth)
	if err != nil {
		return fmt.Errorf("кука auth: %w", err)
	}
	var auth struct {
		AccessToken  string `json:"accessToken"`
		RefreshToken string `json:"refreshToken"`
		ExpiresAt    int64  `json:"expiresAt"`
	}
	if err := json.Unmarshal([]byte(cookie), &auth); err != nil {
		return fmt.Errorf("кука auth: %w", err)
	}
	if auth.AccessToken == "" || auth.RefreshToken == "" || in.ClientID == "" {
		return errors.New("в куках нет токенов или _clientId: ты вошёл в аккаунт?")
	}
	if auth.ExpiresAt > 1e12 { // milliseconds
		auth.ExpiresAt /= 1000
	}
	return saveBoostyAuth(path, &boostyAuth{
		AccessToken: auth.AccessToken, RefreshToken: auth.RefreshToken,
		ExpiresAt: auth.ExpiresAt, DeviceID: in.ClientID, UserAgent: in.UA,
	})
}

// Boosty keeps a post as draft.js blocks. A text block's content is itself
// a JSON string, [text, "unstyled", [[style, offset, length]...]] with
// offsets in UTF-16 units; every paragraph ends with a BLOCK_END block, an
// empty line is a BLOCK_END alone. Links are blocks of their own.
type boostyBlock map[string]any

// Style codes of the editor (barsikus007/boosty TextFormatEnum).
const (
	boostyBold   = 0
	boostyItalic = 2
)

func boostyBlocks(md string) []boostyBlock {
	blocks := []boostyBlock{}
	for line := range strings.SplitSeq(md, "\n") {
		last := 0
		for _, m := range reLink.FindAllStringSubmatchIndex(line, -1) {
			blocks = appendBoostyText(blocks, line[last:m[0]])
			blocks = append(blocks, boostyBlock{
				"type": "link", "url": line[m[4]:m[5]], "explicit": true,
				"content": boostyContent(toPlain(line[m[2]:m[3]]), [][3]int{}),
			})
			last = m[1]
		}
		blocks = appendBoostyText(blocks, line[last:])
		blocks = append(blocks, boostyBlock{"type": "text", "content": "", "modificator": "BLOCK_END"})
	}
	return blocks
}

func appendBoostyText(blocks []boostyBlock, md string) []boostyBlock {
	if md == "" {
		return blocks
	}
	text, styles := boostyStyled(md)
	return append(blocks, boostyBlock{"type": "text", "content": boostyContent(text, styles), "modificator": ""})
}

func boostyContent(text string, styles [][3]int) string {
	raw, err := json.Marshal([]any{text, "unstyled", styles})
	if err != nil {
		panic(err) // strings and ints always marshal
	}
	return string(raw)
}

// boostyStyled strips **bold**, *italic* and `code` (Boosty has no code
// style: plain text) and returns the style ranges.
func boostyStyled(md string) (string, [][3]int) {
	var out strings.Builder
	styles := [][3]int{}
	marks := []struct {
		re    *regexp.Regexp
		style int
	}{{reCode, -1}, {reBold, boostyBold}, {reItalic, boostyItalic}}
	for md != "" {
		var loc []int
		style := -1
		for _, mk := range marks {
			if m := mk.re.FindStringSubmatchIndex(md); m != nil && (loc == nil || m[0] < loc[0]) {
				loc, style = m, mk.style
			}
		}
		if loc == nil {
			out.WriteString(md)
			break
		}
		out.WriteString(md[:loc[0]])
		inner := md[loc[2]:loc[3]]
		if style >= 0 {
			styles = append(styles, [3]int{style, utf16Len(out.String()), utf16Len(inner)})
		}
		out.WriteString(inner)
		md = md[loc[1]:]
	}
	return out.String(), styles
}

func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

type boostyRawBlock struct {
	Type        string `json:"type"`
	Content     string `json:"content"`
	Modificator string `json:"modificator"`
}

// decodeBoostyBlocks reads a post's data: an array of blocks, or that array
// as a JSON string (how drafts are sent).
func decodeBoostyBlocks(raw json.RawMessage) ([]boostyRawBlock, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return nil, nil
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return nil, err
		}
		raw = json.RawMessage(s)
	}
	var blocks []boostyRawBlock
	err := json.Unmarshal(raw, &blocks)
	return blocks, err
}

// boostyPlain is the text a reader sees, links by their text: what the
// journal hashes.
func boostyPlain(blocks []boostyRawBlock) string {
	var sb strings.Builder
	for _, bl := range blocks {
		if bl.Type != "text" && bl.Type != "link" && bl.Type != "header" {
			continue
		}
		if bl.Content == "" {
			if bl.Modificator == "BLOCK_END" {
				sb.WriteString("\n")
			}
			continue
		}
		var parts []json.RawMessage
		var text string
		if json.Unmarshal([]byte(bl.Content), &parts) != nil || len(parts) == 0 || json.Unmarshal(parts[0], &text) != nil {
			continue // a block of an unknown shape adds no text
		}
		sb.WriteString(text)
	}
	return sb.String()
}

func hasMedia(blocks []boostyRawBlock) bool {
	for _, bl := range blocks {
		if bl.Type != "text" && bl.Type != "link" && bl.Type != "header" {
			return true
		}
	}
	return false
}
