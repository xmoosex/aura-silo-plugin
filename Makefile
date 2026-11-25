.PHONY: build test vet check dist release

build:
	GOWORK=off go build -o plugin .

test:
	GOWORK=off go test -race ./...

vet:
	GOWORK=off go vet ./...

check: vet test
	test -z "$$(gofmt -l .)"

dist: check
	python3 scripts/package.py

# make release VERSION=1.0.1 raises the version, commits, tags and pushes; DRY_RUN=1 only shows what it would do
release:
	python3 scripts/release.py "$(VERSION)" "$(DRY_RUN)"
