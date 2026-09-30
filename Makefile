.PHONY: test check build compose-check linux-build

DOCKYARD_VERSION := $(shell cat VERSION)
BUILD_INFO := github.com/ziqx/ziqx-dockyard-service/internal/buildinfo
VERSION_FLAGS := -X $(BUILD_INFO).Version=$(DOCKYARD_VERSION) -X $(BUILD_INFO).Commit=local

test:
	go test -race ./...

check:
	go vet ./...

build:
	mkdir -p bin
	CGO_ENABLED=1 go build -trimpath -ldflags='$(VERSION_FLAGS)' -o bin/dockyard ./cmd/dockyard
	CGO_ENABLED=1 go build -trimpath -ldflags='$(VERSION_FLAGS)' -o bin/dockyardctl ./cmd/dockyardctl

compose-check:
	DOCKYARD_COMPOSE_TEST=1 go test ./internal/runtime -run 'Test(ComposeLiteralEnvironmentRoundTrip|SubmittedMultiServiceComposeRoundTrip)' -v

linux-build:
	docker build --pull --build-arg DOCKYARD_VERSION=$(DOCKYARD_VERSION) --build-arg DOCKYARD_COMMIT=local --file Dockerfile.build --output bin/linux .
