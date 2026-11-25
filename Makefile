.PHONY: build test vet check dist

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
