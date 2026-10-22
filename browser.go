package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"

	"github.com/playwright-community/playwright-go"
)

// Platforms without a usable posting API (Boosty, Sponsr; VK gives wall
// rights to user tokens only on request) are driven through a real browser
// with a persistent profile: the user logs in once with `multipost login`,
// the session cookies stay in the profile. The profile lives next to the
// project's env file unless BROWSER_PROFILE says otherwise.

type browserSession struct {
	pw  *playwright.Playwright
	ctx playwright.BrowserContext
}

func openBrowser(profile string, headless bool) (*browserSession, error) {
	if err := playwright.Install(&playwright.RunOptions{Browsers: []string{"chromium"}, Verbose: false}); err != nil {
		return nil, fmt.Errorf("install playwright: %w", err)
	}
	pw, err := playwright.Run()
	if err != nil {
		return nil, err
	}
	bctx, err := pw.Chromium.LaunchPersistentContext(profile, playwright.BrowserTypeLaunchPersistentContextOptions{
		Headless: new(headless),
		Locale:   new("ru-RU"),
		Viewport: &playwright.Size{Width: 1400, Height: 1000},
	})
	if err != nil {
		return nil, errors.Join(err, pw.Stop())
	}
	return &browserSession{pw: pw, ctx: bctx}, nil
}

// Russian sites hang the "load" event on a stuck counter or ad script
// (sponsr.ru: 30 s timeout via browser, 0.5 s via curl).
var domContentLoaded = playwright.PageGotoOptions{WaitUntil: playwright.WaitUntilStateDomcontentloaded}

func (s *browserSession) Close() error {
	return errors.Join(s.ctx.Close(), s.pw.Stop())
}

// login opens the site in the profile and returns when the user closes
// the window: cookies are saved by the profile itself.
func login(ctx context.Context, profile, startURL string) error {
	s, err := openBrowser(profile, false)
	if err != nil {
		return err
	}
	defer s.Close()
	page, err := s.ctx.NewPage()
	if err != nil {
		return err
	}
	if _, err := page.Goto(startURL, domContentLoaded); err != nil {
		return err
	}
	fmt.Println("Войди в аккаунт в открытом окне и закрой его — сессия сохранится в профиле.")
	closed := make(chan struct{})
	done := sync.OnceFunc(func() { close(closed) })
	s.ctx.OnClose(func(playwright.BrowserContext) { done() })
	page.OnClose(func(playwright.Page) {
		if len(s.ctx.Pages()) == 0 {
			done()
		}
	})
	select {
	case <-closed:
	case <-ctx.Done():
	}
	return nil
}

// BrowserTarget publishes through a site flow in the logged-in profile.
type BrowserTarget struct {
	name     string
	startURL string
	profile  string
	headless bool
	route    routeWant
	checker  routeFunc
	flow     siteFlow
}

type siteFlow func(ctx context.Context, page playwright.Page, p *Post) (Result, error)

// browserFlows are recorded per site against the live, logged-in page.
var browserFlows = map[string]siteFlow{}

func (b *BrowserTarget) Name() string { return b.name }

func (b *BrowserTarget) Publish(ctx context.Context, p *Post) (Result, error) {
	if err := b.checker(ctx, b.startURL, b.route); err != nil {
		return Result{}, err
	}
	s, err := openBrowser(b.profile, b.headless)
	if err != nil {
		return Result{}, err
	}
	defer s.Close()
	page, err := s.ctx.NewPage()
	if err != nil {
		return Result{}, err
	}
	if _, err := page.Goto(b.startURL, domContentLoaded); err != nil {
		return Result{}, err
	}
	res, err := b.flow(ctx, page, p)
	if err != nil {
		shot := filepath.Join(p.Dir, ".multipost-"+b.name+"-fail.png")
		if _, serr := page.Screenshot(playwright.PageScreenshotOptions{Path: &shot, FullPage: new(true)}); serr == nil {
			err = fmt.Errorf("%w (screenshot %s)", err, shot)
		}
	}
	return res, err
}
