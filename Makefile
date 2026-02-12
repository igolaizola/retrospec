#!/bin/bash

SHELL             = /bin/bash
PLATFORMS        ?= linux/amd64 darwin/amd64 darwin/arm64 windows/amd64
IMAGE_PREFIX     ?= igolaizola
REPO_NAME        ?= retrospec
COMMIT_SHORT     ?= $(shell git rev-parse --verify --short HEAD)
VERSION          ?= $(shell git describe --tags --exact-match 2>/dev/null || echo $(COMMIT_SHORT))

# Build the binary for the current platform
.PHONY: build
build:
	os=$$(go env GOOS); \
	arch=$$(go env GOARCH); \
	PLATFORMS="$$os/$$arch" make app-build

# Build binaries for target platforms
# Example: PLATFORMS=linux/amd64 make app-build
.PHONY: app-build
app-build:
	@mkdir -p ./bin
	@for platform in $(PLATFORMS) ; do \
		os=$$(echo $$platform | cut -f1 -d/); \
		arch=$$(echo $$platform | cut -f2 -d/); \
		arm=$$(echo $$platform | cut -f3 -d/); \
		arm=$${arm#v}; \
		ext=""; \
		if [ "$$os" == "windows" ]; then \
			ext=".exe"; \
		fi; \
		file=./bin/$(REPO_NAME)-$(COMMIT_SHORT)-$$(echo $$platform | tr / -)$$ext; \
		GOOS=$$os GOARCH=$$arch GOARM=$$arm CGO_ENABLED=0 \
		go build \
			-a -x -tags netgo,timetzdata -installsuffix cgo -installsuffix netgo \
			-o $$file \
			./cmd/$(REPO_NAME); \
		if [ $$? -ne 0 ]; then \
			exit 1; \
		fi; \
		chmod +x $$file; \
	done

# Build docker image
# Example: PLATFORMS=linux/amd64 make docker-build
.PHONY: docker-build
docker-build:
	@platforms=($(PLATFORMS)); \
	platform=$${platforms[0]}; \
	if [[ $${#platforms[@]} -ne 1 ]]; then \
		echo "Multi-arch build not supported"; \
		exit 1; \
	fi; \
	docker build --platform $$platform -t $(IMAGE_PREFIX)/$(REPO_NAME):$(COMMIT_SHORT) .

# Build docker images using buildx
# Example: PLATFORMS="linux/amd64 linux/arm64" make docker-buildx
.PHONY: docker-buildx
docker-buildx:
	@platforms=($(PLATFORMS)); \
	platform=$$(IFS=, ; echo "$${platforms[*]}"); \
	docker buildx build --platform $$platform -t $(IMAGE_PREFIX)/$(REPO_NAME):$(COMMIT_SHORT) .

# Run tests
.PHONY: test
test:
	go test -v ./...

# Clean binaries
.PHONY: clean
clean:
	rm -rf bin

