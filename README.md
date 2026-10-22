# multipost

Один пост → Telegram, MAX, VK, Boosty, Sponsr. Локальная утилита.

- **Telegram, MAX** — через Bot API.
- **VK, Boosty, Sponsr** — через браузер (Playwright) с постоянным профилем.
  У Boosty и Sponsr нет API публикации. VK даёт права `wall`/`photos`
  токену пользователя только по запросу в поддержку.

## Пост

Каталог с `post.md`, при `mode: teaser` ещё и с `teaser.md`. Медиа — всё
из подкаталога `media/` (картинки и видео, по имени); что лежит рядом
с `post.md`, не прикладывается. Front matter не обязателен.

```markdown
---
title: Неделя 12
mode: teaser            # full — всем полный текст; teaser — тизер + paid_url
paid_url: https://boosty.to/…/posts/…
targets: [tg, max, vk, boosty, sponsr]   # по умолчанию все настроенные
---
Полный текст. **жирный**, *курсив*, `код`, [ссылка](https://…).
```

Boosty и Sponsr получают полный текст, остальные — публичный.

## Запуск

Оба пути полные: утилита общая, у каждого проекта свои `.env` и посты.

```
go install github.com/inhuman/multipost@latest
multipost -env ~/proj/.multipost/.env -dry-run ~/proj/posts/2026-10-23-slug
multipost -env ~/proj/.multipost/.env ~/proj/posts/2026-10-23-slug
multipost -env … -only tg …    # одна площадка
```

Удобнее завести в проекте обёртку, которая подставляет оба пути
по имени поста.

### Журнал: без двойных постов, без человека

`journal.json` рядом с `.env` помнит, куда что ушло — по папке поста и по
хешу текста (тот же текст из другой папки тоже считается). Перед вызовом
площадки запись пишется как `pending`, после ответа — `posted`. Если прогон
упал или ответ потерялся, `pending` остаётся, и следующий запуск сам смотрит
историю площадки: нашёл пост — записывает его и предупреждает, не нашёл —
постит. Полупост (в Telegram альбом ушёл, текст нет) сразу удаляется; что
не удалилось, удаляется перед повтором. `-force` публикует заново.

Telegram-бот не умеет читать свой канал, поэтому для проверки нужен
служебный приватный чат, где бот админ (`TG_LOG_CHAT`): пост канала
пересылается туда, текст сравнивается, пересылка удаляется. Без него
неизвестный исход в Telegram — пропуск с ошибкой выхода.

## Маршрут

Telegram доступен только через VPN, MAX и VK — только мимо него
(их IP закрепляет `rus-routes.sh`). Перед каждой отправкой утилита
проверяет `ip route get` каждого хоста, включая выданные адреса загрузки,
и при неверном маршруте отказывается постить.

## Настройка: `.env` проекта

Образец — `env.example`. Файл с токенами держать под `.gitignore`.


| переменная | что |
|---|---|
| `TG_BOT_TOKEN` | токен от @BotFather; бот — админ канала с правом постить |
| `TG_CHAT` | `@channel` или `-100…` |
| `TG_LOG_CHAT` | служебный приватный чат (`-100…`), бот — админ: проверка канала после сбоя |
| `MAX_BOT_TOKEN` | токен бота MAX (dev.max.ru); бот — админ канала |
| `MAX_CHAT_ID` | id канала: `multipost max-chats` |
| `VK_URL` | страница сообщества, `https://vk.com/club…` |
| `BOOSTY_URL` | `https://boosty.to/<блог>` |
| `SPONSR_URL` | `https://sponsr.ru/<проект>/` |
| `BROWSER_PROFILE` | профиль браузера; по умолчанию `browser/` рядом с `.env` |
| `BROWSER_HEADLESS` | `true` — браузер без окна (по умолчанию окно видно) |

Браузерным площадкам нужен вход один раз: `multipost -env … login vk`
(аналогично `boosty`, `sponsr`). Профиль с сессиями лежит рядом с `.env`,
поэтому под `.gitignore` должен быть весь каталог.

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

## Спецификация и тесты

Поведение задано моделями на Lean в `spec/lean/MultipostSpec`: теоремы
проверяет ядро Lean, а Go-тесты ([evident](https://github.com/inhuman/evident))
гоняют живой код по каждой строке каждой модели.

| требование | что доказано |
|---|---|
| `tg.layout` | текст уходит ровно один раз; медиа одним вызовом и первыми; при нарушении лимита — ни одного вызова |
| `route.guard` | трафик идёт только нужной стороной VPN, проверяется каждый адрес хоста |
| `targets.select` | названные площадки — все или ни одной; по умолчанию все готовые; опечатка в настройке роняет запуск |
| `state.no-double-post` | пост не попадает на площадку дважды даже после падения: журнал пишется до вызова, неизвестный исход решает история площадки, не повтор вслепую |
| `post.text` | платные площадки получают весь пост, бесплатные никогда не получают платный текст |

```
go test ./...                                      # вердикт по каждому требованию
cd spec/lean && lake build && lake env lean Export.lean   # после правки модели
cd spec && go run github.com/inhuman/evident/cmd/evident lock evident.lock *.json
go run github.com/inhuman/evident/cmd/evident verify spec/evident.lock spec/*.json
```

Правка модели — это правка требования: экспорт и lock перегенерируются
вместе с ней, иначе тесты не соберут спецификацию.
