# Copyright (c) Tetrate, Inc 2022 All Rights Reserved.

NAME := swimlane-headers-propagate13
WASM := $(NAME).wasm

OUT := $(WASM)
TAG ?= 1.19
HUB ?= gcr.io/images-374305

LINTER := github.com/golangci/golangci-lint/cmd/golangci-lint@v1.50.0

all: clean test lint docker-build docker-push

compile: $(OUT)

$(OUT):
	tinygo build -o $(OUT) -scheduler=none -target=wasi ./...

test:
	go test -v -tags=proxytest ./...

lint:
	go run $(LINTER) run --verbose --build-tags proxytest

clean:
	rm -f *.wasm

docker-build: $(OUT)
	docker build --platform linux/amd64 --build-arg WASM_BINARY_PATH=$(OUT) -t $(HUB)/$(NAME):$(TAG) .

docker-push:
	docker push $(HUB)/$(NAME):$(TAG)
