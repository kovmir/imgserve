PROJECT := imgserve
IMAGES := uploads
GIT_VERSION := $(shell git describe --tags --always --dirty)
CGO_ENABLED := 0

PREFIX := /usr/local
INSTALL := install

CONTAINER_TAG := kovmir/imgserve:$(GIT_VERSION)
CONTAINER_ENGINE := docker

# Use podman if exists.
PODMAN_CHECK := $(shell command -v podman)
ifneq ($(PODMAN_CHECK),)
    CONTAINER_ENGINE := podman
endif

build:
	CGO_ENABLED="$(CGO_ENABLED)" \
	    go build -ldflags "-X main.gitVersion=$(GIT_VERSION)" -o ./$(PROJECT) .

container:
	$(CONTAINER_ENGINE) build \
		--build-arg GIT_VERSION=$(GIT_VERSION) \
		-t $(CONTAINER_TAG) \
		.

fmt:
	gofmt -w main.go

install:
	mkdir -p "$(DESTDIR)$(PREFIX)/bin"
	$(INSTALL) ./$(PROJECT) "$(DESTDIR)$(PREFIX)/bin/$(PROJECT)"

uninstall:
	rm -f "$(DESTDIR)$(PREFIX)/bin/$(PROJECT)"
	rmdir --ignore-fail-on-non-empty "$(DESTDIR)$(PREFIX)/bin"

clean:
	rm -rf ./$(IMAGES) ./$(PROJECT)

.PHONY: build install uninstall clean
