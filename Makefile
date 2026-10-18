DOCKER_COMPOSE_BASE = docker compose --project-directory "$(CURDIR)"
include makefiles/compose.mk
DOCKER_COMPOSE = docker compose --project-directory "$(CURDIR)" $(COMPOSE_FLAGS)
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

.PHONY: fmt
fmt:
	gofmt -w $$(git ls-files '*.go')

.PHONY: fmt-check
fmt-check:
	@files="$$(gofmt -l .)"; status=$$?; test $$status -eq 0 || exit $$status; test -z "$$files" || { printf '%s\n' "$$files"; exit 1; }

.PHONY: format
format: fmt-check

.PHONY: test
test:
	go test ./...

.PHONY: vet
vet:
	go vet ./...

.PHONY: lint
lint: vet

.PHONY: config-check
config-check:
	@BOT_TOKEN=dummy-token ROOT_ID=1 STASH_URL=http://stash:8080 $(DOCKER_COMPOSE) config --quiet

.PHONY: check
check: fmt-check lint test config-check

.PHONY: clean
clean:
	rm -f $(BINARY)

# Read-only validation of every tracked Compose combination, without runtime secrets.
.PHONY: compose-layout-check
compose-layout-check:
	$(DOCKER_COMPOSE_BASE) --env-file /dev/null -f compose/docker-compose.yml --profile '*' config --no-interpolate --no-env-resolution --quiet

check: compose-layout-check
