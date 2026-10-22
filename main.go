// multipost publishes one post (a directory with post.md and media) to
// Telegram, MAX, VK, Boosty and Sponsr in one run.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/inhuman/config"
	"go.uber.org/zap"
)

type Config struct {
	TGBotToken  string `env:"TG_BOT_TOKEN"`
	TGChat      string `env:"TG_CHAT"` // @channel or -100…
	MaxBotToken string `env:"MAX_BOT_TOKEN"`
	MaxChatID   int64  `env:"MAX_CHAT_ID"`
	VKURL       string `env:"VK_URL"`     // community page, https://vk.com/club…
	BoostyURL   string `env:"BOOSTY_URL"` // https://boosty.to/<blog>
	SponsrURL   string `env:"SPONSR_URL"` // https://sponsr.ru/<project>/
	Headless    bool   `env:"BROWSER_HEADLESS" env-default:"false"`
}

var allTargets = []string{"tg", "max", "vk", "boosty", "sponsr"}

const usage = `multipost — один пост на все площадки

  multipost [-only tg,max] [-dry-run] [-force] <каталог поста>
  multipost login vk|boosty|sponsr   войти в браузерном профиле (один раз)
  multipost max-chats                 чаты бота MAX, для MAX_CHAT_ID

Токены и адреса — в ~/.config/multipost/.env (см. README).
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

func loadConfig() (Config, error) {
	var cfg Config
	dir, err := os.UserConfigDir()
	if err != nil {
		return cfg, err
	}
	err = config.Load(&cfg, config.WithSearchInDotEnvFile(),
		config.WithFilePath(filepath.Join(dir, "multipost", ".env")))
	return cfg, err
}

func run(ctx context.Context, log *zap.Logger, args []string) error {
	fs := flag.NewFlagSet("multipost", flag.ContinueOnError)
	fs.Usage = func() { fmt.Fprint(os.Stderr, usage) }
	only := fs.String("only", "", "comma-separated targets, overrides the post's list")
	dryRun := fs.Bool("dry-run", false, "show what goes where, publish nothing")
	force := fs.Bool("force", false, "publish again where the post already went")
	if err := fs.Parse(args); err != nil {
		return err
	}
	cfg, err := loadConfig()
	if err != nil {
		return err
	}

	switch fs.Arg(0) {
	case "":
		fs.Usage()
		return fmt.Errorf("no post directory")
	case "login":
		url := map[string]string{"vk": cfg.VKURL, "boosty": cfg.BoostyURL, "sponsr": cfg.SponsrURL}[fs.Arg(1)]
		if url == "" {
			return fmt.Errorf("login %q: unknown site or its URL is not set in .env", fs.Arg(1))
		}
		return login(ctx, url)
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
	if len(names) == 0 {
		names = allTargets
	}
	targets := make([]Target, 0, len(names))
	for _, n := range names {
		t, err := buildTarget(cfg, strings.TrimSpace(n))
		if err != nil {
			return err
		}
		targets = append(targets, t)
	}

	if *dryRun {
		return printPlan(ctx, post, targets)
	}

	state, err := loadState(post.Dir)
	if err != nil {
		return err
	}
	var failed []string
	for _, t := range targets {
		l := log.With(zap.String("target", t.Name()))
		if prev, ok := state[t.Name()]; ok && !*force {
			l.Info("already posted, skip (-force to repeat)", zap.String("url", prev.URL), zap.Time("at", prev.At))
			continue
		}
		start := time.Now()
		res, err := t.Publish(ctx, post)
		if err != nil {
			l.Error("publish failed", zap.Error(err))
			failed = append(failed, t.Name())
			continue
		}
		state[t.Name()] = Posted{Result: res, At: time.Now()}
		if err := state.save(post.Dir); err != nil {
			return err
		}
		l.Info("posted", zap.String("id", res.ID), zap.String("url", res.URL), zap.Duration("took", time.Since(start)))
	}
	if len(failed) > 0 {
		return fmt.Errorf("failed: %s — fix and rerun, posted targets are skipped", strings.Join(failed, ","))
	}
	return nil
}

func buildTarget(cfg Config, name string) (Target, error) {
	missing := func(vars ...string) error {
		return fmt.Errorf("target %s: set %s in ~/.config/multipost/.env", name, strings.Join(vars, ", "))
	}
	switch name {
	case "tg":
		if cfg.TGBotToken == "" || cfg.TGChat == "" {
			return nil, missing("TG_BOT_TOKEN", "TG_CHAT")
		}
		return &Telegram{Token: cfg.TGBotToken, Chat: cfg.TGChat}, nil
	case "max":
		if cfg.MaxBotToken == "" || cfg.MaxChatID == 0 {
			return nil, missing("MAX_BOT_TOKEN", "MAX_CHAT_ID")
		}
		return &Max{Token: cfg.MaxBotToken, ChatID: cfg.MaxChatID}, nil
	case "vk", "boosty", "sponsr":
		url := map[string]string{"vk": cfg.VKURL, "boosty": cfg.BoostyURL, "sponsr": cfg.SponsrURL}[name]
		if url == "" {
			return nil, missing(strings.ToUpper(name) + "_URL")
		}
		route := map[string]routeWant{"vk": viaDirect, "boosty": viaAny, "sponsr": viaAny}[name]
		return &BrowserTarget{name: name, startURL: url, headless: cfg.Headless, route: route, flow: browserFlows[name]}, nil
	}
	return nil, fmt.Errorf("unknown target %q, want one of %v", name, allTargets)
}

func printPlan(ctx context.Context, p *Post, targets []Target) error {
	fmt.Printf("пост: %s (режим %s), медиа: %d\n", p.Dir, p.Mode, len(p.Media))
	for _, m := range p.Media {
		fmt.Println("  ", filepath.Base(m), kindOf(m))
	}
	for _, t := range targets {
		text := p.PublicText()
		if slices.Contains([]string{"boosty", "sponsr"}, t.Name()) {
			text = p.Body
		}
		fmt.Printf("\n== %s: %d знаков, маршрут: %s\n%s\n", t.Name(), textLen(text), routeStatus(ctx, t), text)
	}
	return nil
}

func routeStatus(ctx context.Context, t Target) string {
	var err error
	switch t := t.(type) {
	case *Telegram:
		err = checkRoute(ctx, tgAPI, viaVPN)
	case *Max:
		err = checkRoute(ctx, maxAPI, viaDirect)
	case *BrowserTarget:
		err = checkRoute(ctx, t.startURL, t.route)
	}
	if err != nil {
		return "⛔ " + err.Error()
	}
	return "ок"
}
