// multipost publishes one post (a directory with post.md and media) to
// Telegram, MAX, VK, Boosty and Sponsr in one run.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/inhuman/config"
	"go.uber.org/zap"
)

type Config struct {
	TGBotToken  string `env:"TG_BOT_TOKEN"`
	TGChat      string `env:"TG_CHAT"`     // @channel or -100…
	TGLogChat   string `env:"TG_LOG_CHAT"` // private chat, bot admin: lets it read the channel
	MaxBotToken string `env:"MAX_BOT_TOKEN"`
	MaxChatID   string `env:"MAX_CHAT_ID"`     // empty in the template until max-chats tells it
	VKURL       string `env:"VK_URL"`          // community page, https://vk.com/club…
	BoostyURL   string `env:"BOOSTY_URL"`      // https://boosty.to/<blog>
	SponsrURL   string `env:"SPONSR_URL"`      // https://sponsr.ru/<project>/
	Profile     string `env:"BROWSER_PROFILE"` // default: browser/ next to the env file
	Headless    bool   `env:"BROWSER_HEADLESS" env-default:"false"`
}

var allTargets = []string{"tg", "max", "vk", "boosty", "sponsr"}

// paidTargets get the full post behind their paywall; the rest get the
// public text.
var paidTargets = map[string]bool{"boosty": true, "sponsr": true}

const usage = `multipost — один пост на все площадки

  multipost -env <путь/.env> [-only tg,max] [-dry-run] [-force] <каталог поста>
  multipost -env <путь/.env> login vk|boosty|sponsr   войти в браузерном профиле
  multipost -env <путь/.env> max-chats                чаты бота MAX, для MAX_CHAT_ID

Токены и адреса проекта — в его .env (см. README, env.example).
`

func main() {
	logCfg := zap.NewDevelopmentConfig()
	logCfg.DisableStacktrace = true
	logCfg.DisableCaller = true
	log, err := logCfg.Build()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	defer log.Sync()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	if err := run(ctx, log, os.Args[1:]); err != nil {
		log.Error("multipost failed", zap.Error(err))
		os.Exit(1)
	}
}

func loadConfig(envPath string) (Config, error) {
	var cfg Config
	if envPath == "" {
		return cfg, errors.New("-env is required: path to the project's .env")
	}
	// config.Load falls back to the process env when the file can't be
	// read; a typo in the path must fail, not post with empty tokens.
	if _, err := os.Stat(envPath); err != nil {
		return cfg, err
	}
	if err := config.Load(&cfg, config.WithSearchInDotEnvFile(), config.WithFilePath(envPath)); err != nil {
		return cfg, err
	}
	if cfg.Profile == "" {
		cfg.Profile = filepath.Join(filepath.Dir(envPath), "browser")
	}
	return cfg, nil
}

func run(ctx context.Context, log *zap.Logger, args []string) error {
	fs := flag.NewFlagSet("multipost", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	envPath := fs.String("env", "", "path to the project's .env with tokens and URLs")
	only := fs.String("only", "", "comma-separated targets, overrides the post's list")
	dryRun := fs.Bool("dry-run", false, "show what goes where, publish nothing")
	force := fs.Bool("force", false, "publish again where the post already went")
	if err := fs.Parse(args); errors.Is(err, flag.ErrHelp) {
		return nil
	} else if err != nil {
		return err
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return errors.New("no post directory")
	}
	cfg, err := loadConfig(*envPath)
	if err != nil {
		return err
	}

	switch fs.Arg(0) {
	case "login":
		url := map[string]string{"vk": cfg.VKURL, "boosty": cfg.BoostyURL, "sponsr": cfg.SponsrURL}[fs.Arg(1)]
		if url == "" {
			return fmt.Errorf("login %q: unknown site or its URL is not set in .env", fs.Arg(1))
		}
		return login(ctx, cfg.Profile, url)
	case "max-chats":
		chats, err := (&Max{Token: cfg.MaxBotToken}).Chats(ctx)
		if err != nil {
			return err
		}
		for _, c := range chats {
			fmt.Printf("%d\t%s\t%s\t%s\n", c.ChatID, c.Type, c.Title, c.Link)
		}
		return nil
	}

	post, err := loadPost(fs.Arg(0))
	if err != nil {
		return err
	}
	names := post.Targets
	if *only != "" {
		names = strings.Split(*only, ",")
	}
	targets, err := selectTargets(cfg, names, log)
	if err != nil {
		return err
	}

	if *dryRun {
		return printPlan(ctx, post, targets)
	}

	journal, err := loadJournal(filepath.Join(filepath.Dir(*envPath), "journal.json"))
	if err != nil {
		return err
	}
	failed, err := publishAll(ctx, log, post, targets, journal, *force)
	if err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed: %s — fix and rerun, posted targets are skipped", strings.Join(failed, ","))
	}
	return nil
}

// selectTargets builds the targets to post to. Named targets must all be
// set up, or nothing goes; by default every configured one goes and the
// rest are only reported. A value that is set but broken (MAX_CHAT_ID
// with a typo) always fails: skipping it would hide the typo.
func selectTargets(cfg Config, names []string, log *zap.Logger) ([]Target, error) {
	explicit := len(names) > 0
	if !explicit {
		names = allTargets
	}
	targets := make([]Target, 0, len(names))
	for _, n := range names {
		t, err := buildTarget(cfg, strings.TrimSpace(n))
		if err != nil && !explicit && errors.Is(err, errNotConfigured) {
			log.Info("not configured, skip", zap.String("target", n))
			continue
		}
		if err != nil {
			return nil, err
		}
		targets = append(targets, t)
	}
	if len(targets) == 0 {
		return nil, errors.New("no target configured in the .env")
	}
	return targets, nil
}

// publishAll posts to every target in order (spec: NoDoublePost). The
// journal entry goes to disk as pending BEFORE the platform is called, so a
// crash at any point leaves a trace; a pending entry from an earlier run is
// resolved by the platform's own history, never by posting blindly.
func publishAll(ctx context.Context, log *zap.Logger, post *Post, targets []Target, j *Journal, force bool) ([]string, error) {
	var failed []string
	for _, t := range targets {
		name := t.Name()
		l := log.With(zap.String("target", name))
		hash := textHash(toPlainNoURL(post.TextFor(paidTargets[name])))

		e := j.find(name, post.Dir, hash)
		if e != nil && !force {
			if e.Status == statusPosted {
				// A forced repost may have left a fragment next to the post.
				if err := removeHalfSent(ctx, j, t, post.Dir); err != nil {
					l.Warn("half-sent post can't be removed", zap.Error(err))
					failed = append(failed, name)
				}
				l.Info("already posted, skip (-force to repeat)", zap.String("url", e.URL), zap.String("dir", e.Dir))
				continue
			}
			hf, ok := t.(historyFinder)
			if !ok {
				l.Warn("previous attempt has unknown outcome and this platform can't be checked: skipped")
				failed = append(failed, name)
				continue
			}
			// The platform may hold the text as it was attempted, and only
			// messages after what was known then can be it.
			since := e.SinceID
			if since == 0 {
				since = j.lastID(name) // entries from before SinceID was kept
			}
			res, found, err := hf.FindPost(ctx, since, e.Hash)
			if err != nil {
				l.Warn("previous attempt has unknown outcome, history check failed: skipped", zap.Error(err))
				failed = append(failed, name)
				continue
			}
			if found {
				j.put(Entry{Target: name, Dir: post.Dir, Hash: e.Hash, Status: statusPosted, ID: res.ID, URL: res.URL})
				if err := j.save(); err != nil {
					return failed, err
				}
				l.Warn("previous attempt did post: found on the platform, recorded", zap.String("url", res.URL))
				continue
			}
		}

		// Not on the platform (or -force): the half-sent part goes before
		// the whole post is sent again. Found posts never get here, so a
		// post is never deleted on suspicion of being a fragment.
		if err := removeHalfSent(ctx, j, t, post.Dir); err != nil {
			l.Warn("half-sent post can't be removed: skipped", zap.Error(err))
			failed = append(failed, name)
			continue
		}

		var prev *Entry
		if cur := j.entryFor(name, post.Dir); cur != nil {
			prev = new(*cur)
		}
		j.put(Entry{Target: name, Dir: post.Dir, Hash: hash, Status: statusPending, SinceID: j.lastID(name)})
		if err := j.save(); err != nil {
			return failed, err
		}
		start := time.Now()
		res, err := t.Publish(ctx, post)
		switch classify(err) {
		case resultOK:
			j.put(Entry{Target: name, Dir: post.Dir, Hash: hash, Status: statusPosted, ID: res.ID, URL: res.URL})
			l.Info("posted", zap.String("id", res.ID), zap.String("url", res.URL), zap.Duration("took", time.Since(start)))
		case resultRejected:
			j.restore(name, post.Dir, prev)
			l.Error("publish rejected, nothing went out", zap.Error(err))
			failed = append(failed, name)
		case resultLost:
			l.Error("publish outcome unknown, the next run checks the platform", zap.Error(err))
			failed = append(failed, name)
		case resultHalfSent:
			hs, _ := errors.AsType[*halfSentError](err)
			j.put(Entry{Target: name, Dir: post.Dir, Hash: hash, Status: statusPending, Sent: hs.Sent, SinceID: j.lastID(name)})
			l.Error("post half sent, the next run removes the part and retries", zap.Error(err))
			failed = append(failed, name)
		}
		if err := j.save(); err != nil {
			return failed, err
		}
	}
	return failed, nil
}

// removeHalfSent deletes what a half-sent attempt left on the platform and
// forgets the ids once they are gone.
func removeHalfSent(ctx context.Context, j *Journal, t Target, dir string) error {
	cur := j.entryFor(t.Name(), dir)
	if cur == nil || len(cur.Sent) == 0 {
		return nil
	}
	c, ok := t.(cleaner)
	if !ok {
		return fmt.Errorf("platform can't delete, left in place: %v", cur.Sent)
	}
	if err := c.Cleanup(ctx, cur.Sent); err != nil {
		return fmt.Errorf("delete %v: %w", cur.Sent, err)
	}
	cur.Sent = nil
	return j.save()
}

var errNotConfigured = errors.New("not configured")

func buildTarget(cfg Config, name string) (Target, error) {
	missing := func(vars ...string) error {
		return fmt.Errorf("target %s: set %s in the .env: %w", name, strings.Join(vars, ", "), errNotConfigured)
	}
	switch name {
	case "tg":
		if cfg.TGBotToken == "" || cfg.TGChat == "" {
			return nil, missing("TG_BOT_TOKEN", "TG_CHAT")
		}
		return &Telegram{Token: cfg.TGBotToken, Chat: cfg.TGChat, LogChat: cfg.TGLogChat, API: tgAPI, Route: checkRoute}, nil
	case "max":
		if cfg.MaxBotToken == "" || cfg.MaxChatID == "" {
			return nil, missing("MAX_BOT_TOKEN", "MAX_CHAT_ID")
		}
		id, err := strconv.ParseInt(cfg.MaxChatID, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("MAX_CHAT_ID: %w", err)
		}
		return &Max{Token: cfg.MaxBotToken, ChatID: id, API: maxAPI, Route: checkRoute}, nil
	case "vk", "boosty", "sponsr":
		url := map[string]string{"vk": cfg.VKURL, "boosty": cfg.BoostyURL, "sponsr": cfg.SponsrURL}[name]
		if url == "" {
			return nil, missing(strings.ToUpper(name) + "_URL")
		}
		if browserFlows[name] == nil {
			return nil, fmt.Errorf("target %s: browser flow not recorded yet: %w", name, errNotConfigured)
		}
		route := map[string]routeWant{"vk": viaDirect, "boosty": viaAny, "sponsr": viaAny}[name]
		return &BrowserTarget{name: name, startURL: url, profile: cfg.Profile, headless: cfg.Headless, route: route, checker: checkRoute, flow: browserFlows[name]}, nil
	}
	return nil, fmt.Errorf("unknown target %q, want one of %v", name, allTargets)
}

func printPlan(ctx context.Context, p *Post, targets []Target) error {
	fmt.Printf("пост: %s (режим %s), медиа: %d\n", p.Dir, p.Mode, len(p.Media))
	for _, m := range p.Media {
		fmt.Println("  ", filepath.Base(m), kindOf(m))
	}
	for _, t := range targets {
		text := p.TextFor(paidTargets[t.Name()])
		fmt.Printf("\n== %s: %d знаков, маршрут: %s\n%s\n", t.Name(), textLen(text), routeStatus(ctx, t), text)
	}
	return nil
}

func routeStatus(ctx context.Context, t Target) string {
	var err error
	switch t := t.(type) {
	case *Telegram:
		err = t.Route(ctx, t.API, viaVPN)
	case *Max:
		err = t.Route(ctx, t.API, viaDirect)
	case *BrowserTarget:
		err = t.checker(ctx, t.startURL, t.route)
	}
	if err != nil {
		return "⛔ " + err.Error()
	}
	return "ок"
}
