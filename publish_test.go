package main

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// Fake targets. The three types differ only in which optional interfaces
// they implement, which is what publishAll dispatches on.

type fakeTarget struct {
	name    string
	events  *[]string
	publish func(p *Post) (Result, error) // nil: success with id "<name>-new"
}

func (f *fakeTarget) Name() string { return f.name }

func (f *fakeTarget) Publish(_ context.Context, p *Post) (Result, error) {
	*f.events = append(*f.events, f.name+" publish")
	if f.publish == nil {
		return Result{ID: f.name + "-new", URL: "https://x/" + f.name + "-new"}, nil
	}
	return f.publish(p)
}

type histTarget struct {
	*fakeTarget
	find func(sinceID int64, hash string) (Result, bool, error) // nil: not found
}

func (h *histTarget) FindPost(_ context.Context, sinceID int64, hash string) (Result, bool, error) {
	*h.events = append(*h.events, fmt.Sprintf("%s find since=%d", h.name, sinceID))
	if h.find == nil {
		return Result{}, false, nil
	}
	return h.find(sinceID, hash)
}

type cleanTarget struct {
	*histTarget
	cleanup func(ids []string) error
}

func (c *cleanTarget) Cleanup(_ context.Context, ids []string) error {
	*c.events = append(*c.events, fmt.Sprintf("%s cleanup %s", c.name, strings.Join(ids, ",")))
	if c.cleanup == nil {
		return nil
	}
	return c.cleanup(ids)
}

func plainT(ev *[]string, name string, pub func(*Post) (Result, error)) *fakeTarget {
	return &fakeTarget{name: name, events: ev, publish: pub}
}

func histT(ev *[]string, name string, pub func(*Post) (Result, error), find func(int64, string) (Result, bool, error)) *histTarget {
	return &histTarget{fakeTarget: plainT(ev, name, pub), find: find}
}

func cleanT(ev *[]string, name string, pub func(*Post) (Result, error), find func(int64, string) (Result, bool, error), clean func([]string) error) *cleanTarget {
	return &cleanTarget{histTarget: histT(ev, name, pub, find), cleanup: clean}
}

func fail(err error) func(*Post) (Result, error) {
	return func(*Post) (Result, error) { return Result{}, err }
}

var (
	errLost     = &url.Error{Op: "Post", URL: "https://api", Err: errors.New("connection reset")}
	errRejected = &httpError{Status: 400, Body: "bad request"}
)

const dirA = "posts/a"

// The post as written and as a platform shows it: hashes are of the shown
// text, so markup and whitespace must not matter.
func postA() *Post {
	return &Post{Dir: dirA, Mode: modeFull, Body: "Hello **world**, [see](https://e.x/1)"}
}

var hashA = textHash("Hello world, see")

func newJournal(t *testing.T, entries ...Entry) *Journal {
	t.Helper()
	j := &Journal{path: filepath.Join(t.TempDir(), "journal.json"), Entries: slices.Clone(entries)}
	if err := j.save(); err != nil {
		t.Fatal(err)
	}
	return j
}

func diskEntries(t *testing.T, j *Journal) []Entry {
	t.Helper()
	got, err := loadJournal(j.path)
	if err != nil {
		t.Fatalf("journal on disk unreadable: %v", err)
	}
	return got.Entries
}

func entryKey(e Entry) string {
	return fmt.Sprintf("%s|%s|%s|%s|id=%s|url=%s|sent=%v", e.Target, e.Dir, e.Hash, e.Status, e.ID, e.URL, e.Sent)
}

func sameEntries(t *testing.T, got, want []Entry) {
	t.Helper()
	g := make([]string, len(got))
	for i, e := range got {
		g[i] = entryKey(e)
	}
	w := make([]string, len(want))
	for i, e := range want {
		w[i] = entryKey(e)
	}
	slices.Sort(g)
	slices.Sort(w)
	if !slices.Equal(g, w) {
		t.Errorf("journal on disk:\n got  %q\n want %q", g, w)
	}
}

func observed() (*zap.Logger, *observer.ObservedLogs) {
	core, logs := observer.New(zapcore.DebugLevel)
	return zap.New(core), logs
}

func TestPublishAll(t *testing.T) {
	posted := func(target, dir, hash, id string) Entry {
		return Entry{Target: target, Dir: dir, Hash: hash, Status: statusPosted, ID: id, URL: "https://x/" + id}
	}
	pending := func(target, dir, hash string, sent ...string) Entry {
		return Entry{Target: target, Dir: dir, Hash: hash, Status: statusPending, Sent: sent}
	}
	newPosted := func(target string) Entry { return posted(target, dirA, hashA, target+"-new") }
	found := func(int64, string) (Result, bool, error) {
		return Result{ID: "77", URL: "https://x/77"}, true, nil
	}
	notFound := func(int64, string) (Result, bool, error) { return Result{}, false, nil }

	tests := []struct {
		name       string
		journal    []Entry
		targets    func(ev *[]string) []Target
		force      bool
		wantFailed []string
		wantEvents []string
		want       []Entry
		wantWarn   string // a warning the author must see
	}{
		{
			name: "fresh post goes to every target in order",
			targets: func(ev *[]string) []Target {
				return []Target{plainT(ev, "tg", nil), histT(ev, "max", nil, nil), plainT(ev, "vk", nil)}
			},
			wantEvents: []string{"tg publish", "max publish", "vk publish"},
			want:       []Entry{newPosted("tg"), newPosted("max"), newPosted("vk")},
		},
		{
			name:       "posted entry for the folder: skipped, history not consulted",
			journal:    []Entry{posted("tg", dirA, hashA, "5")},
			targets:    func(ev *[]string) []Target { return []Target{histT(ev, "tg", nil, found), plainT(ev, "vk", nil)} },
			wantEvents: []string{"vk publish"},
			want:       []Entry{posted("tg", dirA, hashA, "5"), newPosted("vk")},
		},
		{
			name:       "posted entry for the folder with an edited text: still skipped",
			journal:    []Entry{posted("tg", dirA, textHash("older text"), "5")},
			targets:    func(ev *[]string) []Target { return []Target{plainT(ev, "tg", nil)} },
			wantEvents: nil,
			want:       []Entry{posted("tg", dirA, textHash("older text"), "5")},
		},
		{
			name:       "same text posted from another folder (different markup and spacing): skipped",
			journal:    []Entry{posted("tg", "posts/old", textHash("Hello   world,\nsee"), "5")},
			targets:    func(ev *[]string) []Target { return []Target{plainT(ev, "tg", nil)} },
			wantEvents: nil,
			want:       []Entry{posted("tg", "posts/old", textHash("Hello world, see"), "5")},
		},
		{
			name:       "same text posted on another target does not count",
			journal:    []Entry{posted("vk", dirA, hashA, "5")},
			targets:    func(ev *[]string) []Target { return []Target{plainT(ev, "tg", nil)} },
			wantEvents: []string{"tg publish"},
			want:       []Entry{posted("vk", dirA, hashA, "5"), newPosted("tg")},
		},
		{
			name:       "other folder, other text: posted",
			journal:    []Entry{posted("tg", "posts/old", textHash("something else"), "5")},
			targets:    func(ev *[]string) []Target { return []Target{plainT(ev, "tg", nil)} },
			wantEvents: []string{"tg publish"},
			want:       []Entry{posted("tg", "posts/old", textHash("something else"), "5"), newPosted("tg")},
		},
		{
			name: "rejected: no trace left, the next target still goes",
			targets: func(ev *[]string) []Target {
				return []Target{plainT(ev, "tg", fail(errRejected)), plainT(ev, "vk", nil)}
			},
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg publish", "vk publish"},
			want:       []Entry{newPosted("vk")},
		},
		{
			name: "rejected wrapped in context: still rejected",
			targets: func(ev *[]string) []Target {
				return []Target{plainT(ev, "tg", fail(fmt.Errorf("upload: %w", errRejected)))}
			},
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg publish"},
			want:       nil,
		},
		{
			name: "transport error: outcome unknown, pending kept",
			targets: func(ev *[]string) []Target {
				return []Target{plainT(ev, "tg", fail(fmt.Errorf("send: %w", errLost))), plainT(ev, "vk", nil)}
			},
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg publish", "vk publish"},
			want:       []Entry{pending("tg", dirA, hashA), newPosted("vk")},
		},
		{
			name: "unreadable 2xx: outcome unknown, pending kept",
			targets: func(ev *[]string) []Target {
				return []Target{plainT(ev, "tg", fail(fmt.Errorf("%w: decode", errAmbiguous)))}
			},
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg publish"},
			want:       []Entry{pending("tg", dirA, hashA)},
		},
		{
			name: "half sent: pending with the sent ids",
			targets: func(ev *[]string) []Target {
				return []Target{plainT(ev, "tg", fail(&halfSentError{Sent: []string{"10", "11"}, Err: errRejected}))}
			},
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg publish"},
			want:       []Entry{pending("tg", dirA, hashA, "10", "11")},
		},
		{
			name: "half sent wrapped: still recorded as half sent",
			targets: func(ev *[]string) []Target {
				return []Target{plainT(ev, "tg", fail(fmt.Errorf("tg: %w", &halfSentError{Sent: []string{"10"}, Err: errLost})))}
			},
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg publish"},
			want:       []Entry{pending("tg", dirA, hashA, "10")},
		},
		{
			name:       "pending, platform can't be checked: skipped, never posted blindly",
			journal:    []Entry{pending("tg", dirA, hashA)},
			targets:    func(ev *[]string) []Target { return []Target{plainT(ev, "tg", nil), plainT(ev, "vk", nil)} },
			wantFailed: []string{"tg"},
			wantEvents: []string{"vk publish"},
			want:       []Entry{pending("tg", dirA, hashA), newPosted("vk")},
		},
		{
			name:    "pending, history check fails: skipped",
			journal: []Entry{pending("tg", dirA, hashA)},
			targets: func(ev *[]string) []Target {
				return []Target{histT(ev, "tg", nil, func(int64, string) (Result, bool, error) { return Result{}, false, errors.New("no log chat") })}
			},
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg find since=0"},
			want:       []Entry{pending("tg", dirA, hashA)},
		},
		{
			name:       "pending, found in history: recorded as posted with a warning, not posted again",
			journal:    []Entry{pending("tg", dirA, hashA)},
			targets:    func(ev *[]string) []Target { return []Target{histT(ev, "tg", nil, found)} },
			wantEvents: []string{"tg find since=0"},
			want:       []Entry{posted("tg", dirA, hashA, "77")},
			wantWarn:   "found on the platform",
		},
		{
			name:       "pending, not in history: posted",
			journal:    []Entry{pending("tg", dirA, hashA)},
			targets:    func(ev *[]string) []Target { return []Target{histT(ev, "tg", nil, notFound)} },
			wantEvents: []string{"tg find since=0", "tg publish"},
			want:       []Entry{newPosted("tg")},
		},
		{
			name:       "history scan starts after the newest id the journal knows for this target",
			journal:    []Entry{posted("tg", "p/1", "h1", "40"), pending("tg", dirA, hashA, "42", "41"), posted("tg", "p/2", "h2", "x9"), posted("vk", "p/3", "h3", "900")},
			targets:    func(ev *[]string) []Target { return []Target{cleanT(ev, "tg", nil, notFound, nil)} },
			wantEvents: []string{"tg find since=42", "tg cleanup 42,41", "tg publish"},
			want:       []Entry{posted("tg", "p/1", "h1", "40"), newPosted("tg"), posted("tg", "p/2", "h2", "x9"), posted("vk", "p/3", "h3", "900")},
		},
		{
			name:       "pending for the same text in another folder: checked, not posted blindly",
			journal:    []Entry{pending("tg", "posts/old", hashA)},
			targets:    func(ev *[]string) []Target { return []Target{plainT(ev, "tg", nil)} },
			wantFailed: []string{"tg"},
			wantEvents: nil,
			want:       []Entry{pending("tg", "posts/old", hashA)},
		},
		{
			name:       "half sent, not in history: the part is removed before the retry",
			journal:    []Entry{pending("tg", dirA, hashA, "10", "11")},
			targets:    func(ev *[]string) []Target { return []Target{cleanT(ev, "tg", nil, notFound, nil)} },
			wantEvents: []string{"tg find since=11", "tg cleanup 10,11", "tg publish"},
			want:       []Entry{newPosted("tg")},
		},
		{
			name:       "half sent with a single id: removed before the retry",
			journal:    []Entry{pending("tg", dirA, hashA, "10")},
			targets:    func(ev *[]string) []Target { return []Target{cleanT(ev, "tg", nil, notFound, nil)} },
			wantEvents: []string{"tg find since=10", "tg cleanup 10", "tg publish"},
			want:       []Entry{newPosted("tg")},
		},
		{
			name:    "half sent, part can't be removed: not posted again, ids kept",
			journal: []Entry{pending("tg", dirA, hashA, "10", "11")},
			targets: func(ev *[]string) []Target {
				return []Target{cleanT(ev, "tg", nil, notFound, func([]string) error { return errors.New("forbidden") }), plainT(ev, "vk", nil)}
			},
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg find since=11", "tg cleanup 10,11", "vk publish"},
			want:       []Entry{pending("tg", dirA, hashA, "10", "11"), newPosted("vk")},
		},
		{
			name:       "half sent but found in history: recorded, nothing removed",
			journal:    []Entry{pending("tg", dirA, hashA, "10")},
			targets:    func(ev *[]string) []Target { return []Target{cleanT(ev, "tg", nil, found, nil)} },
			wantEvents: []string{"tg find since=10"},
			want:       []Entry{posted("tg", dirA, hashA, "77")},
		},
		{
			name:       "plain pending (nothing sent): no cleanup call",
			journal:    []Entry{pending("tg", dirA, hashA)},
			targets:    func(ev *[]string) []Target { return []Target{cleanT(ev, "tg", nil, notFound, nil)} },
			wantEvents: []string{"tg find since=0", "tg publish"},
			want:       []Entry{newPosted("tg")},
		},
		{
			name:       "-force: posts again over a posted entry",
			journal:    []Entry{posted("tg", dirA, hashA, "5")},
			targets:    func(ev *[]string) []Target { return []Target{histT(ev, "tg", nil, found)} },
			force:      true,
			wantEvents: []string{"tg publish"},
			want:       []Entry{newPosted("tg")},
		},
		{
			name:       "-force, rejected: the old posted record survives",
			journal:    []Entry{posted("tg", dirA, hashA, "5")},
			targets:    func(ev *[]string) []Target { return []Target{plainT(ev, "tg", fail(errRejected))} },
			force:      true,
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg publish"},
			want:       []Entry{posted("tg", dirA, hashA, "5")},
		},
		{
			name:       "-force, lost: the old posted record is not downgraded",
			journal:    []Entry{posted("tg", dirA, hashA, "5")},
			targets:    func(ev *[]string) []Target { return []Target{plainT(ev, "tg", fail(errLost))} },
			force:      true,
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg publish"},
			want:       []Entry{posted("tg", dirA, hashA, "5")},
		},
		{
			name:       "-force over pending: no history check",
			journal:    []Entry{pending("tg", dirA, hashA)},
			targets:    func(ev *[]string) []Target { return []Target{histT(ev, "tg", nil, found)} },
			force:      true,
			wantEvents: []string{"tg publish"},
			want:       []Entry{newPosted("tg")},
		},
		{
			name:       "rejected retry after a pending entry: pending restored, not dropped",
			journal:    []Entry{pending("tg", dirA, hashA)},
			targets:    func(ev *[]string) []Target { return []Target{histT(ev, "tg", fail(errRejected), notFound)} },
			wantFailed: []string{"tg"},
			wantEvents: []string{"tg find since=0", "tg publish"},
			want:       []Entry{pending("tg", dirA, hashA)},
		},
		{
			name: "several failures are all reported, in target order",
			targets: func(ev *[]string) []Target {
				return []Target{plainT(ev, "tg", fail(errLost)), plainT(ev, "max", nil), plainT(ev, "vk", fail(errRejected))}
			},
			wantFailed: []string{"tg", "vk"},
			wantEvents: []string{"tg publish", "max publish", "vk publish"},
			want:       []Entry{pending("tg", dirA, hashA), newPosted("max")},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := newJournal(t, tc.journal...)
			var ev []string
			log, logs := observed()
			failed, err := publishAll(t.Context(), log, postA(), tc.targets(&ev), j, tc.force)
			if err != nil {
				t.Fatalf("publishAll: %v", err)
			}
			if !slices.Equal(failed, tc.wantFailed) {
				t.Errorf("failed = %q, want %q", failed, tc.wantFailed)
			}
			if !slices.Equal(ev, tc.wantEvents) {
				t.Errorf("calls:\n got  %q\n want %q", ev, tc.wantEvents)
			}
			sameEntries(t, diskEntries(t, j), tc.want)
			if tc.wantWarn != "" && logs.FilterLevelExact(zapcore.WarnLevel).FilterMessageSnippet(tc.wantWarn).Len() == 0 {
				t.Errorf("no warning containing %q; logs: %v", tc.wantWarn, logs.All())
			}
			if len(tc.wantFailed) > 0 && logs.Filter(func(e observer.LoggedEntry) bool { return e.Level >= zapcore.WarnLevel }).Len() < len(tc.wantFailed) {
				t.Errorf("each failed target must be logged at warn or above; logs: %v", logs.All())
			}
		})
	}
}

// The pending record must be on disk before the platform is called: a crash
// inside Publish (the process dies) must leave a trace.
func TestPublishAllWritesPendingBeforeCalling(t *testing.T) {
	tests := []struct {
		name    string
		journal []Entry
		force   bool
	}{
		{name: "fresh"},
		{name: "after a not-found pending", journal: []Entry{{Target: "tg", Dir: dirA, Hash: hashA, Status: statusPending, Sent: []string{"3"}}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := newJournal(t, tc.journal...)
			var ev []string
			var seen []Entry
			tg := cleanT(&ev, "tg", func(*Post) (Result, error) {
				seen = diskEntries(t, j) // what a crash at this point would leave
				return Result{ID: "9"}, nil
			}, nil, nil)
			if _, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg}, j, tc.force); err != nil {
				t.Fatal(err)
			}
			sameEntries(t, seen, []Entry{{Target: "tg", Dir: dirA, Hash: hashA, Status: statusPending}})
		})
	}
}

// A run that crashed or lost its answer, then reruns: the post must reach
// the platform exactly once over all runs.
func TestPublishAllAcrossRuns(t *testing.T) {
	tests := []struct {
		name string
		// first run's answer, and whether the post actually reached the platform
		first        error
		reached      bool
		wantPublish  int // over both runs
		wantPostedID string
	}{
		{name: "lost answer, post did go out", first: errLost, reached: true, wantPublish: 1, wantPostedID: "found"},
		{name: "lost answer, post did not go out", first: errLost, reached: false, wantPublish: 2, wantPostedID: "second"},
		{name: "unreadable success", first: fmt.Errorf("%w: x", errAmbiguous), reached: true, wantPublish: 1, wantPostedID: "found"},
		{name: "rejected", first: errRejected, reached: false, wantPublish: 2, wantPostedID: "second"},
		{name: "success", first: nil, reached: true, wantPublish: 1, wantPostedID: "first"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.json")
			var onPlatform []string
			var ev []string
			calls := 0
			tg := histT(&ev, "tg", func(p *Post) (Result, error) {
				calls++
				id := map[int]string{1: "first", 2: "second"}[calls]
				if calls == 1 && !tc.reached {
					return Result{}, tc.first
				}
				onPlatform = append(onPlatform, textHash(toPlainNoURL(p.PublicText())))
				if calls == 1 {
					return Result{ID: id}, tc.first
				}
				return Result{ID: id}, nil
			}, func(_ int64, hash string) (Result, bool, error) {
				if slices.Contains(onPlatform, hash) {
					return Result{ID: "found"}, true, nil
				}
				return Result{}, false, nil
			})
			for run := range 2 {
				j, err := loadJournal(path) // each run starts from disk, like a new process
				if err != nil {
					t.Fatal(err)
				}
				if _, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg}, j, false); err != nil {
					t.Fatalf("run %d: %v", run, err)
				}
			}
			if calls != tc.wantPublish {
				t.Errorf("Publish called %d times, want %d", calls, tc.wantPublish)
			}
			if len(onPlatform) != 1 {
				t.Errorf("post reached the platform %d times, want exactly 1", len(onPlatform))
			}
			j, err := loadJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			if e := j.entryFor("tg", dirA); e == nil || e.Status != statusPosted || e.ID != tc.wantPostedID {
				t.Errorf("final entry %+v, want posted id %q", e, tc.wantPostedID)
			}
		})
	}
}

// If the pending record can't be written, the platform must not be called.
func TestPublishAllSaveFails(t *testing.T) {
	tests := []struct {
		name    string
		journal []Entry
		find    func(int64, string) (Result, bool, error)
	}{
		{name: "fresh post"},
		{name: "found in history", journal: []Entry{{Target: "tg", Dir: dirA, Hash: hashA, Status: statusPending}},
			find: func(int64, string) (Result, bool, error) { return Result{ID: "1"}, true, nil }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := &Journal{path: filepath.Join(t.TempDir(), "missing-dir", "journal.json"), Entries: slices.Clone(tc.journal)}
			var ev []string
			tg := histT(&ev, "tg", nil, tc.find)
			_, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg, plainT(&ev, "vk", nil)}, j, false)
			if err == nil {
				t.Fatal("want an error when the journal can't be saved")
			}
			if slices.ContainsFunc(ev, func(s string) bool { return strings.HasSuffix(s, "publish") }) {
				t.Errorf("a platform was called without a saved journal: %q", ev)
			}
		})
	}
}

// Paid targets are matched by the full text, free ones by what they show.
func TestPublishAllHashPerAudience(t *testing.T) {
	p := &Post{Dir: dirA, Mode: modeTeaser, Body: "Full **body**", Teaser: "Short", PaidURL: "https://boosty.to/x/1"}
	j := newJournal(t,
		Entry{Target: "boosty", Dir: "posts/old", Hash: textHash("Full body"), Status: statusPosted, ID: "b"},
		Entry{Target: "tg", Dir: "posts/old", Hash: textHash("Short https://boosty.to/x/1"), Status: statusPosted, ID: "t"},
		Entry{Target: "vk", Dir: "posts/old", Hash: textHash("Full body"), Status: statusPosted, ID: "v"},
	)
	var ev []string
	_, err := publishAll(t.Context(), zap.NewNop(), p, []Target{plainT(&ev, "boosty", nil), plainT(&ev, "tg", nil), plainT(&ev, "vk", nil)}, j, false)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"vk publish"}; !slices.Equal(ev, want) {
		t.Errorf("calls %q, want %q (boosty and tg already have this text, vk had only the full one)", ev, want)
	}
	if e := j.entryFor("vk", dirA); e == nil || e.Hash != textHash("Short https://boosty.to/x/1") {
		t.Errorf("vk entry %+v: want the hash of the public teaser", e)
	}
}

func TestJournalPut(t *testing.T) {
	p := Entry{Target: "tg", Dir: dirA, Hash: "h", Status: statusPending}
	d := Entry{Target: "tg", Dir: dirA, Hash: "h", Status: statusPosted, ID: "1"}
	tests := []struct {
		name  string
		start []Entry
		put   Entry
		want  []Entry
	}{
		{"new entry appended", nil, p, []Entry{p}},
		{"pending becomes posted", []Entry{p}, d, []Entry{d}},
		{"posted never downgraded to pending", []Entry{d}, p, []Entry{d}},
		{"posted replaced by a newer posted", []Entry{d}, Entry{Target: "tg", Dir: dirA, Status: statusPosted, ID: "2"}, []Entry{{Target: "tg", Dir: dirA, Status: statusPosted, ID: "2"}}},
		{"pending replaced, sent ids dropped", []Entry{{Target: "tg", Dir: dirA, Status: statusPending, Sent: []string{"1"}}}, p, []Entry{p}},
		{"other folder is another entry", []Entry{d}, Entry{Target: "tg", Dir: "b", Status: statusPending}, []Entry{d, {Target: "tg", Dir: "b", Status: statusPending}}},
		{"other target is another entry", []Entry{d}, Entry{Target: "vk", Dir: dirA, Status: statusPending}, []Entry{d, {Target: "vk", Dir: dirA, Status: statusPending}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := &Journal{Entries: slices.Clone(tc.start)}
			j.put(tc.put)
			sameEntries(t, j.Entries, tc.want)
			if e := j.entryFor(tc.put.Target, tc.put.Dir); e == nil || (entryKey(*e) == entryKey(tc.put) && e.At.IsZero()) {
				t.Errorf("entry %+v: want a time stamp", e)
			}
		})
	}
}

func TestJournalFind(t *testing.T) {
	entries := []Entry{
		{Target: "tg", Dir: "a", Hash: "h1", Status: statusPending, ID: "pa"},
		{Target: "tg", Dir: "b", Hash: "h1", Status: statusPosted, ID: "pb"},
		{Target: "tg", Dir: "c", Hash: "h3", Status: statusPending, ID: "pc"},
		{Target: "vk", Dir: "d", Hash: "h4", Status: statusPosted, ID: "vd"},
	}
	tests := []struct {
		name, target, dir, hash, wantID string
	}{
		{"by folder", "tg", "c", "zzz", "pc"},
		{"by text", "tg", "new", "h3", "pc"},
		{"posted preferred over pending", "tg", "a", "h1", "pb"},
		{"own pending found by folder", "tg", "a", "zzz", "pa"},
		{"other target ignored", "tg", "d", "h4", ""},
		{"no match", "tg", "new", "zzz", ""},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := &Journal{Entries: slices.Clone(entries)}
			got := ""
			if e := j.find(tc.target, tc.dir, tc.hash); e != nil {
				got = e.ID
			}
			if got != tc.wantID {
				t.Errorf("find = %q, want %q", got, tc.wantID)
			}
		})
	}
}

func TestJournalRestore(t *testing.T) {
	old := Entry{Target: "tg", Dir: dirA, Status: statusPosted, ID: "old"}
	cur := Entry{Target: "tg", Dir: dirA, Status: statusPending}
	other := Entry{Target: "vk", Dir: dirA, Status: statusPosted, ID: "v"}
	tests := []struct {
		name  string
		start []Entry
		prev  *Entry
		want  []Entry
	}{
		{"no previous: the attempt's entry removed", []Entry{other, cur}, nil, []Entry{other}},
		{"previous put back", []Entry{cur, other}, &old, []Entry{old, other}},
		{"previous re-added when missing", []Entry{other}, &old, []Entry{other, old}},
		{"nothing to do", []Entry{other}, nil, []Entry{other}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := &Journal{Entries: slices.Clone(tc.start)}
			j.restore("tg", dirA, tc.prev)
			sameEntries(t, j.Entries, tc.want)
		})
	}
}

func TestJournalLastID(t *testing.T) {
	tests := []struct {
		name    string
		entries []Entry
		want    int64
	}{
		{"empty", nil, 0},
		{"highest of ids", []Entry{{Target: "tg", ID: "9"}, {Target: "tg", ID: "120"}, {Target: "tg", ID: "15"}}, 120},
		{"sent ids count", []Entry{{Target: "tg", ID: "9", Sent: []string{"30", "31"}}}, 31},
		{"numeric order, not string order", []Entry{{Target: "tg", ID: "9"}, {Target: "tg", ID: "10"}}, 10},
		{"other targets ignored", []Entry{{Target: "tg", ID: "9"}, {Target: "max", ID: "99999"}}, 9},
		{"non-numeric ids ignored", []Entry{{Target: "tg", ID: "abc"}, {Target: "tg", ID: "4"}}, 4},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			j := &Journal{Entries: tc.entries}
			if got := j.lastID("tg"); got != tc.want {
				t.Errorf("lastID = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestJournalDisk(t *testing.T) {
	tests := []struct {
		name    string
		content *string // nil: no file
		wantErr bool
		wantN   int
	}{
		{name: "no file: empty journal", content: nil},
		{name: "valid file", content: new(`{"entries":[{"target":"tg","dir":"a","hash":"h","status":"posted","id":"1"}]}`), wantN: 1},
		{name: "corrupted file must fail, not start empty", content: new(`{"entries":[{"target":`), wantErr: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "journal.json")
			if tc.content != nil {
				if err := os.WriteFile(path, []byte(*tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			j, err := loadJournal(path)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tc.wantErr)
			}
			if err != nil {
				return
			}
			if len(j.Entries) != tc.wantN {
				t.Fatalf("entries = %d, want %d", len(j.Entries), tc.wantN)
			}
			e := Entry{Target: "vk", Dir: "b", Hash: "h2", Status: statusPending, Sent: []string{"5", "6"}}
			j.put(e)
			if err := j.save(); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(path + ".tmp"); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("temp file left behind: %v", err)
			}
			back, err := loadJournal(path)
			if err != nil {
				t.Fatal(err)
			}
			if got := back.entryFor("vk", "b"); got == nil || entryKey(*got) != entryKey(e) || got.At.IsZero() {
				t.Errorf("round trip lost data: %+v", got)
			}
		})
	}
}

func TestClassify(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want attemptResult
	}{
		{"nil", nil, resultOK},
		{"transport error", errLost, resultLost},
		{"wrapped transport error", fmt.Errorf("a: %w", fmt.Errorf("b: %w", errLost)), resultLost},
		{"unreadable success", fmt.Errorf("%w: decode", errAmbiguous), resultLost},
		{"joined with unreadable success", errors.Join(errRejected, errAmbiguous), resultLost},
		{"http refusal", errRejected, resultRejected},
		{"local refusal", errors.New("text 5000 chars > 4096"), resultRejected},
		{"context canceled before sending", context.Canceled, resultRejected},
		{"half sent", &halfSentError{Sent: []string{"1"}, Err: errRejected}, resultHalfSent},
		{"half sent over a transport error", &halfSentError{Sent: []string{"1"}, Err: errLost}, resultHalfSent},
		{"wrapped half sent", fmt.Errorf("x: %w", &halfSentError{Sent: []string{"1"}, Err: errRejected}), resultHalfSent},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := classify(tc.err); got != tc.want {
				t.Errorf("classify(%v) = %s, want %s", tc.err, got, tc.want)
			}
		})
	}
}
