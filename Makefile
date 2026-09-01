BINARY ?= kube-applier-gcp
IMAGE  ?= kube-applier-gcp
TAG    ?= latest

.PHONY: build test vet image clean kind-setup emulator run-local

build:
	go build -o $(BINARY) .

test:
	go test ./... -count=1

vet:
	go vet ./...

image:
	docker build -t $(IMAGE):$(TAG) .

kind-setup:
	./hack/setup-kind.sh

emulator:
	./hack/start-emulator.sh

run-local:
	./hack/run-local.sh

clean:
	rm -f $(BINARY)
