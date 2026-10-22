package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"go.uber.org/zap"
)

// Bugs found by the evident-vs-units measurement (23.10.2026); each test
// failed on the code before its fix.

// After the half-sent part is removed, a rejected retry must not bring the
// removed ids back: the next run would try to delete them again, fail on
// messages that no longer exist, and the platform would be stuck.
func TestCleanedSentNotRestoredOnRejection(t *testing.T) {
	j := newJournal(t, Entry{Target: "tg", Dir: dirA, Hash: hashA, Status: statusPending, Sent: []string{"3"}})
	var ev []string
	tg := cleanT(&ev, "tg", fail(errRejected), nil, nil)
	if _, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg}, j, false); err != nil {
		t.Fatal(err)
	}
	for _, e := range diskEntries(t, j) {
		if len(e.Sent) > 0 {
			t.Fatalf("removed ids came back: %+v", e)
		}
	}
	ev = nil
	tg.publish = nil
	if _, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg}, j, false); err != nil {
		t.Fatal(err)
	}
	if slices.ContainsFunc(ev, func(s string) bool { return strings.Contains(s, "cleanup") }) {
		t.Errorf("second run cleaned up again: %v", ev)
	}
}

// -force over a half-sent post first removes the half, or it stays in the
// channel next to the new full post.
func TestForceCleansHalfSentFirst(t *testing.T) {
	j := newJournal(t, Entry{Target: "tg", Dir: dirA, Hash: hashA, Status: statusPending, Sent: []string{"3"}})
	var ev []string
	tg := cleanT(&ev, "tg", nil, nil, nil)
	if _, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg}, j, true); err != nil {
		t.Fatal(err)
	}
	if want := []string{"tg cleanup 3", "tg publish"}; !slices.Equal(ev, want) {
		t.Errorf("events %v, want %v", ev, want)
	}
}

// -force over a posted entry that ends half-sent must keep the ids of the
// half, or nothing will ever remove it.
func TestForceOverPostedKeepsHalfSentIDs(t *testing.T) {
	j := newJournal(t, Entry{Target: "tg", Dir: dirA, Hash: hashA, Status: statusPosted, ID: "1"})
	var ev []string
	tg := cleanT(&ev, "tg", fail(&halfSentError{Sent: []string{"7"}, Err: errRejected}), nil, nil)
	if _, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg}, j, true); err != nil {
		t.Fatal(err)
	}
	if e := diskEntries(t, j); len(e) != 1 || !slices.Equal(e[0].Sent, []string{"7"}) {
		t.Fatalf("half-sent ids lost: %+v", e)
	}
	ev = nil
	tg.publish = nil
	if _, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg}, j, false); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(ev, "tg cleanup 7") {
		t.Errorf("next run did not remove the half: %v", ev)
	}
}

func TestClassifyHalfSentBeforeAmbiguous(t *testing.T) {
	err := &halfSentError{Sent: []string{"5"}, Err: fmt.Errorf("%w: text reply", errAmbiguous)}
	if got := classify(err); got != resultHalfSent {
		t.Errorf("classify = %s, want halfSent: the sent ids must not be lost", got)
	}
}

// The history scan for a pending post starts after the newest id known
// when that post was attempted — not after posts that went out later, or
// the pending post is never found and goes out twice.
func TestHistoryScanStartsWhereThePendingPostWas(t *testing.T) {
	j := newJournal(t, Entry{Target: "tg", Dir: "posts/old", Hash: "h-old", Status: statusPosted, ID: "10"})
	var ev []string
	tg := cleanT(&ev, "tg", fail(errLost), nil, nil)
	if _, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg}, j, false); err != nil {
		t.Fatal(err)
	}
	postB := &Post{Dir: "posts/b", Mode: modeFull, Body: "another post"}
	tg.publish = func(*Post) (Result, error) { return Result{ID: "50"}, nil }
	if _, err := publishAll(t.Context(), zap.NewNop(), postB, []Target{tg}, j, false); err != nil {
		t.Fatal(err)
	}
	ev = nil
	if _, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg}, j, false); err != nil {
		t.Fatal(err)
	}
	if len(ev) == 0 || ev[0] != "tg find since=10" {
		t.Errorf("scan for post A: %v, want it to start after id 10", ev)
	}
}

// A pending entry whose post was edited since: the platform may have the
// OLD text, so that is what the history is searched for.
func TestHistorySearchUsesTheAttemptedText(t *testing.T) {
	j := newJournal(t, Entry{Target: "tg", Dir: dirA, Hash: "h-attempted", Status: statusPending})
	var ev []string
	var searched string
	tg := cleanT(&ev, "tg", nil, func(_ int64, hash string) (Result, bool, error) {
		searched = hash
		return Result{ID: "4"}, true, nil
	}, nil)
	if _, err := publishAll(t.Context(), zap.NewNop(), postA(), []Target{tg}, j, false); err != nil {
		t.Fatal(err)
	}
	if searched != "h-attempted" {
		t.Errorf("searched %q, want the hash of the attempted text", searched)
	}
	if slices.Contains(ev, "tg publish") {
		t.Errorf("posted again although the attempt was found: %v", ev)
	}
}

func publishTG(t *testing.T, p *Post) (*tgFake, error) {
	t.Helper()
	f := &tgFake{}
	srv := httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(srv.Close)
	tg := &Telegram{Token: tgToken, Chat: "@chan", API: srv.URL, Route: func(context.Context, string, routeWant) error { return nil }}
	_, err := tg.Publish(t.Context(), p)
	return f, err
}

// Media with no text is just the media: no empty message after it.
func TestTelegramMediaWithoutText(t *testing.T) {
	f, err := publishTG(t, &Post{Mode: modeTeaser, Media: mediaFiles(t, "a.jpg")})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.methods(); !slices.Equal(got, []string{"sendPhoto"}) {
		t.Errorf("calls %v, want only sendPhoto", got)
	}
}

// Telegram counts limits in UTF-16 code units: an emoji is two.
func TestTelegramLimitsInUTF16(t *testing.T) {
	caption := strings.Repeat("😀", 600) // 600 runes, 1200 UTF-16 units
	f, err := publishTG(t, &Post{Mode: modeFull, Body: caption, Media: mediaFiles(t, "a.jpg")})
	if err != nil {
		t.Fatal(err)
	}
	if got := f.methods(); !slices.Equal(got, []string{"sendPhoto", "sendMessage"}) {
		t.Errorf("calls %v: a 1200-unit text can't be a caption", got)
	}
	if c := f.calls[0].Fields["caption"]; c != "" {
		t.Errorf("caption sent: %d chars", len(c))
	}

	f, err = publishTG(t, &Post{Mode: modeFull, Body: strings.Repeat("😀", 3000)})
	if err == nil || len(f.methods()) != 0 {
		t.Errorf("6000-unit text: err %v, calls %v; want refusal before any call", err, f.methods())
	}
}
