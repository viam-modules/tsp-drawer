BIN_OUTPUT_PATH = bin
GOOS ?= linux
GOARCH ?= amd64

build:
	GOOS=$(GOOS) GOARCH=$(GOARCH) go build -o $(BIN_OUTPUT_PATH)/tsp-drawer .

module: build
	rm -f $(BIN_OUTPUT_PATH)/module.tar.gz
	tar czf $(BIN_OUTPUT_PATH)/module.tar.gz $(BIN_OUTPUT_PATH)/tsp-drawer meta.json

test:
	go test -v -race ./...

gofmt:
	gofmt -w -s .

clean:
	rm -rf $(BIN_OUTPUT_PATH)
