# imgserve

Minimalist image hosting server.

* No dependencies
* No database
* Image TTL
* Deduplication

# PREVIEW

![screenshot](screenshot.png)

# INSTALL

```bash
git clone https://git.sr.ht/~kovmir/imgserve
cd imgserve
go install # Installs in ~/go/bin by default.
```

# USAGE

```bash
imgserve # Simply run the executable to start the server.
```

Upload images via web UI at `http://localhost:8077/` or `curl`:

```bash
curl -X POST -F "image=@/path/to/image.jpg" http://localhost:8077/upload
```
