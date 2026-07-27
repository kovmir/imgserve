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
	"text/template"
	"time"
)

//go:embed upload_form.html
var uploadFormHTML string
var uploadFormTmpl *template.Template

//go:embed favicon.ico
var faviconData []byte

const linkTimeDelim = "_"

var (
	// Flags
	runGC         bool
	nShaChars     int
	delayGC       uint64
	listenAddr    string
	defaultTTLStr string
	uploadDir     string
	urlProto      string
	urlHost       string
	maxSize       int64
	minTTLStr     string
	maxTTLStr     string

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
		// _ is safe due to the type whitelisting.
		exts, _ := mime.ExtensionsByType(contentType)
		imgExt = exts[0]
	}

	imgHash := fmt.Sprintf("%x", sha256.Sum256(imgData))[:nShaChars]
	imgName := imgHash + imgExt
	imgPath := filepath.Join(uploadDir, imgName)
	imgURL := fmt.Sprintf("%s://%s/%s", urlProto, urlHost, imgName)

	// Save on disk.
	if err := writeFileExcl(imgPath, imgData, 0644); err != nil {
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

	realIP := r.Header.Get("X-Real-IP")
	if realIP == "" {
		realIP = r.RemoteAddr
	}
	log.Println("upload from", realIP)

	r.Body = http.MaxBytesReader(w, r.Body, maxSize)

	file, header, err := r.FormFile("image")
	if err != nil {
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
		http.Error(w, "Form is too big", http.StatusBadRequest)
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
	if r.Method != http.MethodGet {
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
		w.Header().Set("Content-Type", "text/html; charest=utf-8")
		uploadFormTmpl.Execute(w, struct {
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
		return
	}

	// Serve favicon.
	if reqPath == "/favicon.ico" {
		w.Header().Set("Content-Type", "image/x-icon")
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

// Returns a string with random ascii numbers/letters of the specified length.
func randomID(n int) string {
	const letters = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		b[i] = letters[rand.Intn(len(letters))]
	}
	return string(b)
}

// Delete images past the expiration time.
func expiredGC() {
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
		if strings.Contains(linkName, linkTimeDelim) == false {
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

// Verify CLI arguments' validity.
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
	uploadFormTmpl = t

	return nil
}

func init() {
	flag.BoolVar(&runGC, "del", true, "delete images past the expiration time?")
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
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		panic(err)
	}

	flag.Parse()
	if err := validateCLIArgs(); err != nil {
		panic(err)
	}

	root, err := os.OpenRoot(uploadDir)
	if err != nil {
		panic(err)
	}
	uploadRoot = root

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
