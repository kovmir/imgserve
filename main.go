// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Ivan Kovmir
package main

import (
	"bytes"
	"crypto/sha256"
	_ "embed"
	"errors"
	"flag"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log"
	"math/rand"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed upload_form.html
var uploadFormHTML string
var uploadForm *template.Template

//go:embed favicon.ico
var faviconData []byte

const linkTimeDelim = "_"

var (
	// Flags
	runGarbageCollector bool
	nShaChars           int
	delayGC             uint64
	listenAddr          string
	defaultTTLStr       string
	uploadDir           string
	urlProto            string
	urlHost             string
	maxSize             int64
	minTTLStr           string
	maxTTLStr           string

	uploadRoot *os.Root

	defaultTTL time.Duration
	minTTL     time.Duration
	maxTTL     time.Duration

	gitVersion string
)

// os.WriteFile(...) with O_EXCL; for safety against concurrent writes.
func writeFileExcl(name string, data []byte, perm os.FileMode) error {
	// O_EXCL ensures atomic creation: fails if file exists.
	f, err := os.OpenFile(name, os.O_EXCL|os.O_WRONLY|os.O_CREATE, perm)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err1 := f.Close(); err1 != nil && err == nil {
		err = err1
	}
	return err
}

// Save the image and create a link pointing to it;
// the link holds expiration timestamp in the name.
func saveImage(imgData []byte, imgExt string, imgExpiresAt time.Time) (string, error) {
	allowedTypes := map[string]bool{
		"image/jpeg": true,
		"image/png":  true,
		"image/gif":  true,
		"image/webp": true,
		"image/bmp":  true,
		"image/tiff": true,
		"image/avif": true,
	}

	contentType := http.DetectContentType(imgData)
	if !allowedTypes[contentType] {
		return "", errors.New("invalid image type")
	}

	if imgExt == "" {
		// _ is safe due to the above type whitelisting.
		exts, _ := mime.ExtensionsByType(contentType)
		imgExt = exts[0]
	}

	imgHash := fmt.Sprintf("%x", sha256.Sum256(imgData))[:nShaChars]
	imgName := imgHash + imgExt
	imgPath := filepath.Join(uploadDir, imgName)
	imgURL := fmt.Sprintf("%s://%s/%s", urlProto, urlHost, imgName)

	// Create a symlink pointing to the image.
	// The name of the symlink holds expiration time and a random string to
	// avoid naming collisions.
	lnName := fmt.Sprintf("%d%s%s", imgExpiresAt.Unix(), linkTimeDelim, randomID(8))
	lnPath := filepath.Join(uploadDir, lnName)
	if err := os.Symlink(imgName, lnPath); err != nil {
		return "", err
	}
	// If the server crashes at this point, we are left with a dangling
	// symlink, which will be cleaned up at server restart. All good.

	// Save image on disk.
	// We create the link first so we end up with dangling symlinks, not
	// orphan images, in the event of a server crash.
	if err := writeFileExcl(imgPath, imgData, 0644); err != nil {
		os.Remove(lnPath) // Remove dangling symlink.
		// File already exists or unknown I/O error.
		return "", err
	}

	log.Printf("%s -> %s saved\n", lnName, imgName)
	return imgURL, nil
}

// Delete the link and the image it points to.
func deleteImage(linkName string) error {
	linkPath := filepath.Join(uploadDir, linkName)
	targetName, err := os.Readlink(linkPath)
	if err != nil {
		return err
	}
	targetPath := filepath.Join(uploadDir, targetName)

	// Checking for dangling symlinks is easier,
	// so remove the target first.
	if err := os.Remove(targetPath); err != nil {
		return err
	}
	// If at this point the server crashes,
	// it may leave a dangling symlink behind.
	if err := os.Remove(linkPath); err != nil {
		return err
	}

	log.Printf("%s -> %s deleted", linkName, targetName)
	return nil
}

// Upload images.
func handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}

	realIP := r.Header.Get("X-Real-IP")
	if realIP == "" {
		realIP = r.RemoteAddr
	}
	log.Println("upload from", realIP)

	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	file, header, err := r.FormFile("image")
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "No \"image\" key in the POST form.", http.StatusBadRequest)
		return
	}
	defer file.Close()

	ttlStr := r.FormValue("ttl")
	if ttlStr == "" {
		ttlStr = defaultTTLStr
	}
	ttl, err := time.ParseDuration(ttlStr)
	if err != nil {
		http.Error(w, "Invalid TTL.", http.StatusBadRequest)
		return
	}
	if ttl > maxTTL {
		ttl = maxTTL
	}
	if ttl < minTTL {
		ttl = minTTL
	}

	var buf bytes.Buffer
	_, err = io.Copy(&buf, file)
	if err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Println(err)
		return
	}
	url, err := saveImage(buf.Bytes(), filepath.Ext(header.Filename), time.Now().Add(ttl))
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			http.Error(w, "Already there", http.StatusConflict)
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Println(err)
		return
	}

	if r.FormValue("redirect") == "true" {
		http.Redirect(w, r, url, http.StatusSeeOther)
	} else {
		fmt.Fprintln(w, url)
	}
}

// Serve images.
func handleView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}

	realIP := r.Header.Get("X-Real-IP")
	if realIP == "" {
		realIP = r.RemoteAddr
	}

	reqPath := path.Clean(r.URL.Path)
	log.Println(reqPath, "requested by", realIP)

	// Serve upload form.
	if reqPath == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		err := uploadForm.Execute(w, struct {
			DefaultTTL string
			MaxTTL     string
			MinTTL     string
			GitVersion string
		}{
			defaultTTLStr,
			maxTTLStr,
			minTTLStr,
			gitVersion,
		})
		if err != nil {
			log.Println(err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	// Serve favicon.
	if reqPath == "/favicon.ico" {
		w.Header().Set("Content-Type", "image/vnd.microsoft.icon")
		w.Header().Set("Cache-Control", "public, max-age=86400") // Cache 1 day
		_, err := w.Write(faviconData)
		if err != nil {
			log.Println(err)
		}
		return
	}

	// Serve image.
	http.ServeFileFS(w, r, uploadRoot.FS(), reqPath)
}

// Returns a string with random ascii letters and numbers.
func randomID(nChars int) string {
	const charSet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, nChars)
	for i := range b {
		b[i] = charSet[rand.Intn(len(charSet))]
	}
	return string(b)
}

// Delete images past the expiration time.
func garbageCollectImages() {
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		log.Println(err)
		return
	}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink == 0 {
			continue // Not symlink.
		}
		linkName := entry.Name()
		if !strings.Contains(linkName, linkTimeDelim) {
			continue // Invalid link.
		}
		linkTime := linkName[:strings.Index(linkName, linkTimeDelim)]
		linkTimeNum, err := strconv.ParseInt(linkTime, 10, 64)
		if err != nil {
			continue // Invalid link.
		}

		expiryTime := time.Unix(linkTimeNum, 0)
		if time.Now().After(expiryTime) {
			// Can delete an image while it is being sent; fine.
			err := deleteImage(linkName)
			if err != nil {
				log.Println(err)
			}
		}
	}
}

// Delete dangling symlinks in the upload directory after server crash.
func cleanOrphanLinks() {
	entries, err := os.ReadDir(uploadDir)
	if err != nil {
		log.Println(err)
		return
	}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink == 0 {
			continue // Not symlink.
		}

		linkPath := filepath.Join(uploadDir, entry.Name())
		_, err := os.Stat(linkPath)
		if errors.Is(err, fs.ErrNotExist) {
			// Dangling symlink.
			os.Remove(linkPath)
			continue
		}
		if err != nil {
			log.Println(err) // Unknown error.
		}
		// Valid symlink.
	}
}

// Verify CLI arguments are valid.
func validateCLIArgs() error {
	if nShaChars < 16 || nShaChars > 64 {
		return errors.New("use between 16 and 64 sha256 characters")
	}

	duration, err := time.ParseDuration(defaultTTLStr)
	if err != nil {
		return errors.New("invalid default TTL")
	}
	defaultTTL = duration
	duration, err = time.ParseDuration(minTTLStr)
	if err != nil {
		return errors.New("invalid minimal TTL")
	}
	minTTL = duration
	duration, err = time.ParseDuration(maxTTLStr)
	if err != nil {
		return errors.New("invalid maximal TTL")
	}
	maxTTL = duration
	if minTTL < 5*time.Second {
		return errors.New("minimal TTL cannot be less than 5 seconds")
	}
	if minTTL >= maxTTL {
		return errors.New("maximal TTL must be greater than minimal")
	}
	if defaultTTL > maxTTL || defaultTTL < minTTL {
		return errors.New("default TTL must be within the minimal and maximal TTLs")
	}

	t, err := template.New("upload-form").Parse(uploadFormHTML)
	if err != nil {
		return err
	}
	uploadForm = t

	return nil
}

func init() {
	flag.BoolVar(&runGarbageCollector, "del", true, "delete images past the expiration time?")
	flag.Int64Var(&maxSize, "maxsize", 32<<20, "maximal uploaded image size in bytes")
	flag.IntVar(&nShaChars, "sumlen", 16, "number of sha256 characters used for image file names")
	flag.StringVar(&defaultTTLStr, "ttl", "72h", "default image TTL")
	flag.StringVar(&minTTLStr, "minttl", "1h", "minimal image TTL")
	flag.StringVar(&maxTTLStr, "maxttl", "168h", "maximal image TTL")
	flag.StringVar(&listenAddr, "addr", ":8077", "server listen address")
	flag.StringVar(&uploadDir, "dir", "./uploads", "uploaded images directory")
	flag.StringVar(&urlHost, "host", "localhost:8077", "hostname in the response URL")
	flag.StringVar(&urlProto, "proto", "http", "protocol in the response URL")
	flag.Uint64Var(&delayGC, "delay", 10, "expired image checker interval in seconds")
}

func main() {
	flag.Parse()
	if err := validateCLIArgs(); err != nil {
		panic(err)
	}

	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		panic(err)
	}

	if root, err := os.OpenRoot(uploadDir); err != nil {
		panic(err)
	} else {
		uploadRoot = root
	}

	if runGarbageCollector {
		cleanOrphanLinks()
		go func() {
			interval := time.Duration(delayGC) * time.Second
			ticker := time.NewTicker(interval)
			for range ticker.C {
				garbageCollectImages()
			}
		}()
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/upload", handleUpload)
	mux.HandleFunc("/", handleView)

	srv := http.Server{
		Addr:              listenAddr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Println("listening on", listenAddr)
	if err := srv.ListenAndServe(); err != nil {
		panic(err)
	}
}
