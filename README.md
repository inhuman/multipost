# multipost

Один пост → Telegram, MAX, VK, Boosty, Sponsr. Локальная утилита.

- **Telegram, MAX** — через Bot API.
- **VK, Boosty, Sponsr** — через браузер (Playwright) с постоянным профилем.
  У Boosty и Sponsr нет API публикации. VK даёт права `wall`/`photos`
  токену пользователя только по запросу в поддержку.

## Пост

Каталог с `post.md`, а при `mode: teaser` ещё и с `teaser.md`. Медиа лежат
рядом, по умолчанию берутся все картинки и видео каталога по имени.

```markdown
---
title: Неделя 12
mode: teaser            # full — всем полный текст; teaser — тизер + paid_url
paid_url: https://boosty.to/…/posts/…
targets: [tg, max, vk, boosty, sponsr]   # по умолчанию все
media: [01.jpg, walk.mp4]                # по умолчанию все медиа каталога
---
Полный текст. **жирный**, *курсив*, `код`, [ссылка](https://…).
```

Boosty и Sponsr получают полный текст, остальные — публичный.

## Запуск

```
go build -o ~/bin/multipost .
multipost -dry-run post/      # что куда уйдёт и годен ли маршрут
multipost post/
multipost -only max post/     # одна площадка
```

Где пост уже вышел, записано в `post/.multipost.json`. Повторный запуск
пропускает эти площадки; `-force` публикует заново.

## Маршрут

Telegram доступен только через VPN, MAX и VK — только мимо него
(их IP закрепляет `rus-routes.sh`). Перед каждой отправкой утилита
проверяет `ip route get` каждого хоста, включая выданные адреса загрузки,
и при неверном маршруте отказывается постить.

## Настройка: `~/.config/multipost/.env`

| переменная | что |
|---|---|
| `TG_BOT_TOKEN` | токен от @BotFather; бот — админ канала с правом постить |
| `TG_CHAT` | `@channel` или `-100…` |
| `MAX_BOT_TOKEN` | токен бота MAX (dev.max.ru); бот — админ канала |
| `MAX_CHAT_ID` | id канала: `multipost max-chats` |
| `VK_URL` | страница сообщества, `https://vk.com/club…` |
| `BOOSTY_URL` | `https://boosty.to/<блог>` |
| `SPONSR_URL` | `https://sponsr.ru/<проект>/` |
| `BROWSER_HEADLESS` | `true` — браузер без окна (по умолчанию окно видно) |

Браузерным площадкам нужен вход один раз: `multipost login vk`
(аналогично `boosty`, `sponsr`). Профиль хранится в `~/.config/multipost/browser`.

## Драйвер Playwright

Официальный CDN драйвера (azureedge) отвечает 404, поэтому
`playwright.Install` скачать драйвер не может. Драйвер собирается
из npm-пакета той версии, которую требует `playwright-go` в go.mod:

```
D=~/.cache/ms-playwright-go/1.60.0
npm pack playwright-core@1.60.0 && tar xzf playwright-core-1.60.0.tgz
mkdir -p $D && mv package $D/ && ln -sf "$(command -v node)" $D/node
sed -i -e 's/pageError\.location\.url/pageError.location?.url || ""/g' \
  -e 's/pageError\.location\.lineNumber/pageError.location?.lineNumber || 0/g' \
  -e 's/pageError\.location\.columnNumber/pageError.location?.columnNumber || 0/g' \
  $D/package/lib/coreBundle.js     # тот же патч, что делает playwright-go
$D/node $D/package/cli.js install chromium
```
