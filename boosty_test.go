package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestBoostyBlocks(t *testing.T) {
	text := func(s string, styles ...[3]int) boostyBlock {
		if styles == nil {
			styles = [][3]int{}
		}
		return boostyBlock{"type": "text", "content": boostyContent(s, styles), "modificator": ""}
	}
	end := boostyBlock{"type": "text", "content": "", "modificator": "BLOCK_END"}
	link := func(s, u string) boostyBlock {
		return boostyBlock{"type": "link", "url": u, "explicit": true, "content": boostyContent(s, [][3]int{})}
	}
	tests := []struct {
		name, md string
		want     []boostyBlock
	}{
		{"plain", "abc", []boostyBlock{text("abc"), end}},
		{"bold", "a **b** c", []boostyBlock{text("a b c", [3]int{boostyBold, 2, 1}), end}},
		{"italic after bold", "**ab** *c*", []boostyBlock{text("ab c", [3]int{boostyBold, 0, 2}, [3]int{boostyItalic, 3, 1}), end}},
		// draft.js offsets are UTF-16: the emoji is two units.
		{"utf16 offset", "😀 **x**", []boostyBlock{text("😀 x", [3]int{boostyBold, 3, 1}), end}},
		{"code is plain", "a `**b**` c", []boostyBlock{text("a **b** c"), end}},
		{"link", "see [the site](https://x.ru/a) now", []boostyBlock{text("see "), link("the site", "https://x.ru/a"), text(" now"), end}},
		{"bold link text is plain", "[**x**](https://x.ru)", []boostyBlock{link("x", "https://x.ru"), end}},
		{"blank line is BLOCK_END alone", "a\n\nb", []boostyBlock{text("a"), end, end, text("b"), end}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := json.Marshal(boostyBlocks(tt.md))
			if err != nil {
				t.Fatal(err)
			}
			want, err := json.Marshal(tt.want)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != string(want) {
				t.Errorf("\n got %s\nwant %s", got, want)
			}
		})
	}
}

// What we send must hash, read back, like the journal hashes the post:
// otherwise FindPost never recognises our own post and it goes out twice.
func TestBoostyPlainMatchesJournalHash(t *testing.T) {
	for _, md := range []string{
		"plain",
		"a **b** *c* `d`",
		"see [the site](https://x.ru/a) now",
		"line one\n\nline [two](https://t.me/x)\nthree 😀",
	} {
		raw, err := json.Marshal(boostyBlocks(md))
		if err != nil {
			t.Fatal(err)
		}
		blocks, err := decodeBoostyBlocks(raw)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := textHash(boostyPlain(blocks)), textHash(toPlainNoURL(md)); got != want {
			t.Errorf("%q: read back as %q", md, boostyPlain(blocks))
		}
	}
}

type boostyCall struct {
	Method, Path, Auth, Part string
	Form                     url.Values
	Size                     int
}

// boostyFake answers like api.boosty.to and upload.boosty.to on one server.
type boostyFake struct {
	t       *testing.T
	calls   []boostyCall
	draft   string // JSON of data.postDraft
	posts   string // JSON of the post list's data
	status  map[string]int
	expired map[string]bool // access tokens answered with 401
	refresh int             // status of /oauth/token/, 0 = ok
}

func (f *boostyFake) serve(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		f.t.Fatal(err)
	}
	c := boostyCall{Method: r.Method, Path: r.URL.Path, Auth: r.Header.Get("Authorization"), Part: r.Header.Get("X-PartNumber"), Size: len(body)}
	if r.Header.Get("Content-Type") == formType {
		if c.Form, err = url.ParseQuery(string(body)); err != nil {
			f.t.Fatal(err)
		}
	}
	f.calls = append(f.calls, c)
	key := r.Method + " " + r.URL.Path
	if r.URL.Path == "/oauth/token/" {
		if f.refresh != 0 {
			w.WriteHeader(f.refresh)
			return
		}
		fmt.Fprint(w, `{"access_token":"new-access","refresh_token":"new-refresh","expires_in":3600}`)
		return
	}
	if f.expired[strings.TrimPrefix(c.Auth, "Bearer ")] {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if s := f.status[key]; s != 0 {
		w.WriteHeader(s)
		fmt.Fprint(w, `{"error":"x"}`)
		return
	}
	switch {
	case key == "GET /v1/blog/b/post_draft":
		fmt.Fprintf(w, `{"data":{"postDraft":%s}}`, cmpOrNull(f.draft))
	case key == "GET /v1/blog/b/post/":
		fmt.Fprintf(w, `{"data":%s}`, cmpOrNull(f.posts))
	case key == "POST /image":
		fmt.Fprint(w, `{"fileId":"F1"}`)
	case key == "POST /v1/blog/b/post_draft/publish/":
		fmt.Fprint(w, `{"data":{"post":{"id":"P1"}}}`)
	default:
		fmt.Fprint(w, `{}`)
	}
}

func cmpOrNull(s string) string {
	if s == "" {
		return "null"
	}
	return s
}

func (f *boostyFake) paths() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.Method+" "+c.Path)
	}
	return out
}

func newBoosty(t *testing.T, auth boostyAuth) (*Boosty, *boostyFake) {
	t.Helper()
	f := &boostyFake{t: t, status: map[string]int{}, expired: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	path := filepath.Join(t.TempDir(), "boosty-auth.json")
	if err := saveBoostyAuth(path, &auth); err != nil {
		t.Fatal(err)
	}
	b := &Boosty{Blog: "b", LevelID: "7", AuthPath: path, API: srv.URL, Upload: srv.URL,
		Route: func(context.Context, string, routeWant) error { return nil }}
	return b, f
}

func freshAuth() boostyAuth {
	return boostyAuth{AccessToken: "acc", RefreshToken: "ref", ExpiresAt: time.Now().Add(time.Hour).Unix(), DeviceID: "dev"}
}

func tempFile(t *testing.T, name string, size int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	writeFile(t, path, int64(size))
	return path
}

func TestBoostyPublish(t *testing.T) {
	b, f := newBoosty(t, freshAuth())
	p := &Post{Title: "T", Mode: modeTeaser, Body: "full **text**", Teaser: "short", Media: []string{tempFile(t, "a.png", 10)}}
	res, err := b.Publish(t.Context(), p)
	if err != nil {
		t.Fatal(err)
	}
	if res.ID != "P1" || res.URL != "https://boosty.to/b/posts/P1" {
		t.Errorf("result %+v", res)
	}
	want := []string{
		"GET /v1/blog/b/post_draft", "POST /image", "POST /upload/F1", "POST /upload/F1/complete",
		"PUT /v1/blog/b/post_draft", "POST /v1/blog/b/post_draft/publish/",
	}
	if !slices.Equal(f.paths(), want) {
		t.Fatalf("calls\n %v\nwant %v", f.paths(), want)
	}
	for _, c := range f.calls {
		if c.Auth != "Bearer acc" {
			t.Errorf("%s %s: auth %q", c.Method, c.Path, c.Auth)
		}
	}
	form := f.calls[4].Form
	if form.Get("subscription_level_id") != "7" || form.Get("title") != "T" {
		t.Errorf("draft form %v", form)
	}
	// Paid platform: the full text, image first; the teaser shows the image
	// and the teaser text, never the full one.
	var data []map[string]any
	if err := json.Unmarshal([]byte(form.Get("data")), &data); err != nil {
		t.Fatal(err)
	}
	if data[0]["type"] != "image" || data[0]["id"] != "F1" || !strings.Contains(form.Get("data"), "full text") {
		t.Errorf("data %s", form.Get("data"))
	}
	teaser := form.Get("teaser_data")
	if !strings.Contains(teaser, `"id":"F1"`) || !strings.Contains(teaser, "short") || strings.Contains(teaser, "full") {
		t.Errorf("teaser %s", teaser)
	}
}

// Chunks are 5 MB: exactly 5 MB is one part, one byte more is two.
func TestBoostyUploadChunks(t *testing.T) {
	for _, tt := range []struct {
		size  int
		parts []string
	}{
		{boostyChunk, []string{"1"}},
		{boostyChunk + 1, []string{"1", "2"}},
	} {
		b, f := newBoosty(t, freshAuth())
		if _, err := b.uploadImage(t.Context(), tempFile(t, "a.jpg", tt.size)); err != nil {
			t.Fatal(err)
		}
		var parts []string
		total := 0
		for _, c := range f.calls {
			if c.Path == "/upload/F1" {
				parts = append(parts, c.Part)
				total += c.Size
			}
		}
		if !slices.Equal(parts, tt.parts) || total != tt.size {
			t.Errorf("size %d: parts %v, %d bytes sent", tt.size, parts, total)
		}
	}
}

func TestBoostyRefusesBeforeAnyCall(t *testing.T) {
	b, f := newBoosty(t, freshAuth())
	_, err := b.Publish(t.Context(), &Post{Body: "x", Media: []string{tempFile(t, "a.mp4", 10)}})
	if err == nil || len(f.calls) != 0 {
		t.Errorf("video: err %v, calls %v", err, f.paths())
	}
}

func TestBoostyDraftGuard(t *testing.T) {
	draft := func(title, md string) string {
		raw, err := json.Marshal(boostyBlocks(md))
		if err != nil {
			t.Fatal(err)
		}
		d, err := json.Marshal(map[string]any{"title": title, "data": string(raw)})
		if err != nil {
			t.Fatal(err)
		}
		return string(d)
	}
	p := &Post{Title: "T", Mode: modeFull, Body: "our **post**"}
	tests := []struct {
		name, draft string
		refused     bool
	}{
		{"no draft", "", false},
		{"empty draft", `{"title":"","data":[]}`, false},
		{"our own draft from a failed run", draft("T", "our **post**"), false},
		{"foreign text", draft("T", "my notes"), true},
		{"same text, other title", draft("Other", "our post"), true},
		{"only a title", `{"title":"idea","data":[]}`, true},
		{"only an image", `{"title":"","data":[{"type":"image","id":"x"}]}`, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, f := newBoosty(t, freshAuth())
			f.draft = tt.draft
			_, err := b.Publish(t.Context(), p)
			put := slices.Contains(f.paths(), "PUT /v1/blog/b/post_draft")
			if tt.refused && (err == nil || put) {
				t.Errorf("foreign draft overwritten: err %v, calls %v", err, f.paths())
			}
			if !tt.refused && err != nil {
				t.Errorf("refused: %v", err)
			}
		})
	}
}

func readAuth(t *testing.T, path string) boostyAuth {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var a boostyAuth
	if err := json.Unmarshal(raw, &a); err != nil {
		t.Fatal(err)
	}
	return a
}

// The refresh token is one-shot: the new pair must be on disk, or the next
// run logs in with a dead token.
func TestBoostyRefreshOn401(t *testing.T) {
	b, f := newBoosty(t, freshAuth())
	f.expired["acc"] = true
	if _, err := b.Levels(t.Context()); err != nil {
		t.Fatal(err)
	}
	want := []string{"GET /v1/blog/b/subscription_level/", "POST /oauth/token/", "GET /v1/blog/b/subscription_level/"}
	if !slices.Equal(f.paths(), want) {
		t.Errorf("calls %v, want %v", f.paths(), want)
	}
	if f.calls[2].Auth != "Bearer new-access" {
		t.Errorf("retry auth %q", f.calls[2].Auth)
	}
	if form := f.calls[1].Form; form.Get("refresh_token") != "ref" || form.Get("device_id") != "dev" || form.Get("grant_type") != "refresh_token" {
		t.Errorf("refresh form %v", form)
	}
	if a := readAuth(t, b.AuthPath); a.AccessToken != "new-access" || a.RefreshToken != "new-refresh" || a.DeviceID != "dev" {
		t.Errorf("saved %+v", a)
	}
}

func TestBoostyRefreshAhead(t *testing.T) {
	for _, tt := range []struct {
		left    time.Duration
		refresh bool
	}{
		{boostyRefreshMargin - time.Minute, true},
		{boostyRefreshMargin + time.Minute, false},
	} {
		a := freshAuth()
		a.ExpiresAt = time.Now().Add(tt.left).Unix()
		b, f := newBoosty(t, a)
		if _, err := b.Levels(t.Context()); err != nil {
			t.Fatal(err)
		}
		if got := f.paths()[0] == "POST /oauth/token/"; got != tt.refresh {
			t.Errorf("%v left: refreshed %v, calls %v", tt.left, got, f.paths())
		}
	}
}

func TestBoostyDeadSession(t *testing.T) {
	b, f := newBoosty(t, freshAuth())
	f.expired["acc"] = true
	f.refresh = http.StatusUnauthorized
	if _, err := b.Levels(t.Context()); !errors.Is(err, errBoostyLogin) {
		t.Errorf("err %v, want the login hint", err)
	}
}

// Rejected means nothing went out; a server error on publish may hide a post
// that did, and must send the next run to the history.
func TestBoostyErrorClasses(t *testing.T) {
	for _, tt := range []struct {
		fail string
		code int
		want attemptResult
	}{
		{"PUT /v1/blog/b/post_draft", 400, resultRejected},
		{"POST /image", 500, resultRejected},
		{"POST /v1/blog/b/post_draft/publish/", 400, resultRejected},
		{"POST /v1/blog/b/post_draft/publish/", 502, resultLost},
	} {
		b, f := newBoosty(t, freshAuth())
		f.status[tt.fail] = tt.code
		_, err := b.Publish(t.Context(), &Post{Body: "x", Media: []string{tempFile(t, "a.png", 1)}})
		if got := classify(err); got != tt.want {
			t.Errorf("%s → %d: %s (%v), want %s", tt.fail, tt.code, got, err, tt.want)
		}
	}
}

func TestBoostyFindPost(t *testing.T) {
	b, f := newBoosty(t, freshAuth())
	data, err := json.Marshal(boostyBlocks("our **post** [link](https://x.ru)"))
	if err != nil {
		t.Fatal(err)
	}
	f.posts = fmt.Sprintf(`[{"id":"P0","data":[]},{"id":"P9","data":%s}]`, data)
	res, found, err := b.FindPost(t.Context(), 0, textHash(toPlainNoURL("our **post** [link](https://x.ru)")))
	if err != nil || !found || res.ID != "P9" || res.URL != "https://boosty.to/b/posts/P9" {
		t.Errorf("found %v %+v %v", found, res, err)
	}
	if _, found, err := b.FindPost(t.Context(), 0, textHash("other")); err != nil || found {
		t.Errorf("other text found: %v %v", found, err)
	}
}

func TestImportBoostyLogin(t *testing.T) {
	cookie := url.PathEscape(`{"accessToken":"a+b","refreshToken":"r","expiresAt":1790000000000}`)
	path := filepath.Join(t.TempDir(), "auth.json")
	line := fmt.Sprintf(`{"auth":%q,"clientId":"dev","ua":"UA"}`+"\n", cookie)
	if err := importBoostyLogin(strings.NewReader(line), path); err != nil {
		t.Fatal(err)
	}
	want := boostyAuth{AccessToken: "a+b", RefreshToken: "r", ExpiresAt: 1790000000, DeviceID: "dev", UserAgent: "UA"}
	if got := readAuth(t, path); got != want {
		t.Errorf("saved %+v, want %+v", got, want)
	}
	if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("token file mode: %v %v", info.Mode(), err)
	}

	noClient := fmt.Sprintf(`{"auth":%q,"clientId":""}`, cookie)
	if err := importBoostyLogin(strings.NewReader(noClient), filepath.Join(t.TempDir(), "x")); err == nil {
		t.Error("accepted a login without _clientId: refresh would be impossible")
	}
}

func TestBoostyBlog(t *testing.T) {
	for in, want := range map[string]string{
		"https://boosty.to/robot":        "robot",
		"https://boosty.to/robot/":       "robot",
		"https://boosty.to/robot/posts/": "robot",
		"":                               "",
	} {
		if got, err := boostyBlog(in); err != nil || got != want {
			t.Errorf("%q: %q %v", in, got, err)
		}
	}
	for _, bad := range []string{"https://boosty.to/", "https://sponsr.ru/robot"} {
		if _, err := boostyBlog(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

// A post for everyone goes without a level: Boosty refuses the free
// level's id (move_to_free_level, live answer 26.10.2026).
func TestBoostyPublicPostHasNoLevel(t *testing.T) {
	b, f := newBoosty(t, freshAuth())
	b.LevelID = ""
	if _, err := b.Publish(t.Context(), &Post{Mode: modeFull, Body: "x"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if c.Method == http.MethodPut {
			if _, ok := c.Form["subscription_level_id"]; ok {
				t.Errorf("public post sent a level: %v", c.Form)
			}
			return
		}
	}
	t.Fatal("no draft PUT")
}
