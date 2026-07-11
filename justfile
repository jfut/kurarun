set dotenv-load := true
set export := true
set positional-arguments := true

NAME := "kurarun"

default:
    @just --list

#
# clean
#

clean:
    rm -rf dist
    mkdir -p dist

#
# update
#

update: update-aqua update-go

update-aqua:
    aqua update
    aqua update-checksum --deep --prune
    aqua i -l

update-go:
    go get -t -u ./...
    go mod tidy

#
# deps
#

deps:
    go mod download

#
# dev
#

fmt:
    gofmt -w .

lint:
    golangci-lint run ./...

test:
    go test ./...

help *ARGS:
    go run ./cmd/kurarun {{ARGS}} --help

#
# build
#

build: clean deps
    CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o dist/kurarun ./cmd/kurarun

#
# run
#

run *ARGS: build
    # Pass "$@" as-is via positional-arguments to avoid misparsing queries that include `>`.
    ./dist/kurarun "$@"

#
# release
#

snapshot: deps
    goreleaser release --skip=publish --clean --snapshot

release: deps
    goreleaser release --skip=publish --clean --skip=validate
