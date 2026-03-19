package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const uploadFormHTML = `
<!DOCTYPE html>
<html>
<head>
  <title>imgserv</title>
  <meta charset="utf-8">
</head>
<body>
  <form method="POST" action="/upload" enctype="multipart/form-data">
    <p><input type="file" name="image" accept="image/*" required></p>
    <p>TTL: <input type="text" name="ttl" value="72h" required></p>
    <p><button type="submit">Upload</button></p>
  </form>
</body>
</html>
`

const linkTimeDelim = "_"

var (
	runGC      bool
	nShaChars  int
	delayGC    uint64
	listenAddr string
	defaultTTL string
	uploadDir  string
	urlProto   string
	urlHost    string
	maxSize    int64
)

// Valid paths have no subdirectories.
func isValidPath(path string) bool {
	// Split and filter empty parts
	parts := strings.Split(strings.Trim(path, "/"), "/")

	// "/"        OK
	// "/abc"     OK
	// "/abc/def" BAD
	return len(parts) <= 1
}

// Save the image and create a link pointing to it;
// the link holds expiration timestamp in the name.
func saveImage(imgData []byte, imgExt string, imgExpiresAt time.Time) (string, error) {
	if imgExt == "" { // Infer file type if not provided.
		contentType := http.DetectContentType(imgData)
		exts, _ := mime.ExtensionsByType(contentType)
		if len(exts) > 0 {
			imgExt = exts[0]
		} else {
			imgExt = ".bin"
		}
	}

	imgHash := fmt.Sprintf("%x", sha256.Sum256(imgData))[:nShaChars]
	imgName := imgHash + imgExt
	imgPath := filepath.Join(uploadDir, imgName)
	imgURL := fmt.Sprintf("%s://%s/%s", urlProto, urlHost, imgName)

	if _, err := os.Stat(imgPath); err == nil {
		// File already exists.
		return imgURL, nil
	}

	// Save on disk.
	//
	// This bit is not threadsafe, but how likely is it that two different
	// clients will upload the exact same file at exactly the same instant?
	if err := os.WriteFile(imgPath, imgData, 0644); err != nil {
		return "", err
	}
	// Create the symlink; it holds the image's expiration time.
	// Aside from the expiration time, we append a random string
	// to the link name to avoid naming collisions.
	lnName := fmt.Sprintf("%d%s%s", imgExpiresAt.Unix(), linkTimeDelim, randomID(8))
	lnPath := filepath.Join(uploadDir, lnName)
	if err := os.Symlink(imgName, lnPath); err != nil {
		return "", err
	}

	log.Printf("%s -> %s saved\n", lnName, imgName)
	return imgURL, nil
}

// Delete the link and the image it points to.
func deleteImage(link string) error {
	linkPath := filepath.Join(uploadDir, link)
	target, err := os.Readlink(linkPath)
	if err != nil {
		return err
	}

	if err := os.Remove(linkPath); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(uploadDir, target)); err != nil {
		return err
	}

	log.Printf("%s -> %s deleted", link, target)
	return nil
}

// Upload images.
func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}

	if err := r.ParseMultipartForm(maxSize); err != nil {
		http.Error(w, "Form is too big or corrupt.", http.StatusBadRequest)
		return
	}
	file, header, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "No \"image\" key in the POST form.", http.StatusBadRequest)
		return
	}
	defer file.Close()

	ttlStr := r.FormValue("ttl")
	if ttlStr == "" {
		ttlStr = defaultTTL
	}
	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		http.Error(w, "Invalid TTL.", http.StatusBadRequest)
		return
	}

	var buf bytes.Buffer
	io.Copy(&buf, file)
	url, err := saveImage(buf.Bytes(), filepath.Ext(header.Filename), time.Now().Add(ttl))
	if err != nil {
		http.Error(w, "Server error", http.StatusInternalServerError)
		return
	}

	//fmt.Fprintln(w, url)
	http.Redirect(w, r, url, http.StatusFound)
}

// Serve images.
func handleView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}

	reqPath := r.URL.Path

	if !isValidPath(reqPath) {
		http.NotFound(w, r)
		return
	}

	if reqPath == "/" {
		w.Header().Set("Content-Type", "text/html; charest=utf-8")
		fmt.Fprint(w, uploadFormHTML)
		return
	}

	if reqPath == "/favicon.ico" {
		http.NotFound(w, r)
		return
	}

	imgPath := filepath.Join(uploadDir, reqPath)
	log.Println(reqPath[1:], "requested")
	if _, err := os.Stat(imgPath); errors.Is(err, os.ErrNotExist) {
		http.NotFound(w, r)
		return
	}

	ext := filepath.Ext(imgPath)
	if contentType := mime.TypeByExtension(ext); contentType != "" {
		w.Header().Set("Content-Type", contentType)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}

	http.ServeFile(w, r, imgPath)
}

// Returns a string with random ascii numbers/letters of the specified length.
func randomID(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	seed := rand.New(rand.NewSource(time.Now().UnixNano()))
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[seed.Intn(len(letters))]
	}
	return string(b)
}

// Delete images past the expiration time.
func expiredGC() {
	files, _ := filepath.Glob(filepath.Join(uploadDir, "*"+linkTimeDelim+"*"))
	for _, f := range files {
		if info, err := os.Stat(f); err == nil {
			linkName := info.Name()
			unixTime := linkName[:strings.Index(linkName, linkTimeDelim)]
			i, _ := strconv.ParseInt(unixTime, 10, 64)
			expiryTime := time.Unix(i, 0)

			if time.Now().After(expiryTime) {
				err := deleteImage(linkName)
				if err != nil {
					log.Println(err)
				}
			}
		}
	}
}

func init() {
	flag.BoolVar(&runGC, "del", true, "delete images past the expiration time?")
	flag.Int64Var(&maxSize, "maxsize", 32<<20, "maximal uploaded image size in bytes")
	flag.IntVar(&nShaChars, "sumlen", 32, "number of sha256 characters used for image file names")
	flag.StringVar(&defaultTTL, "ttl", "72h", "default image TTL")
	flag.StringVar(&listenAddr, "addr", ":8077", "server listen address")
	flag.StringVar(&uploadDir, "dir", "./uploads", "uploaded images directory")
	flag.StringVar(&urlHost, "host", "localhost:8077", "hostname in the responce URL")
	flag.StringVar(&urlProto, "proto", "http", "protocol in the responce URL")
	flag.Uint64Var(&delayGC, "delay", 10, "expired image checker interval in seconds")
	flag.Parse()
}

func main() {
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		panic(err)
	}
	if _, err := time.ParseDuration(defaultTTL); err != nil {
		panic(errors.New("invalid default TTL"))
	}
	if nShaChars < 16 || nShaChars > 64 {
		panic(errors.New("use between 16 and 64 sha256 characters"))
	}

	http.HandleFunc("/upload", handleUpload)
	http.HandleFunc("/", handleView)

	if runGC == true {
		go func() {
			interval := time.Duration(delayGC) * time.Second
			ticker := time.NewTicker(interval)
			for range ticker.C {
				expiredGC()
			}
		}()
	}

	log.Println("listening on", listenAddr)
	if err := http.ListenAndServe(listenAddr, nil); err != nil {
		panic(err)
	}
}
