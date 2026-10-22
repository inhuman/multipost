package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

const tgToken = "123:SECRET"

type tgCall struct {
	Method string
	Fields map[string]string
	Files  map[string]string // form field -> uploaded file name
	Sizes  map[string]int64
}

// tgFake is a Bot API: messages get sequential ids from 100; a method in
// fail answers with that status (or drops the connection for status 0).
type tgFake struct {
	mu     sync.Mutex
	calls  []tgCall
	nextID int64
	fail   map[string]int
	raw    map[string]string // method -> literal 200 body
}

func (f *tgFake) serve(w http.ResponseWriter, r *http.Request) {
	method, ok := strings.CutPrefix(r.URL.Path, "/bot"+tgToken+"/")
	if !ok {
		http.Error(w, "bad path "+r.URL.Path, http.StatusNotFound)
		return
	}
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	c := tgCall{Method: method, Fields: map[string]string{}, Files: map[string]string{}, Sizes: map[string]int64{}}
	for k, v := range r.MultipartForm.Value {
		c.Fields[k] = v[0]
	}
	for k, v := range r.MultipartForm.File {
		c.Files[k] = v[0].Filename
		c.Sizes[k] = v[0].Size
	}
	f.mu.Lock()
	f.calls = append(f.calls, c)
	status, failing := f.fail[method]
	f.mu.Unlock()

	if failing {
		if status == 0 {
			hj, _ := w.(http.Hijacker)
			conn, _, err := hj.Hijack()
			if err == nil {
				conn.Close()
			}
			return
		}
		w.WriteHeader(status)
		fmt.Fprintf(w, `{"ok":false,"error_code":%d,"description":"Bad Request: nope"}`, status)
		return
	}
	if body, ok := f.raw[method]; ok {
		io.WriteString(w, body)
		return
	}
	next := func() int64 {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.nextID++
		return 99 + f.nextID
	}
	switch method {
	case "sendMessage", "sendPhoto", "sendVideo":
		fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d}}`, next())
	case "sendMediaGroup":
		var items []map[string]any
		if err := json.Unmarshal([]byte(c.Fields["media"]), &items); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		msgs := make([]string, len(items))
		for i := range items {
			msgs[i] = fmt.Sprintf(`{"message_id":%d}`, next())
		}
		fmt.Fprintf(w, `{"ok":true,"result":[%s]}`, strings.Join(msgs, ","))
	case "deleteMessage":
		io.WriteString(w, `{"ok":true,"result":true}`)
	default:
		http.Error(w, "unknown method", http.StatusNotFound)
	}
}

func (f *tgFake) methods() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		s := c.Method
		if c.Method == "deleteMessage" {
			s += " " + c.Fields["message_id"]
		}
		out = append(out, s)
	}
	return out
}

// mediaFiles creates media/<name> files; a name "big.mp4=N" gets size N
// (sparse, so 50 MB costs nothing on disk).
func mediaFiles(t *testing.T, specs ...string) []string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "media")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, s := range specs {
		name, size := s, int64(16)
		if n, sz, ok := strings.Cut(s, "="); ok {
			name = n
			if _, err := fmt.Sscan(sz, &size); err != nil {
				t.Fatal(err)
			}
		}
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("0123456789abcdef"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Truncate(p, size); err != nil {
			t.Fatal(err)
		}
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

func nPhotos(n int) []string {
	var s []string
	for i := range n {
		s = append(s, fmt.Sprintf("%02d.jpg", i))
	}
	return s
}

func TestTelegramPublish(t *testing.T) {
	short := "Hi **there** & <you>"
	shortHTML := "Hi <b>there</b> &amp; &lt;you&gt;"
	// visible length differs from the raw one: a link's URL does not count
	link := " [l](https://example.com/" + strings.Repeat("u", 3000) + ")"
	exactCaption := strings.Repeat("я", tgCaptionMax-2) + link // 1022 + "l" + " " = 1024 visible
	overCaption := strings.Repeat("я", tgCaptionMax-1) + link  // 1025 visible
	exactText := strings.Repeat("я", tgTextMax-2) + link
	overText := strings.Repeat("я", tgTextMax-1) + link

	type check func(t *testing.T, f *tgFake, res Result, err error)
	noErr := func(t *testing.T, _ *tgFake, _ Result, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("Publish: %v", err)
		}
	}
	refused := func(want string) check {
		return func(t *testing.T, f *tgFake, _ Result, err error) {
			t.Helper()
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("err = %v, want refusal mentioning %q", err, want)
			}
			if classify(err) != resultRejected {
				t.Errorf("classify = %s, a refusal before sending must be rejected", classify(err))
			}
		}
	}
	result := func(id, url string, sent ...string) check {
		return func(t *testing.T, _ *tgFake, res Result, _ error) {
			t.Helper()
			if res.ID != id || res.URL != url {
				t.Errorf("result id=%q url=%q, want id=%q url=%q", res.ID, res.URL, id, url)
			}
			if sent != nil && !slices.Equal(res.sent, sent) {
				t.Errorf("result sent=%q, want %q", res.sent, sent)
			}
		}
	}
	field := func(call int, key, want string) check {
		return func(t *testing.T, f *tgFake, _ Result, _ error) {
			t.Helper()
			if call >= len(f.calls) {
				t.Fatalf("no call #%d", call)
			}
			got, ok := f.calls[call].Fields[key]
			if want == "<absent>" {
				if ok {
					t.Errorf("call %s: field %s=%q, want absent", f.calls[call].Method, key, got)
				}
				return
			}
			if got != want {
				t.Errorf("call %s: field %s=%.80q, want %.80q", f.calls[call].Method, key, got, want)
			}
		}
	}
	album := func(call int, wantTypes []string, wantCaption string) check {
		return func(t *testing.T, f *tgFake, _ Result, _ error) {
			t.Helper()
			c := f.calls[call]
			var items []struct {
				Type, Media, Caption string
				ParseMode            string `json:"parse_mode"`
			}
			if err := json.Unmarshal([]byte(c.Fields["media"]), &items); err != nil {
				t.Fatalf("media field: %v", err)
			}
			var types, uploaded []string
			for i, it := range items {
				types = append(types, it.Type)
				name, ok := strings.CutPrefix(it.Media, "attach://")
				if !ok || c.Files[name] == "" {
					t.Errorf("item %d media %q: not an attached upload (files %v)", i, it.Media, c.Files)
				}
				uploaded = append(uploaded, c.Files[name])
				if i > 0 && it.Caption != "" {
					t.Errorf("item %d has a caption: only the first one may", i)
				}
			}
			if !slices.Equal(types, wantTypes) {
				t.Errorf("album types %q, want %q", types, wantTypes)
			}
			if !slices.IsSorted(uploaded) || len(c.Files) != len(items) {
				t.Errorf("album files %q: want each file once, in name order", uploaded)
			}
			if items[0].Caption != wantCaption {
				t.Errorf("album caption %.80q, want %.80q", items[0].Caption, wantCaption)
			}
			if wantCaption != "" && items[0].ParseMode != "HTML" {
				t.Errorf("album caption parse_mode %q, want HTML", items[0].ParseMode)
			}
		}
	}

	tests := []struct {
		name   string
		chat   string
		post   *Post
		media  []string
		fail   map[string]int
		raw    map[string]string
		want   []string // Bot API calls in order
		checks []check
	}{
		{
			name: "text only: one HTML message",
			post: &Post{Body: short},
			want: []string{"sendMessage"},
			checks: []check{noErr, field(0, "text", shortHTML), field(0, "parse_mode", "HTML"), field(0, "chat_id", "@chan"),
				result("100", "https://t.me/chan/100", "100")},
		},
		{
			name:   "numeric chat id: no public link",
			chat:   "-1001234",
			post:   &Post{Body: short},
			want:   []string{"sendMessage"},
			checks: []check{noErr, field(0, "chat_id", "-1001234"), result("100", "")},
		},
		{
			name:   "teaser mode sends the teaser and paid link, not the body",
			post:   &Post{Mode: modeTeaser, Body: "SECRET BODY", Teaser: "Teaser", PaidURL: "https://boosty.to/x"},
			want:   []string{"sendMessage"},
			checks: []check{noErr, field(0, "text", "Teaser\n\nhttps://boosty.to/x")},
		},
		{
			name:   "text at the limit counted in visible characters, not bytes or markup",
			post:   &Post{Body: exactText},
			want:   []string{"sendMessage"},
			checks: []check{noErr},
		},
		{
			name:   "text over the limit: refused before any call",
			post:   &Post{Body: overText},
			want:   nil,
			checks: []check{refused("4096")},
		},
		{
			name:   "text over the limit with media: refused before the media goes",
			post:   &Post{Body: overText},
			media:  []string{"a.jpg"},
			want:   nil,
			checks: []check{refused("4096")},
		},
		{
			name:  "one photo with a short text: text is its caption",
			post:  &Post{Body: short},
			media: []string{"a.jpg"},
			want:  []string{"sendPhoto"},
			checks: []check{noErr, field(0, "caption", shortHTML), field(0, "parse_mode", "HTML"), field(0, "chat_id", "@chan"),
				func(t *testing.T, f *tgFake, _ Result, _ error) {
					if f.calls[0].Files["photo"] != "a.jpg" {
						t.Errorf("photo upload %v", f.calls[0].Files)
					}
				}, result("100", "https://t.me/chan/100")},
		},
		{
			name:  "one video: sendVideo, streamable",
			post:  &Post{Body: short},
			media: []string{"a.MP4"},
			want:  []string{"sendVideo"},
			checks: []check{noErr, field(0, "caption", shortHTML), field(0, "supports_streaming", "true"),
				func(t *testing.T, f *tgFake, _ Result, _ error) {
					if f.calls[0].Files["video"] != "a.MP4" {
						t.Errorf("video upload %v", f.calls[0].Files)
					}
				}},
		},
		{
			name:   "caption exactly at the limit stays a caption",
			post:   &Post{Body: exactCaption},
			media:  []string{"a.jpg"},
			want:   []string{"sendPhoto"},
			checks: []check{noErr, field(0, "caption", toHTML(exactCaption))},
		},
		{
			name:  "caption over the limit: media first without caption, then the text; result is the text",
			post:  &Post{Body: overCaption},
			media: []string{"a.jpg"},
			want:  []string{"sendPhoto", "sendMessage"},
			checks: []check{noErr, field(0, "caption", "<absent>"), field(0, "parse_mode", "<absent>"),
				field(1, "text", toHTML(overCaption)), result("101", "https://t.me/chan/101")},
		},
		{
			name:   "album with caption on the first item",
			post:   &Post{Body: short},
			media:  []string{"b.mp4", "a.jpg", "c.png"},
			want:   []string{"sendMediaGroup"},
			checks: []check{noErr, album(0, []string{"photo", "video", "photo"}, shortHTML), result("100", "https://t.me/chan/100", "100", "101", "102")},
		},
		{
			name:   "album of ten is allowed",
			post:   &Post{Body: short},
			media:  nPhotos(tgAlbumMax),
			want:   []string{"sendMediaGroup"},
			checks: []check{noErr},
		},
		{
			name:   "album over ten: refused before any call",
			post:   &Post{Body: short},
			media:  nPhotos(tgAlbumMax + 1),
			want:   nil,
			checks: []check{refused("album")},
		},
		{
			name:   "album with a long text: album without caption, then the text",
			post:   &Post{Body: overCaption},
			media:  []string{"a.jpg", "b.jpg"},
			want:   []string{"sendMediaGroup", "sendMessage"},
			checks: []check{noErr, album(0, []string{"photo", "photo"}, ""), result("102", "https://t.me/chan/102")},
		},
		{
			name:  "file of exactly 50 MB is uploaded",
			post:  &Post{Body: short},
			media: []string{fmt.Sprintf("big.mp4=%d", tgUploadMax)},
			want:  []string{"sendVideo"},
			checks: []check{noErr, func(t *testing.T, f *tgFake, _ Result, _ error) {
				if f.calls[0].Sizes["video"] != tgUploadMax {
					t.Errorf("uploaded %d bytes, want %d", f.calls[0].Sizes["video"], tgUploadMax)
				}
			}},
		},
		{
			name:   "one file over 50 MB in an album: refused before any call",
			post:   &Post{Body: short},
			media:  []string{"a.jpg", fmt.Sprintf("big.mp4=%d", tgUploadMax+1)},
			want:   nil,
			checks: []check{refused("50 MB")},
		},
		{
			name:   "photo refused by the API: nothing more is sent",
			post:   &Post{Body: overCaption},
			media:  []string{"a.jpg"},
			fail:   map[string]int{"sendPhoto": 400},
			want:   []string{"sendPhoto"},
			checks: []check{refused("400")},
		},
		{
			name:  "text refused after the album: the album is taken back, the post counts as not sent",
			post:  &Post{Body: overCaption},
			media: []string{"a.jpg", "b.jpg", "c.mp4"},
			fail:  map[string]int{"sendMessage": 400},
			want:  []string{"sendMediaGroup", "sendMessage", "deleteMessage 100", "deleteMessage 101", "deleteMessage 102"},
			checks: []check{refused("400"), func(t *testing.T, f *tgFake, _ Result, _ error) {
				for _, c := range f.calls[2:] {
					if c.Fields["chat_id"] != "@chan" {
						t.Errorf("delete in chat %q, want the channel", c.Fields["chat_id"])
					}
				}
			}},
		},
		{
			name:   "text refused after a single photo: the photo is taken back",
			post:   &Post{Body: overCaption},
			media:  []string{"a.jpg"},
			fail:   map[string]int{"sendMessage": 400},
			want:   []string{"sendPhoto", "sendMessage", "deleteMessage 100"},
			checks: []check{refused("400")},
		},
		{
			name:  "text refused and the album can't be removed: half sent with every album id",
			post:  &Post{Body: overCaption},
			media: []string{"a.jpg", "b.jpg"},
			fail:  map[string]int{"sendMessage": 400, "deleteMessage": 403},
			want:  []string{"sendMediaGroup", "sendMessage", "deleteMessage 100", "deleteMessage 101"},
			checks: []check{func(t *testing.T, _ *tgFake, _ Result, err error) {
				hs, ok := err.(*halfSentError)
				if !ok || classify(err) != resultHalfSent {
					t.Fatalf("err = %v, want *halfSentError", err)
				}
				if !slices.Equal(hs.Sent, []string{"100", "101"}) {
					t.Errorf("Sent = %q, want both album ids", hs.Sent)
				}
			}},
		},
		{
			name: "connection dropped: outcome unknown, token not in the error",
			post: &Post{Body: short},
			fail: map[string]int{"sendMessage": 0},
			want: []string{"sendMessage"},
			checks: []check{func(t *testing.T, _ *tgFake, _ Result, err error) {
				if classify(err) != resultLost {
					t.Errorf("classify(%v) = %s, want lost", err, classify(err))
				}
				if err != nil && strings.Contains(err.Error(), "SECRET") {
					t.Errorf("token leaked: %v", err)
				}
			}},
		},
		{
			name: "unreadable success: outcome unknown",
			post: &Post{Body: short},
			raw:  map[string]string{"sendMessage": "<html>proxy</html>"},
			want: []string{"sendMessage"},
			checks: []check{func(t *testing.T, _ *tgFake, _ Result, err error) {
				if classify(err) != resultLost {
					t.Errorf("classify(%v) = %s, want lost", err, classify(err))
				}
			}},
		},
		{
			name:  "empty album answer: outcome unknown",
			post:  &Post{Body: short},
			media: []string{"a.jpg", "b.jpg"},
			raw:   map[string]string{"sendMediaGroup": `{"ok":true,"result":[]}`},
			want:  []string{"sendMediaGroup"},
			checks: []check{func(t *testing.T, _ *tgFake, _ Result, err error) {
				if classify(err) != resultLost {
					t.Errorf("classify(%v) = %s, want lost", err, classify(err))
				}
			}},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &tgFake{fail: tc.fail, raw: tc.raw}
			srv := httptest.NewServer(http.HandlerFunc(f.serve))
			defer srv.Close()
			chat := tc.chat
			if chat == "" {
				chat = "@chan"
			}
			if tc.post.Mode == "" {
				tc.post.Mode = modeFull
			}
			if len(tc.media) > 0 {
				tc.post.Media = mediaFiles(t, tc.media...)
			}
			var routed []string
			tg := &Telegram{Token: tgToken, Chat: chat, API: srv.URL, Route: func(_ context.Context, u string, want routeWant) error {
				routed = append(routed, fmt.Sprintf("%s %d", u, want))
				return nil
			}}
			res, err := tg.Publish(t.Context(), tc.post)
			if got := f.methods(); !slices.Equal(got, tc.want) {
				t.Errorf("calls %q, want %q (err %v)", got, tc.want, err)
			}
			if len(f.calls) > 0 && !slices.Equal(routed, []string{fmt.Sprintf("%s %d", srv.URL, viaVPN)}) {
				t.Errorf("route checks %q: want the API host checked once for the VPN before sending", routed)
			}
			for _, c := range tc.checks {
				c(t, f, res, err)
			}
		})
	}
}

func TestTelegramPublishRefusals(t *testing.T) {
	tests := []struct {
		name  string
		media func(t *testing.T) []string
		route error
	}{
		{name: "route not through the VPN", route: fmt.Errorf("api goes direct")},
		{name: "media file missing", media: func(t *testing.T) []string { return []string{filepath.Join(t.TempDir(), "gone.jpg")} }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &tgFake{}
			srv := httptest.NewServer(http.HandlerFunc(f.serve))
			defer srv.Close()
			p := &Post{Mode: modeFull, Body: "hi"}
			if tc.media != nil {
				p.Media = tc.media(t)
			}
			tg := &Telegram{Token: tgToken, Chat: "@chan", API: srv.URL, Route: func(context.Context, string, routeWant) error { return tc.route }}
			_, err := tg.Publish(t.Context(), p)
			if err == nil || classify(err) != resultRejected {
				t.Errorf("err = %v (%s), want a rejection", err, classify(err))
			}
			if len(f.calls) != 0 {
				t.Errorf("calls %q, want none", f.methods())
			}
		})
	}
}
