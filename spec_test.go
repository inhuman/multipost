package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/inhuman/evident"
	"github.com/inhuman/evident/evidenttest"
	"github.com/inhuman/multipost/spec"
	"github.com/playwright-community/playwright-go"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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

// scriptedTarget publishes with a fixed outcome; at the moment of the call
// it reads the journal from disk, as a crash right then would leave it.
type scriptedTarget struct {
	name           string
	result         string
	journal, dir   string
	hash           string
	attempted      bool
	journaledFirst bool
}

func (s *scriptedTarget) Name() string { return s.name }

func (s *scriptedTarget) Publish(context.Context, *Post) (Result, error) {
	s.attempted = true
	if j, err := loadJournal(s.journal); err == nil {
		s.journaledFirst = j.find(s.name, s.dir, s.hash) != nil
	}
	switch s.result {
	case "rejected":
		return Result{}, &httpError{Status: http.StatusBadRequest, Body: "chat not found"}
	case "lost":
		return Result{}, &url.Error{Op: "Post", URL: "https://api.example", Err: errors.New("connection reset by peer")}
	case "halfSent":
		return Result{}, &halfSentError{Sent: []string{"5"}, Err: errors.New("text message failed")}
	}
	return Result{ID: "7"}, nil
}

// listingTarget can also look at its own history.
type listingTarget struct {
	scriptedTarget
	found bool
}

func (l *listingTarget) FindPost(context.Context, int64, string) (Result, bool, error) {
	if l.found {
		return Result{ID: "6"}, true, nil
	}
	return Result{}, false, nil
}

func TestNoDoublePostOverModel(t *testing.T) {
	eachRow(t, spec.NoDoublePost, func(t *testing.T, row evident.Facts) {
		root := t.TempDir()
		journalPath := filepath.Join(root, "journal.json")
		post := &Post{Dir: filepath.Join(root, "2026-10-23-post"), Mode: modeFull, Body: "Пост про **бутылку**"}
		hash := textHash(toPlainNoURL(post.TextFor(false)))

		j, err := loadJournal(journalPath)
		if err != nil {
			t.Fatal(err)
		}
		if rec := row["record"].(string); rec != "none" {
			e := Entry{Target: "a", Dir: post.Dir, Hash: hash, Status: rec, ID: "3"}
			if row["found_by"] == "text" {
				e.Dir, e.Hash = filepath.Join(root, "same-text-other-folder"), hash
			}
			j.put(e)
		}
		if err := j.save(); err != nil {
			t.Fatal(err)
		}

		script := scriptedTarget{name: "a", result: row["result"].(string), journal: journalPath, dir: post.Dir, hash: hash}
		var a Target = &script
		var listing *listingTarget
		if row["remote"] != "cannotList" {
			listing = &listingTarget{scriptedTarget: script, found: row["remote"] == "found"}
			a = listing
		}
		next := &scriptedTarget{name: "next", result: "ok", journal: journalPath, dir: post.Dir}

		core, logs := observer.New(zap.InfoLevel)
		failed, err := publishAll(t.Context(), zap.New(core), post, []Target{a, next}, j, row["force"].(bool))
		if err != nil {
			t.Fatal(err)
		}
		if listing != nil {
			script = listing.scriptedTarget
		}

		after, err := loadJournal(journalPath)
		if err != nil {
			t.Fatal(err)
		}
		record := "none"
		if e := after.find("a", post.Dir, hash); e != nil {
			record = e.Status
		}
		warned := false
		for _, e := range logs.FilterLevelExact(zap.WarnLevel).All() {
			if e.ContextMap()["target"] == "a" {
				warned = true
			}
		}
		witness("state.no-double-post", row, "outcome", map[string]any{
			"attempted": script.attempted, "journaledFirst": script.journaledFirst, "record": record,
			"warned": warned, "failed": slices.Contains(failed, "a"), "nextAttempted": next.attempted,
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
