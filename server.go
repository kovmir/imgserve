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
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const linkTimeDelim = "_"

type server struct {
	cfg config
	// Custom logger
	logger *slog.Logger
	// Image upload directory chroot.
	uploadRoot *os.Root
	// Upload form HTML template.
	uploadForm *template.Template
	// Web page favicon.
	favicon []byte
	// These are function pointers,
	// so they can be replaced during unit testing.
	now      func() time.Time
	randomID func(int) string
}

func newServer(cfg config, formHTML string, favicon []byte) (*server, error) {
	if err := os.MkdirAll(cfg.uploadDir, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cfg.uploadDir)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("upload-form").Parse(formHTML)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return &server{
		cfg:        cfg,
		logger:     slog.New(slog.NewTextHandler(os.Stdout, nil)),
		uploadRoot: root,
		uploadForm: tmpl,
		favicon:    favicon,
		now:        time.Now,
		randomID:   defaultRandomID,
	}, nil
}

func (s *server) close() error {
	if s.uploadRoot != nil {
		return s.uploadRoot.Close()
	}
	return nil
}

func (s *server) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", s.handleUpload)
	mux.HandleFunc("/", s.handleView)
	return mux
}

// os.WriteFile with O_EXCL for atomic file creation without overrides.
func writeFileExcl(name string, data []byte, perm os.FileMode) error {
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

// Save uploaded image on disk and create a symlink with TTL in the name
// pointing at it.
func (s *server) saveImage(imgData []byte, imgExt string, imgExpiresAt time.Time) (string, error) {
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
		// The filetype is known, so the error can be ignored.
		exts, _ := mime.ExtensionsByType(contentType)
		imgExt = exts[0]
	}
	// Calculate image checksum.
	imgHash := fmt.Sprintf("%x", sha256.Sum256(imgData))[:s.cfg.nShaChars]
	// Checksum will be the name of the image to avoid duplicate uploads.
	imgName := imgHash + imgExt

	// Create a symlink pointing to the image. The link holds expiration
	// timestamp in the name and random characters to avoid naming
	// collisions.
	lnName := fmt.Sprintf("%d%s%s", imgExpiresAt.Unix(), linkTimeDelim, s.randomID(8))
	lnPath := filepath.Join(s.cfg.uploadDir, lnName)
	if err := os.Symlink(imgName, lnPath); err != nil {
		return "", err
	}

	// At this point the server may crash, leaving the dangling symlink
	// behind. That's fine, because the server will take care of it on
	// restart.

	// Save the image on disk.
	imgPath := filepath.Join(s.cfg.uploadDir, imgName)
	if err := writeFileExcl(imgPath, imgData, 0o644); err != nil {
		// Could not save the image, so remove the dangling symlink.
		_ = os.Remove(lnPath)
		return "", err
	}
	s.logger.Info("image saved", "link", lnName, "target", imgName)
	return imgName, nil
}

// Delete the image from disk and the link pointing at it.
func (s *server) deleteImage(linkName string) error {
	linkPath := filepath.Join(s.cfg.uploadDir, linkName)
	targetName, err := os.Readlink(linkPath)
	if err != nil {
		return err
	}
	targetPath := filepath.Join(s.cfg.uploadDir, targetName)

	// Remove the image first...
	if err := os.Remove(targetPath); err != nil {
		return err
	}

	// At this point the server may crash, leaving the dangling symlink
	// behind. That's fine, because the server will take care of it on
	// restart.

	// Then the link.
	if err := os.Remove(linkPath); err != nil {
		return err
	}
	s.logger.Info("image deleted", "link", linkName, "target", targetName)
	return nil
}

// Remove images past their expiration time (TTL).
func (s *server) garbageCollect() {
	dir := s.cfg.uploadDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.logger.Error("unable to read upload directory", "dir", dir, "err", err)
		return
	}
	now := s.now()
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink == 0 {
			continue // Not a link.
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
		if now.After(expiryTime) {
			if err := s.deleteImage(linkName); err != nil {
				s.logger.Error("unable to delete image", "img", linkName, "err", err)
			}
		}
	}
}

// Clean up dangling symlinks after a possible server crash.
func (s *server) cleanOrphanLinks() {
	dir := s.cfg.uploadDir
	entries, err := os.ReadDir(dir)
	if err != nil {
		s.logger.Error("unable to read upload directory", "dir", dir, "err", err)
		return
	}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink == 0 {
			continue // Not a link.
		}
		linkPath := filepath.Join(s.cfg.uploadDir, entry.Name())
		_, err := os.Stat(linkPath)
		if errors.Is(err, fs.ErrNotExist) {
			// Dangling link.
			_ = os.Remove(linkPath)
			continue
		}
		if err != nil {
			// Unknown error.
			s.logger.Error("unable to resolve link", "link", linkPath, "err", err)
		}
	}
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

	// Read the incoming image from the form.
	r.Body = http.MaxBytesReader(w, r.Body, s.cfg.maxSize)
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

	// Determine the image TTL.
	formTTL := r.FormValue("ttl")
	if formTTL == "" {
		formTTL = s.cfg.defaultTTL.String()
	}
	ttl, err := time.ParseDuration(formTTL)
	if err != nil {
		http.Error(w, "Invalid TTL.", http.StatusBadRequest)
		return
	}
	if ttl > s.cfg.maxTTL || ttl < s.cfg.minTTL {
		http.Error(w, "Invalid TTL.", http.StatusBadRequest)
		return
	}

	// Save the image on disk.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, file); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		s.logger.Error("unable to read from multipart/form-data", "err", err)
		return
	}
	imgName, err := s.saveImage(buf.Bytes(), filepath.Ext(header.Filename), s.now().Add(ttl))
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
		// Redirecto to the image.
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
		if err := s.uploadForm.Execute(w, struct {
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
	http.ServeFileFS(w, r, s.uploadRoot.FS(), reqPath)
}

// Periodically run garbage collector, runs in a separate goroutine.
func (s *server) gcLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	for range ticker.C {
		s.garbageCollect()
	}
}
