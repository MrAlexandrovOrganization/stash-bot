DOCKER_COMPOSE_BASE = docker compose --project-directory "$(CURDIR)"
include makefiles/compose.mk
DOCKER_COMPOSE = docker compose --project-directory "$(CURDIR)" $(COMPOSE_FLAGS)
include makefiles/versions.mk
BINARY = bot

.PHONY: build
build:
	go build -o $(BINARY) ./cmd/bot

# The bot stack owns ONLY the "stash-bot" service. It must never start the
# stash backend — that is deployed separately by the stash repo's own CI.
# Targeting "stash-bot" explicitly guarantees the backend is never launched here.
.PHONY: up
up:
	$(DOCKER_COMPOSE) up -d --build stash-bot

.PHONY: down
down:
	$(DOCKER_COMPOSE) down

.PHONY: logs
logs:
	$(DOCKER_COMPOSE) logs -f stash-bot

.PHONY: deploy
deploy:
	$(DOCKER_COMPOSE) up -d --build --no-cache stash-bot

.PHONY: restart
restart:
	$(DOCKER_COMPOSE) restart stash-bot

# One-shot repair of items whose stored bytes are Telegram error bodies
# instead of media (see cmd/repair-media). Preview first: DRY_RUN=1 make repair.
.PHONY: repair
repair:
	$(DOCKER_COMPOSE) run --rm --entrypoint /app/repair-media stash-bot

.PHONY: format
format:
	gofmt -w cmd internal

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: config-check
config-check:
	@BOT_TOKEN=dummy-token ROOT_ID=1 STASH_URL=http://stash:8080 $(DOCKER_COMPOSE) config --quiet

.PHONY: check
check: vet test-race config-check
	@files="$$(gofmt -l cmd internal)"; test -z "$$files" || { printf '%s\n' "$$files"; exit 1; }

.PHONY: test-race
test-race:
	go test -race ./...

.PHONY: clean
clean:
	rm -f $(BINARY)

# Read-only validation of every tracked Compose combination, without runtime secrets.
.PHONY: compose-layout-check
compose-layout-check:
	$(DOCKER_COMPOSE_BASE) --env-file /dev/null -f compose/docker-compose.yml --profile '*' config --no-interpolate --no-env-resolution --quiet

check: compose-layout-check

.PHONY: media-memory-test
media-memory-test:
	python3 scripts/check_media_memory.py
check: media-memory-test

.PHONY: versions image-build image-publish image-check image-deploy image-activate image-compose
versions:
	@$(foreach v,$(VERSION_VARS),printf '%s=%s\n' '$(v)' '$($(v))';)
export IMAGE_COMPOSE_FILE := compose/docker-compose.yml
export IMAGE_APP_SERVICES := stash-bot
image-build:
	python3 scripts/images.py build
image-publish:
	python3 scripts/images.py publish
image-check:
	python3 -B scripts/test_images.py
image-deploy:
	python3 scripts/images.py deploy
image-activate:
	$(IMAGE_COMPOSE) up -d --no-build --pull never --no-deps stash-bot
export IMAGE_ARGS
image-compose:
	python3 scripts/images.py compose
include makefiles/ci-deploy.mk
