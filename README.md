# stash-bot

## Local Telegram API files

When `TELEGRAM_LOCAL_API_URL` is configured, the bot reads `getFile`'s absolute
paths from the external `telegram-bot-api-data` volume mounted read-only at
`/var/lib/telegram-bot-api`. The API creates owner-only directories and files
(0700/0600), so the bot must use the API process's numeric UID/GID. Compose defaults
to `TELEGRAM_FILE_UID=101` and `TELEGRAM_FILE_GID=101`, matching the deployed API.
Set these variables in private `.env` if that process uses different IDs; check
the owner numerically without printing token-bearing file paths. Group membership
alone does not grant access to these owner-only files. The bot remains non-root.

A direct-read `permission denied` followed by HTTP 404 means the shared file is
inaccessible to the bot; the local API's HTTP fallback cannot serve it. Correct the
numeric user and deploy through CI. Do not make the API session directories public.

## Sending stored media

Telegram `file_id` values are a cache; Stash's stored object remains the original.
An explicit HTTP 400 rejecting a cached identifier triggers one retry with bytes
from Stash and saves the new identifier. This applies to storage albums, single
photos/videos, GIFs/documents and the File button. An invalid member rejects an
entire album, so its retry rebuilds every multipart attachment with a distinct
name. A one-item page uses the corresponding single-media method.

Responses are matched to album slots in message order, including cached slots;
an incomplete or unexpected response is not guessed across files/media types.
Timeouts, access/rate-limit errors and server failures do not trigger this recovery,
because an uncertain send must not produce duplicate messages. The first open
after cache invalidation can take longer while originals are uploaded; subsequent
opens reuse the repaired cache. Recovery never replaces/deletes stored objects or
changes descriptions, tags, embeddings or `file_unique_id`.

## Структура Compose

Файлы Docker Compose находятся в `compose/`; основной файл — `compose/docker-compose.yml`.
Команды запускайте из корня проекта через Makefile: он задаёт `--project-directory`
на корень checkout. Build contexts, bind mounts, `env_file` и секреты остаются
относительными к этому корню; имена Compose project, сервисов, сетей и томов сохранены.
При явном вызове Docker используйте `docker compose --project-directory . -f compose/docker-compose.yml ...`.
Если в приватной `.env` или окружении задан `COMPOSE_FILE`, добавьте `compose/`
к прежним именам файлов, сохранив прежний набор overlays и порядок. Для плавного
перехода Make также распознаёт прежние короткие имена, если файлы уже перенесены.
Не меняйте project name и не удаляйте прежние тома для переноса файлов.
