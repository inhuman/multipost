package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/inhuman/evident"
	"github.com/inhuman/evident/evidenttest"
	"github.com/inhuman/multipost/spec"
	"github.com/playwright-community/playwright-go"
	"go.uber.org/zap"
)

func TestMain(m *testing.M) {
	os.Exit(evidenttest.Run(m, spec.All...))
}

func eachRow(t *testing.T, req evident.Requirement, fn func(t *testing.T, row evident.Facts)) {
	for i, row := range req.Domain.Rows {
		t.Run(fmt.Sprintf("%03d_%s", i, evident.RowKey(req.Domain.Keys, row)), func(t *testing.T) {
			fn(t, row)
		})
	}
}

func witness(id string, row evident.Facts, key string, out any) {
	f := evident.Facts{key: out}
	for k, v := range row {
		f[k] = v
	}
	evidenttest.Suite.Witness(id, f)
}

func noRoute(context.Context, string, routeWant) error { return nil }

// fakeBotAPI records which Bot API methods were called, in order, and
// whether any media call carried a caption.
type fakeBotAPI struct {
	mu      sync.Mutex
	calls   []string
	caption bool
}

var tgMethods = map[string]string{
	"sendMessage": "message", "sendPhoto": "photo", "sendVideo": "video", "sendMediaGroup": "album",
}

func (f *fakeBotAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	method := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
	if err := r.ParseMultipartForm(1 << 20); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.calls = append(f.calls, tgMethods[method])
	if method != "sendMessage" {
		if r.FormValue("caption") != "" {
			f.caption = true
		}
		var items []struct {
			Caption string `json:"caption"`
		}
		if js := r.FormValue("media"); js != "" && json.Unmarshal([]byte(js), &items) == nil {
			for _, it := range items {
				if it.Caption != "" {
					f.caption = true
				}
			}
		}
	}
	f.mu.Unlock()
	if method == "sendMediaGroup" {
		fmt.Fprint(w, `{"ok":true,"result":[{"message_id":1}]}`)
		return
	}
	fmt.Fprint(w, `{"ok":true,"result":{"message_id":1}}`)
}

func writeFile(t *testing.T, path string, size int64) {
	t.Helper()
	if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if size > 1 {
		// Sparse: a 51 MB file without writing 51 MB.
		if err := os.Truncate(path, size); err != nil {
			t.Fatal(err)
		}
	}
}

// postWithMedia lays out a real post folder for the media class of a row.
func postWithMedia(t *testing.T, media, text string) *Post {
	t.Helper()
	dir := t.TempDir()
	body := map[string]string{
		"short": "Короткий пост",
		"long":  strings.Repeat("длинный ", 250),
		"over":  strings.Repeat("слишком ", 700),
	}[text]
	if err := os.WriteFile(filepath.Join(dir, "post.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	mediaDir := filepath.Join(dir, "media")
	if err := os.Mkdir(mediaDir, 0o755); err != nil {
		t.Fatal(err)
	}
	add := func(name string, size int64) { writeFile(t, filepath.Join(mediaDir, name), size) }
	switch media {
	case "image":
		add("01.jpg", 1)
	case "video":
		add("01.mp4", 1)
	case "album":
		add("01.jpg", 1)
		add("02.mp4", 1)
		add("03.png", 1)
	case "tooMany":
		for i := range 11 {
			add(fmt.Sprintf("%02d.jpg", i), 1)
		}
	case "tooBig":
		add("01.mp4", tgUploadMax+1)
	}
	p, err := loadPost(dir)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTGLayoutOverModel(t *testing.T) {
	eachRow(t, spec.TGLayout, func(t *testing.T, row evident.Facts) {
		api := &fakeBotAPI{}
		srv := httptest.NewServer(api)
		defer srv.Close()
		p := postWithMedia(t, row["media"].(string), row["text"].(string))
		tg := &Telegram{Token: "T", Chat: "@c", API: srv.URL, Route: noRoute}
		_, err := tg.Publish(t.Context(), p)
		witness("tg.layout", row, "outcome", map[string]any{
			"calls": api.calls, "caption": api.caption, "refused": err != nil,
		})
	})
}

func TestRouteGuardOverModel(t *testing.T) {
	devs := map[string]string{"vpn": "amn0", "phys": "enp4s0"}
	wants := map[string]routeWant{"vpn": viaVPN, "direct": viaDirect, "any": viaAny}
	eachRow(t, spec.RouteGuard, func(t *testing.T, row evident.Facts) {
		devOf := map[string]string{}
		var ips []net.IP
		for i, k := range []string{"a", "b"} {
			if iface, ok := row[k].(string); ok {
				ip := net.IPv4(10, 0, 0, byte(i+1))
				ips = append(ips, ip)
				devOf[ip.String()] = devs[iface]
			}
		}
		g := routeGuard{
			lookup: func(context.Context, string) ([]net.IP, error) { return ips, nil },
			dev: func(_ context.Context, ip net.IP) (string, error) {
				return devOf[ip.String()], nil
			},
		}
		err := g.check(t.Context(), "https://host.example/", wants[row["want"].(string)])
		witness("route.guard", row, "allowed", err == nil)
	})
}

func TestTargetsSelectOverModel(t *testing.T) {
	requests := map[string][]string{
		"byDefault": nil, "onlyTg": {"tg"}, "onlyVk": {"vk"}, "onlyMax": {"max"}, "all": {"tg", "max", "vk"},
	}
	eachRow(t, spec.TargetsSelect, func(t *testing.T, row evident.Facts) {
		var cfg Config
		if row["tg"] == "ready" {
			cfg.TGBotToken, cfg.TGChat = "t", "@c"
		}
		switch row["max"] {
		case "ready":
			cfg.MaxBotToken, cfg.MaxChatID = "m", "123"
		case "badId":
			cfg.MaxBotToken, cfg.MaxChatID = "m", "12x"
		}
		if row["vk"] != "unset" {
			cfg.VKURL = "https://vk.com/club1"
		}
		if row["vk"] == "ready" {
			browserFlows["vk"] = func(context.Context, playwright.Page, *Post) (Result, error) { return Result{}, nil }
			t.Cleanup(func() { delete(browserFlows, "vk") })
		}
		targets, err := selectTargets(cfg, requests[row["request"].(string)], zap.NewNop())
		selected := []string{}
		for _, tg := range targets {
			selected = append(selected, tg.Name())
		}
		witness("targets.select", row, "outcome", map[string]any{"selected": selected, "failed": err != nil})
	})
}

// scriptedTarget publishes with a fixed outcome and counts the attempts.
type scriptedTarget struct {
	name     string
	fail     bool
	attempts *[]string
}

func (s *scriptedTarget) Name() string { return s.name }

func (s *scriptedTarget) Publish(context.Context, *Post) (Result, error) {
	*s.attempts = append(*s.attempts, s.name)
	if s.fail {
		return Result{}, errors.New("scripted failure")
	}
	return Result{ID: "1"}, nil
}

func TestNoDoublePostOverModel(t *testing.T) {
	eachRow(t, spec.NoDoublePost, func(t *testing.T, row evident.Facts) {
		dir := t.TempDir()
		state := State{}
		for _, n := range []string{"a", "b"} {
			if row[n+"_posted"] == true {
				state[n] = Posted{Result: Result{ID: "old"}}
			}
		}
		if err := state.save(dir); err != nil {
			t.Fatal(err)
		}
		state, err := loadState(dir)
		if err != nil {
			t.Fatal(err)
		}
		attempts := []string{}
		targets := []Target{
			&scriptedTarget{name: "a", fail: row["a_result"] == "fail", attempts: &attempts},
			&scriptedTarget{name: "b", fail: row["b_result"] == "fail", attempts: &attempts},
		}
		failed, err := publishAll(t.Context(), zap.NewNop(), &Post{Dir: dir}, targets, state, row["force"].(bool))
		if err != nil {
			t.Fatal(err)
		}
		after, err := loadState(dir)
		if err != nil {
			t.Fatal(err)
		}
		recorded := []string{}
		for _, n := range []string{"a", "b"} {
			if _, ok := after[n]; ok {
				recorded = append(recorded, n)
			}
		}
		witness("state.no-double-post", row, "outcome", map[string]any{
			"attempted": attempts, "recorded": recorded, "failed": len(failed) > 0,
		})
	})
}

func TestPostTextOverModel(t *testing.T) {
	eachRow(t, spec.PostText, func(t *testing.T, row evident.Facts) {
		p := &Post{Mode: row["mode"].(string), Body: "BODY", Teaser: "TEASER"}
		if row["link"] == true {
			p.PaidURL = "https://paid.example/1"
		}
		got := p.TextFor(row["paid"].(bool))
		kind := map[string]string{
			"BODY": "body", "TEASER": "teaser", "TEASER\n\nhttps://paid.example/1": "teaserLink",
		}[got]
		if kind == "" {
			kind = "unknown: " + strconv.Quote(got)
		}
		witness("post.text", row, "text", kind)
	})
}
