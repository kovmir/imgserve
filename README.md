# imgserve

[![builds.sr.ht status](https://builds.sr.ht/~kovmir/imgserve.svg)](https://builds.sr.ht/~kovmir/imgserve?)

Minimalist image hosting server.

* No dependencies
* No database
* Image TTL
* Image deduplication

# PREVIEW

![screenshot](screenshot.png)

*Stylish web UI following modern design guidelines.*

# INSTALL

Install [Go](https://go.dev/), then:

```bash
git clone https://git.sr.ht/~kovmir/imgserve
cd imgserve
go install # Installs in ~/go/bin by default.
```

# USAGE

```bash
imgserve -h # To see all CLI options.
imgserve # Simply run the executable to start the server.
```

Upload images via web UI at `http://localhost:8077/` or `curl`:

```bash
curl -X POST -F "image=@/path/to/image.jpg" http://localhost:8077/upload
curl -X POST -F "image=@/path/to/image.jpg" -F "ttl=24h" http://localhost:8077/upload
```

TTL format is documented [here](https://pkg.go.dev/time#ParseDuration).
