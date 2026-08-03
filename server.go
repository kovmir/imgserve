// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Ivan Kovmir

// Main project code.
package main

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"html/template"
	"io"
	"io/fs"
	"log/slog"
	"mime"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	linkTimeDelim = "_"
	linkRandIDLen = 16
)

type server struct {
	cfg config
	// Custom logger
	logger *slog.Logger
	// Image upload directory chroot.
	chroot *os.Root
	// Upload form HTML template.
	htmlForm *template.Template
	// Web page favicon.
	favicon []byte
	// These are function pointers,
	// so they can be replaced during unit testing.
	currTime func() time.Time
	randID   func(int) string
}

func newServer(cfg config, formHTML string, favicon []byte) (*server, error) {
	if err := os.MkdirAll(cfg.uploadPath, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cfg.uploadPath)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("upload-form").Parse(formHTML)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return &server{
		cfg:      cfg,
		logger:   slog.New(slog.NewTextHandler(os.Stdout, nil)),
		chroot:   root,
		htmlForm: tmpl,
		favicon:  favicon,
		currTime: time.Now,
		randID:   defaultRandomID,
	}, nil
}

func (s *server) close() error {
	if s.chroot != nil {
		return s.chroot.Close()
	}
	return nil
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/", s.handleView)
	return mux
}

// Save uploaded image on disk and create a symlink with TTL in the name
// pointing at it.
func (s *server) saveImage(data io.Reader, ttl time.Time) (string, error) {
	allowedTypes := map[string]bool{
		"image/jpeg": true,
		"image/png":  true,
		"image/gif":  true,
		"image/webp": true,
		"image/bmp":  true,
		"image/tiff": true,
		"image/avif": true,
	}
	// Peek at the first 512 bytes to detect content type.
	head, err := io.ReadAll(io.LimitReader(data, 512))
	if err != nil {
		return "", err
	}
	contentType := http.DetectContentType(head)
	if !allowedTypes[contentType] {
		return "", errors.New("invalid image type")
	}
	exts, _ := mime.ExtensionsByType(contentType)
	if len(exts) == 0 {
		return "", errors.New("invalid image type")
	}
	imgExt := exts[0]

	// Stream file to disk and compute checksum.
	tmpName := "." + s.randID(16) + "_upload"
	tmpFile, err := s.chroot.OpenFile(tmpName, os.O_EXCL|os.O_WRONLY|os.O_CREATE, 0o644)
	if err != nil {
		return "", err
	}
	defer func() {
		tmpFile.Close()
		s.chroot.Remove(tmpName)
	}()
	hasher := sha256.New()
	src := io.MultiReader(bytes.NewReader(head), data)
	dst := io.MultiWriter(tmpFile, hasher)
	if _, err := io.Copy(dst, src); err != nil {
		return "", err
	}

	imgHash := fmt.Sprintf("%x", hasher.Sum(nil))[:s.cfg.hashLen]
	imgName := imgHash + imgExt
	lnName := fmt.Sprintf("%d%s%s", ttl.Unix(), linkTimeDelim, s.randID(linkRandIDLen))
	// Create a symlink to the image holding TTL in the name.
	if err := s.chroot.Symlink(imgName, lnName); err != nil {
		return "", err
	}
	// Hardlink temporary file to the image name.
	if err := s.chroot.Link(tmpName, imgName); err != nil {
		return "", err
	}

	s.logger.Info("image saved", "link", lnName, "target", imgName)
	return imgName, nil
}

// Delete the image from disk and the link pointing at it.
func (s *server) deleteImage(lnName string) error {
	lnTarget, err := s.chroot.Readlink(lnName)
	if err != nil {
		return err
	}

	// Remove the image first...
	if err := s.chroot.Remove(lnTarget); err != nil {
		return err
	}

	// At this point the server may crash, leaving the dangling symlink
	// behind. That's fine, because the server will take care of it on
	// restart.

	// Then the link.
	if err := s.chroot.Remove(lnName); err != nil {
		return err
	}
	s.logger.Info("image deleted", "link", lnName, "target", lnTarget)
	return nil
}

// Remove images past their expiration time (TTL).
func (s *server) imgGarbageCollect() {
	entries, err := fs.ReadDir(s.chroot.FS(), ".")
	if err != nil {
		s.logger.Error("unable to read upload directory", "dir", s.cfg.uploadPath, "err", err)
		return
	}
	now := s.currTime()
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink == 0 {
			continue // Not a link.
		}
		lnName := entry.Name()
		num, _, found := strings.Cut(lnName, linkTimeDelim)
		if !found {
			continue // Invalid link.
		}
		ttl, err := strconv.ParseInt(num, 10, 64)
		if err != nil {
			continue // Invalid link.
		}

		expiryTime := time.Unix(ttl, 0)
		if now.After(expiryTime) {
			if err := s.deleteImage(lnName); err != nil {
				s.logger.Error("unable to delete image", "img", lnName, "err", err)
			}
		}
	}
}

// Clean up dangling symlinks and temporary upload files after a server crash.
func (s *server) cleanOrphans() error {
	entries, err := fs.ReadDir(s.chroot.FS(), ".")
	if err != nil {
		return err
	}
	for _, entry := range entries {
		// Links.
		if entry.Type()&fs.ModeSymlink != 0 {
			lnName := entry.Name()
			_, err := s.chroot.Stat(lnName)
			if errors.Is(err, fs.ErrNotExist) {
				// Dangling link.
				if err := s.chroot.Remove(lnName); err != nil {
					return err
				}
				continue
			}
			if err != nil {
				return err
			}
		}
		// Temporary upload files.
		if entry.Name()[0] == '.' {
			if err := s.chroot.Remove(entry.Name()); err != nil {
				return err
			}
		}
	}
	return nil
}

// Handle server upload requests.
func (s *server) handleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}
	realIP := r.Header.Get("X-Real-IP")
	if realIP == "" {
		realIP = r.RemoteAddr
	}
	s.logger.Info("incoming request", "method", r.Method, "path", r.URL.Path, "ip", realIP)

	// Read the data from the form.
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.maxImgSize)
	if err := r.ParseMultipartForm(64 << 10); err != nil { // 64KiB
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			http.Error(w, "Too large", http.StatusRequestEntityTooLarge)
			return
		}
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}
	// Read image.
	file, _, err := r.FormFile("image")
	if err != nil {
		http.Error(w, "No \"image\" key in the POST form.", http.StatusBadRequest)
		return
	}
	defer file.Close()
	// Read TTL.
	formTTL := r.FormValue("ttl")

	var ttl time.Duration
	if formTTL == "" {
		// Set default TTL.
		ttl = s.cfg.defaultTTL
	} else {
		// Parse and validate TTL.
		duration, err := time.ParseDuration(formTTL)
		if err != nil {
			http.Error(w, "Invalid TTL.", http.StatusBadRequest)
			return
		}
		if duration > s.cfg.maxTTL || duration < s.cfg.minTTL {
			http.Error(w, "Invalid TTL.", http.StatusBadRequest)
			return
		}
		ttl = duration
	}

	// Save image on disk.
	imgName, err := s.saveImage(file, s.currTime().Add(ttl))
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			http.Error(w, "Already there", http.StatusConflict)
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		s.logger.Error("unable to save uploaded image on disk", "err", err)
		return
	}

	// Build response URL.
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		scheme = "http"
		if r.TLS != nil {
			scheme = "https"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	url := fmt.Sprintf("%s://%s/%s", scheme, host, imgName)

	if r.FormValue("redirect") == "true" {
		// Redirect to the image.
		http.Redirect(w, r, url, http.StatusSeeOther)
	} else {
		// Reply with the image URL.
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		fmt.Fprintln(w, url)
	}
}

// Handle image download requests.
func (s *server) handleView(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}
	realIP := r.Header.Get("X-Real-IP")
	if realIP == "" {
		realIP = r.RemoteAddr
	}
	reqPath := r.URL.Path
	s.logger.Info("incoming request", "method", r.Method, "path", reqPath, "ip", realIP)

	// Serve upload form.
	if reqPath == "/" {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := s.htmlForm.Execute(w, struct {
			DefaultTTL time.Duration
			MaxTTL     time.Duration
			MinTTL     time.Duration
			GitVersion string
		}{
			s.cfg.defaultTTL,
			s.cfg.maxTTL,
			s.cfg.minTTL,
			s.cfg.gitVersion,
		}); err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			s.logger.Error("unable to execute template", "err", err)
		}
		return
	}

	// Serve web page favicon.
	if reqPath == "/favicon.ico" {
		w.Header().Set("Content-Type", "image/vnd.microsoft.icon")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if _, err := w.Write(s.favicon); err != nil {
			s.logger.Error("unable to send favicon", "err", err)
		}
		return
	}

	// Serve the image.
	http.ServeFileFS(w, r, s.chroot.FS(), reqPath)
}

// Periodically run garbage collector, runs in a separate goroutine.
func (s *server) gcLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	for range ticker.C {
		s.imgGarbageCollect()
	}
}
