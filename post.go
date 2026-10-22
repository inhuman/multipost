package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Post is one publication: a directory with post.md (optional front matter
// + full text), optional teaser.md, and media/ — every image and video in
// it is attached, in name order. Media lying next to post.md is ignored:
// post folders also keep drafts, figures' sources and scripts.
type Post struct {
	Dir     string   `yaml:"-"`
	Title   string   `yaml:"title"`
	Mode    string   `yaml:"mode"`     // full | teaser
	PaidURL string   `yaml:"paid_url"` // appended to the teaser on public targets
	Targets []string `yaml:"targets"`
	Media   []string `yaml:"-"`

	Body   string `yaml:"-"`
	Teaser string `yaml:"-"`
}

const (
	modeFull   = "full"
	modeTeaser = "teaser"
)

var mediaExts = map[string]mediaKind{
	".jpg": mediaImage, ".jpeg": mediaImage, ".png": mediaImage, ".webp": mediaImage,
	".mp4": mediaVideo, ".mov": mediaVideo, ".webm": mediaVideo,
}

type mediaKind string

const (
	mediaImage mediaKind = "image"
	mediaVideo mediaKind = "video"
)

func kindOf(path string) mediaKind {
	return mediaExts[strings.ToLower(filepath.Ext(path))]
}

func loadPost(dir string) (*Post, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "post.md"))
	if err != nil {
		return nil, err
	}
	p := &Post{Dir: dir, Mode: modeFull}
	body := raw
	if rest, ok := bytes.CutPrefix(raw, []byte("---\n")); ok {
		front, after, found := bytes.Cut(rest, []byte("\n---\n"))
		if !found {
			return nil, errors.New("post.md: front matter not closed with ---")
		}
		if err := yaml.Unmarshal(front, p); err != nil {
			return nil, fmt.Errorf("post.md front matter: %w", err)
		}
		body = after
	}
	p.Body = strings.TrimSpace(string(body))
	if p.Body == "" {
		return nil, errors.New("post.md: empty body")
	}

	switch p.Mode {
	case modeFull:
	case modeTeaser:
		t, err := os.ReadFile(filepath.Join(dir, "teaser.md"))
		if err != nil {
			return nil, fmt.Errorf("mode teaser needs teaser.md: %w", err)
		}
		p.Teaser = strings.TrimSpace(string(t))
	default:
		return nil, fmt.Errorf("mode %q: want full or teaser", p.Mode)
	}

	mediaDir := filepath.Join(dir, "media")
	entries, err := os.ReadDir(mediaDir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if kindOf(e.Name()) == "" {
			return nil, fmt.Errorf("media/%s: not an image or video (%v)", e.Name(), slices.Sorted(maps.Keys(mediaExts)))
		}
		p.Media = append(p.Media, filepath.Join(mediaDir, e.Name()))
	}
	slices.Sort(p.Media)
	return p, nil
}

// TextFor is what a target gets: paid platforms always the full post, free
// ones the full post or, in teaser mode, the teaser plus the paid link.
func (p *Post) TextFor(paid bool) string {
	if paid {
		return p.Body
	}
	return p.PublicText()
}

// PublicText is what free channels get: the full post, or the teaser plus
// the paid link.
func (p *Post) PublicText() string {
	if p.Mode == modeFull {
		return p.Body
	}
	if p.PaidURL == "" {
		return p.Teaser
	}
	return p.Teaser + "\n\n" + p.PaidURL
}
