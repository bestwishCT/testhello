NOW=`date "+%Y-%m-%d %H:%M:%S"`
GOHOSTOS:=$(shell go env GOHOSTOS)
GOPATH:=$(shell go env GOPATH)
VERSION=$(shell git describe --tags --always)
PACKAGES=$(shell go list ./... | grep -v /vendor/)

ifeq ($(GOHOSTOS), windows)
	#the `find.exe` is different from `find` in bash/shell.
	#to see https://docs.microsoft.com/en-us/windows-server/administration/windows-commands/find.
	#changed to use git-bash.exe to run find cli or other cli friendly, caused of every developer has a Git.
	#Git_Bash= $(subst cmd\,bin\bash.exe,$(dir $(shell where git)))
	Git_Bash=$(shell which bash)
	PROTO_FILES=$(shell $(Git_Bash) -c "find . -name *.proto")
	TEST_DIRS=$(shell $(Git_Bash) -c "find . -name '*_test.go' | awk -F '/[^/]*$$' '{print $$1}' | sort -u")
	GO_FILES=$(shell $(Git_Bash) -c "find . -name '*.go' -type f -not -path './vendor/*'")
else
	PROTO_FILES=$(shell find . -name *.proto)
	TEST_DIRS=$(shell find . -name '*_test.go' | awk -F '/[^/]*$$' '{print $$1}' | sort -u)
    GO_FILES=$(shell find . -name '*.go' -type f -not -path './vendor/*')
endif

.PHONY: init
# init env
init:
	go install google.golang.org/protobuf/cmd/protoc-gen-go@latest
	go install google.golang.org/grpc/cmd/protoc-gen-go-grpc@latest
	go install github.com/google/wire/cmd/wire@latest

.PHONY: wire
# wire
wire:
	cd cmd/ && wire gen ./...

.PHONY: generate
# generate
generate:
	go mod tidy
	go get github.com/google/wire/cmd/wire@latest
	go generate ./...

.PHONY: proto
# generate proto
proto:
	protoc --proto_path=./pkg/proto \
 	       --go_out=paths=source_relative:./pkg/proto \
 	       --go-grpc_out=paths=source_relative:./pkg/proto \
	       $(PROTO_FILES)

.PHONY: test
# test
test:
	@go clean -testcache && go test -cover -v ${TEST_DIRS} -gcflags="all=-N -l"


.PHONY: fmt
# 格式化代码
fmt:
	@echo "${NOW} Starting to format ..."
	@gofumpt -l -w ./
	@echo "${NOW} Format done!"

.PHONY: fmt-check
# 格式化检查
fmt-check:
	@echo "${NOW} Starting to format check ..."
	@UNFORMATTED=$$(gofumpt -l ./); \
	if [ -z "$$UNFORMATTED" ]; then \
		echo "Format check done!"; \
	else \
		echo "Below files need to format:"; \
		echo "$$UNFORMATTED"; \
		exit 1; \
	fi;


.PHONY: vet
# vet
vet:
	@go vet --unsafeptr=false $(PACKAGES)

.PHONY: build
# build
build:
	mkdir -p bin/ && go build -ldflags "-s -w -X main.Version=$(VERSION)" -o ./bin/rpims rpims/cmd/rpims

.PHONY: x86buildMaster
# x86buildMaster
x86buildMaster:
	mkdir -p bin/ && CGO_ENABLED=0 GOOS=linux GOARCH=amd64  go build -ldflags "-s -w -X main.Version=$(VERSION)" -o ./bin/master shiledp2p/cmd/master
x86buildAgent:
	mkdir -p bin/ && CGO_ENABLED=0 GOOS=linux GOARCH=amd64  go build -ldflags "-s -w -X main.Version=$(VERSION)" -o ./bin/agent shiledp2p/cmd/agent
x86buildClient:
	mkdir -p bin/ && CGO_ENABLED=0 GOOS=linux GOARCH=amd64  go build -ldflags "-s -w -X main.Version=$(VERSION)" -o ./bin/client shiledp2p/cmd/client
.PHONY: all
# generate all
all:
	make init;
	make generate;
	#make proto;
	make vet;
	#make test;
	make build

# show help
help:
	@echo ''
	@echo 'Usage:'
	@echo ' make [target]'
	@echo ''
	@echo 'Targets:'
	@awk '/^[a-zA-Z\-\_0-9]+:/ { \
	helpMessage = match(lastLine, /^# (.*)/); \
		if (helpMessage) { \
			helpCommand = substr($$1, 0, index($$1, ":")); \
			helpMessage = substr(lastLine, RSTART + 2, RLENGTH); \
			printf "\033[36m%-22s\033[0m %s\n", helpCommand,helpMessage; \
		} \
	} \
	{ lastLine = $$0 }' $(MAKEFILE_LIST)

.DEFAULT_GOAL := help