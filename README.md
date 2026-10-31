# stash-bot

## Start command

`/start` cancels pending input and sends a fresh main menu below the command,
including when a previous control message is still available. Menu callbacks
continue to edit the existing controls. Control message requests have a
15-second deadline; failed sends and start handling are logged.

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

## Bounded media transfers

Telegram uploads, Stash downloads, outgoing multipart attachments and image-search
requests use disk-backed temporary files and streaming readers. The copy buffer
is 32 KiB; media is not accumulated in Go byte slices. A file is limited to
512 MiB, including when a size is missing or understated. Known oversized messages
are rejected before `getFile` can download them into the local Bot API. Image
search has a separate 16 MiB limit.

One shared temporary-file budget is 1 GiB per bot process, including active
attachments and prefetch. The disk cache uses LRU eviction with a 128 MiB and
128-entry ceiling; cache keys include the immutable Stash storage path. Evicted
files remain charged and readable until their final active reader closes. Files
larger than the cache ceiling are not prefetched. Prefetch skips busy download
slots instead of queueing behind foreground transfers.

At most two download/upload pipelines and two Telegram media sends run concurrently.
Albums count as one send and prepare their files through the download limiter.
Budget exhaustion fails promptly rather than waiting while holding album files;
an album is not partially sent after a preparation error. Stale Telegram ID
recovery releases the first attempt's readers before rebuilding the multipart
request. HTTP errors/timeouts do not trigger extra sends.

Temporary files are mode 0600 and unlinked immediately after creation. They stay
open through the transfer/cache lease; closing the final reader or terminating
the process releases the data, including after SIGKILL. The default directory is
the container's `/tmp` on its writable disk layer. Do not configure it as tmpfs:
that would charge large files to the same 512 MiB memory budget. Ensure at least
1 GiB disk headroom for temporary media. No persistent application volumes or
Telegram API files are changed by this cache.

`make format` applies Go formatting. `make check` validates formatting, runs vet,
all Go tests with the race detector, Compose validation and `media-memory-test`.
The latter cross-compiles a test binary for the Docker daemon architecture and
runs it in a disposable Linux container with 512 MiB RAM, no swap and no external
network. Synthetic HTTP services exercise a full 512 MiB incoming/outgoing file
and a ten-file album (16 MiB each) with prefetch, then verify temporary-byte
cleanup. Ordinary tests also cover LRU eviction, shared concurrent byte admission,
active-reader lifetime, cancellation, oversized input, bounded transfers and
cleanup/recovery after failed sends. Tests never send real Telegram messages or
read production files. `make test` runs the ordinary Go tests without Docker.

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
