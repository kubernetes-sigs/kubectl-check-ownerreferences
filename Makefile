.PHONY: default install build clean test fmt vet lint

all: build test fmt vet lint

default: build

build:
	go build -o ./bin/kubectl-check-ownerreferences $(shell ./build/print-ldflags.sh) ./

build-release:
	# check go version (this should match the go version used by the referenced k8s.io/client-go library version)
	@OUTPUT=`go version`; \
	case "$$OUTPUT" in \
	*"go1.16"*);; \
	*) \
		echo "Unexpected go version: $$OUTPUT"; \
		exit 1; \
	;; \
	esac

	rm -fr ./bin
	mkdir -p ./bin/darwin/amd64
	mkdir -p ./bin/linux/amd64
	GOOS=darwin GOARCH=amd64 go build -trimpath -o ./bin/darwin/amd64/kubectl-check-ownerreferences $(shell ./build/print-ldflags.sh) ./
	GOOS=darwin GOARCH=arm64 go build -trimpath -o ./bin/darwin/arm64/kubectl-check-ownerreferences $(shell ./build/print-ldflags.sh) ./
	GOOS=linux  GOARCH=amd64 go build -trimpath -o ./bin/linux/amd64/kubectl-check-ownerreferences  $(shell ./build/print-ldflags.sh) ./
	tar -cvzf ./bin/kubectl-check-ownerreferences-darwin-amd64.tar.gz LICENSE -C ./bin/darwin/amd64 kubectl-check-ownerreferences
	tar -cvzf ./bin/kubectl-check-ownerreferences-darwin-arm64.tar.gz LICENSE -C ./bin/darwin/arm64 kubectl-check-ownerreferences
	tar -cvzf ./bin/kubectl-check-ownerreferences-linux-amd64.tar.gz  LICENSE -C ./bin/linux/amd64  kubectl-check-ownerreferences

install:
	go install $(shell ./build/print-ldflags.sh) ./

clean:
	rm -fr bin

test:
	go test -v ./...

# Capture output and force failure when there is non-empty output
fmt:
	@echo gofmt -l ./
	@OUTPUT=`gofmt -l ./ 2>&1`; \
	if [ "$$OUTPUT" ]; then \
		echo "gofmt must be run on the following files:"; \
		echo "$$OUTPUT"; \
		exit 1; \
	fi

vet:
	go vet ./

# https://github.com/golang/lint
# go get github.com/golang/lint/golint
# Capture output and force failure when there is non-empty output
lint:
	@echo golint ./...
	@OUTPUT=`golint ./... 2>&1`; \
	if [ "$$OUTPUT" ]; then \
		echo "golint errors:"; \
		echo "$$OUTPUT"; \
		exit 1; \
	fi
