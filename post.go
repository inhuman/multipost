package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// Post is one publication: a directory with post.md (front matter + full
// text), optional teaser.md and media files.
type Post struct {
	Dir     string   `yaml:"-"`
	Title   string   `yaml:"title"`
	Mode    string   `yaml:"mode"`     // full | teaser
	PaidURL string   `yaml:"paid_url"` // appended to the teaser on public targets
	Targets []string `yaml:"targets"`
	Media   []string `yaml:"media"`

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

	if len(p.Media) == 0 {
		entries, err := os.ReadDir(dir)
		if err != nil {
			return nil, err
		}
		for _, e := range entries {
			if !e.IsDir() && kindOf(e.Name()) != "" {
				p.Media = append(p.Media, e.Name())
			}
		}
		slices.Sort(p.Media)
	}
	for i, m := range p.Media {
		if kindOf(m) == "" {
			return nil, fmt.Errorf("media %q: unknown type", m)
		}
		p.Media[i] = filepath.Join(dir, m)
		if _, err := os.Stat(p.Media[i]); err != nil {
			return nil, err
		}
	}
	return p, nil
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
