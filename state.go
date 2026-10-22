package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Journal is the project-wide record of where posts went (spec:
// NoDoublePost). An entry is pending from just before a platform is called
// until its answer, then posted: whatever may have reached a platform is
// never forgotten. It lives next to the project's .env.
type Journal struct {
	path    string
	Entries []Entry `json:"entries"`
}

type Entry struct {
	Target string   `json:"target"`
	Dir    string   `json:"dir"`
	Hash   string   `json:"hash"`
	Status string   `json:"status"` // pending | posted
	ID     string   `json:"id,omitzero"`
	URL    string   `json:"url,omitzero"`
	Sent   []string `json:"sent,omitzero"` // ids of a half-sent post, to clean up before the retry
	// SinceID is the newest message id known when the attempt started:
	// a history scan for a pending post starts after it.
	SinceID int64     `json:"since_id,omitzero"`
	At      time.Time `json:"at"`
}

const (
	statusPending = "pending"
	statusPosted  = "posted"
)

func loadJournal(path string) (*Journal, error) {
	j := &Journal{path: path}
	raw, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return j, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, j); err != nil {
		return nil, err
	}
	return j, nil
}

// save writes via a temp file and rename: a crash mid-write must not leave
// a truncated journal, or every record would be lost at once.
func (j *Journal) save() error {
	raw, err := json.MarshalIndent(j, "", "  ")
	if err != nil {
		return err
	}
	tmp := j.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, j.path)
}

// find returns the entry for this target matching the post's folder or its
// text, preferring posted over pending.
func (j *Journal) find(target, dir, hash string) *Entry {
	var found *Entry
	for i := range j.Entries {
		e := &j.Entries[i]
		if e.Target != target || (e.Dir != dir && e.Hash != hash) {
			continue
		}
		if found == nil || e.Status == statusPosted {
			found = e
		}
	}
	return found
}

func (j *Journal) entryFor(target, dir string) *Entry {
	for i := range j.Entries {
		if j.Entries[i].Target == target && j.Entries[i].Dir == dir {
			return &j.Entries[i]
		}
	}
	return nil
}

// put writes the entry for its target and folder; posted is never
// downgraded to pending.
func (j *Journal) put(e Entry) {
	e.At = time.Now()
	if cur := j.entryFor(e.Target, e.Dir); cur != nil {
		if cur.Status == statusPosted && e.Status == statusPending {
			// Still posted, but a half-sent fragment of a forced repost
			// must stay known until it is removed.
			cur.Sent = e.Sent
			return
		}
		*cur = e
		return
	}
	j.Entries = append(j.Entries, e)
}

// restore puts the entry for this target and folder back as it was before
// a rejected attempt (nil: there was none).
func (j *Journal) restore(target, dir string, prev *Entry) {
	i := slices.IndexFunc(j.Entries, func(e Entry) bool { return e.Target == target && e.Dir == dir })
	switch {
	case i < 0 && prev != nil:
		j.Entries = append(j.Entries, *prev)
	case i >= 0 && prev == nil:
		j.Entries = slices.Delete(j.Entries, i, i+1)
	case i >= 0:
		j.Entries[i] = *prev
	}
}

// lastID is the highest numeric message id the journal knows for a target:
// where a scan of the platform's history starts.
func (j *Journal) lastID(target string) int64 {
	var last int64
	for _, e := range j.Entries {
		if e.Target != target {
			continue
		}
		for _, s := range append([]string{e.ID}, e.Sent...) {
			if n, err := strconv.ParseInt(s, 10, 64); err == nil && n > last {
				last = n
			}
		}
	}
	return last
}

var reSpace = regexp.MustCompile(`\s+`)

// normText is the text as a platform shows it: no markup, no URLs behind
// link text, whitespace collapsed. The journal hashes it and a platform's
// history is compared against it; both sides go through it, or a stray
// newline would read as a different post.
func normText(plain string) string {
	return strings.TrimSpace(reSpace.ReplaceAllString(plain, " "))
}

func textHash(plain string) string {
	sum := sha256.Sum256([]byte(normText(plain)))
	return hex.EncodeToString(sum[:])
}
