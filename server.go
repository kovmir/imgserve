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
	"log"
	"mime"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const linkTimeDelim = "_"

type Server struct {
	Cfg Config
	// Image upload directory chroot.
	UploadRoot *os.Root
	// Upload form HTML template.
	UploadForm *template.Template
	// Web page favicon.
	Favicon []byte
	// These are function pointers,
	// so they can be replaced during unit testing.
	Now      func() time.Time
	RandomID func(int) string
}

func NewServer(cfg Config, formHTML string, favicon []byte) (*Server, error) {
	if err := os.MkdirAll(cfg.UploadDir, 0o755); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(cfg.UploadDir)
	if err != nil {
		return nil, err
	}
	tmpl, err := template.New("upload-form").Parse(formHTML)
	if err != nil {
		_ = root.Close()
		return nil, err
	}
	return &Server{
		Cfg:        cfg,
		UploadRoot: root,
		UploadForm: tmpl,
		Favicon:    favicon,
		Now:        time.Now,
		RandomID:   defaultRandomID,
	}, nil
}

func (s *Server) Close() error {
	if s.UploadRoot != nil {
		return s.UploadRoot.Close()
	}
	return nil
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/upload", s.HandleUpload)
	mux.HandleFunc("/", s.HandleView)
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
func (s *Server) SaveImage(imgData []byte, imgExt string, imgExpiresAt time.Time) (string, error) {
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
	imgHash := fmt.Sprintf("%x", sha256.Sum256(imgData))[:s.Cfg.NShaChars]
	// Checksum will be the name of the image to avoid duplicate uploads.
	imgName := imgHash + imgExt

	// Create a symlink pointing to the image. The link holds expiration
	// timestamp in the name and random characters to avoid naming
	// collisions.
	lnName := fmt.Sprintf("%d%s%s", imgExpiresAt.Unix(), linkTimeDelim, s.RandomID(8))
	lnPath := filepath.Join(s.Cfg.UploadDir, lnName)
	if err := os.Symlink(imgName, lnPath); err != nil {
		return "", err
	}

	// At this point the server may crash, leaving the dangling symlink
	// behind. That's fine, because the server will take care of it on
	// restart.

	// Save the image on disk.
	imgPath := filepath.Join(s.Cfg.UploadDir, imgName)
	if err := writeFileExcl(imgPath, imgData, 0o644); err != nil {
		// Could not save the image, so remove the dangling symlink.
		_ = os.Remove(lnPath)
		return "", err
	}
	log.Printf("%s -> %s saved\n", lnName, imgName)
	return imgName, nil
}

// Delete the image from disk and the link pointing at it.
func (s *Server) DeleteImage(linkName string) error {
	linkPath := filepath.Join(s.Cfg.UploadDir, linkName)
	targetName, err := os.Readlink(linkPath)
	if err != nil {
		return err
	}
	targetPath := filepath.Join(s.Cfg.UploadDir, targetName)

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
	log.Printf("%s -> %s deleted", linkName, targetName)
	return nil
}

// Remove images past their expiration time (TTL).
func (s *Server) GarbageCollect() {
	entries, err := os.ReadDir(s.Cfg.UploadDir)
	if err != nil {
		log.Println(err)
		return
	}
	now := s.Now()
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
			if err := s.DeleteImage(linkName); err != nil {
				log.Println(err)
			}
		}
	}
}

// Clean up dangling symlinks after a possible server crash.
func (s *Server) CleanOrphanLinks() {
	entries, err := os.ReadDir(s.Cfg.UploadDir)
	if err != nil {
		log.Println(err)
		return
	}
	for _, entry := range entries {
		if entry.Type()&fs.ModeSymlink == 0 {
			continue // Not a link.
		}
		linkPath := filepath.Join(s.Cfg.UploadDir, entry.Name())
		_, err := os.Stat(linkPath)
		if errors.Is(err, fs.ErrNotExist) {
			// Dangling link.
			_ = os.Remove(linkPath)
			continue
		}
		if err != nil {
			// Unknown error.
			log.Println(err)
		}
	}
}

// Handle server upload requests.
func (s *Server) HandleUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Invalid method", http.StatusMethodNotAllowed)
		return
	}
	realIP := r.Header.Get("X-Real-IP")
	if realIP == "" {
		realIP = r.RemoteAddr
	}
	log.Println("upload from", realIP)

	// Read the incoming image from the form.
	r.Body = http.MaxBytesReader(w, r.Body, s.Cfg.MaxSize)
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
		formTTL = s.Cfg.DefaultTTL.String()
	}
	ttl, err := time.ParseDuration(formTTL)
	if err != nil {
		http.Error(w, "Invalid TTL.", http.StatusBadRequest)
		return
	}
	if ttl > s.Cfg.MaxTTL || ttl < s.Cfg.MinTTL {
		http.Error(w, "Invalid TTL.", http.StatusBadRequest)
		return
	}

	// Save the image on disk.
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, file); err != nil {
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Println(err)
		return
	}
	imgName, err := s.SaveImage(buf.Bytes(), filepath.Ext(header.Filename), s.Now().Add(ttl))
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			http.Error(w, "Already there", http.StatusConflict)
			return
		}
		http.Error(w, "Internal server error", http.StatusInternalServerError)
		log.Println(err)
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
func (s *Server) HandleView(w http.ResponseWriter, r *http.Request) {
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
		if err := s.UploadForm.Execute(w, struct {
			DefaultTTL time.Duration
			MaxTTL     time.Duration
			MinTTL     time.Duration
			GitVersion string
		}{
			s.Cfg.DefaultTTL,
			s.Cfg.MaxTTL,
			s.Cfg.MinTTL,
			s.Cfg.GitVersion,
		}); err != nil {
			log.Println(err)
			http.Error(w, "Internal server error", http.StatusInternalServerError)
		}
		return
	}

	// Serve web page favicon.
	if reqPath == "/favicon.ico" {
		w.Header().Set("Content-Type", "image/vnd.microsoft.icon")
		w.Header().Set("Cache-Control", "public, max-age=86400")
		if _, err := w.Write(s.Favicon); err != nil {
			log.Println(err)
		}
		return
	}

	// Serve the image.
	http.ServeFileFS(w, r, s.UploadRoot.FS(), reqPath)
}

// Periodically run garbage collector, runs in a separate goroutine.
func (s *Server) gcLoop(interval time.Duration) {
	ticker := time.NewTicker(interval)
	for range ticker.C {
		s.GarbageCollect()
	}
}
