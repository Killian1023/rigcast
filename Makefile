GO ?= go
NODE ?= node
VERSION ?= dev
DIST ?= dist
TARGETS := linux-amd64 linux-arm64 darwin-amd64 darwin-arm64 windows-amd64 windows-arm64
GOFLAGS_RELEASE := -trimpath -ldflags "-s -w -X main.version=$(VERSION)"

.PHONY: build test vet release npm-pack $(TARGETS)

build:
	mkdir -p "$(DIST)"
	CGO_ENABLED=0 $(GO) build $(GOFLAGS_RELEASE) -o "$(DIST)/rigcast$(if $(filter windows,$(shell $(GO) env GOOS)),.exe)" ./cmd/rigcast

test:
	CGO_ENABLED=0 $(GO) test ./...

vet:
	$(GO) vet ./...

release: $(TARGETS)

npm-pack:
	$(MAKE) release DIST="$(DIST)/npm-binaries" VERSION="$$($(NODE) -p 'require("./npm/package.json").version')"
	GO="$(GO)" $(NODE) scripts/pack-npm.mjs "$(DIST)"

$(TARGETS):
	mkdir -p "$(DIST)"
	CGO_ENABLED=0 GOOS=$(word 1,$(subst -, ,$@)) GOARCH=$(word 2,$(subst -, ,$@)) $(GO) build $(GOFLAGS_RELEASE) -o "$(DIST)/rigcast-$@$(if $(filter windows-%,$@),.exe)" ./cmd/rigcast
