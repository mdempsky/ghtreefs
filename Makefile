CONTAINER ?= container
GO_IMAGE ?= golang:1.26.1

.PHONY: test

test:
	$(CONTAINER) run --rm \
		-e GOMODCACHE=/tmp/gomodcache \
		-v "$(CURDIR):/src" \
		-w /src \
		$(GO_IMAGE) \
		go test ./...
