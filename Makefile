PROJECT = imgserve
UPLOADS = uploads
VERSION = $(shell git describe --tags)
CGO_ENABLED ?= 0

INSTALL ?= install

build:
	CGO_ENABLED="$(CGO_ENABLED)" \
	    go build -ldflags "-X main.gitVersion=$(VERSION)" -o ./$(PROJECT) .

install:
	mkdir -p "$(DESTDIR)$(PREFIX)/bin"
	$(INSTALL) ./$(PROJECT) "$(DESTDIR)$(PREFIX)/bin/$(PROJECT)"

uninstall:
	rm -f "$(DESTDIR)$(PREFIX)/bin/$(PROJECT)"
	rmdir --ignore-fail-on-non-empty "$(DESTDIR)$(PREFIX)/bin"

clean:
	rm -rf ./$(UPLOADS) ./$(PROJECT)

.PHONY: build install uninstall clean
