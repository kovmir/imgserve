// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Ivan Kovmir
package main

import (
	"bytes"
	"errors"
	"image"
	"image/png"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T) *server {
	t.Helper()

	dir := t.TempDir()
	cfg := config{
		uploadDir:  dir,
		nShaChars:  16,
		maxSize:    1 << 20,
		defaultTTL: time.Hour,
		maxTTL:     24 * time.Hour,
		minTTL:     time.Minute,
		gitVersion: "test",
	}

	s, err := newServer(cfg, "<html>{{.DefaultTTL}}</html>", []byte("favicon"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.close() })

	// Deterministic replacements.
	fixed := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	s.now = func() time.Time { return fixed }
	s.randomID = func(n int) string { return strings.Repeat("a", n) }
	// Silence logs.
	s.logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	return s
}

func newPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestSaveImage(t *testing.T) {
	t.Parallel()

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		data := newPNG(t)
		dir := s.cfg.uploadDir

		imgName, err := s.saveImage(data, ".png", s.now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}

		// Image must exist.
		imgPath := filepath.Join(dir, imgName)
		if _, err := os.Stat(imgPath); err != nil {
			t.Fatalf("image not found: %v", err)
		}

		// Symlink must exist and point to the image.
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var found bool
		for _, e := range entries {
			if e.Type()&fs.ModeSymlink == 0 {
				continue
			}
			target, err := os.Readlink(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatal(err)
			}
			if target == imgName {
				found = true
				break
			}
		}
		if !found {
			t.Fatal("no symlink pointing to saved image")
		}
	})

	t.Run("invalid_type", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		_, err := s.saveImage([]byte("not an image"), ".png", s.now().Add(time.Hour))
		if err == nil || !strings.Contains(err.Error(), "invalid image type") {
			t.Fatalf("expected invalid image type error, got: %v", err)
		}
	})

	t.Run("duplicate", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		data := newPNG(t)
		if _, err := s.saveImage(data, ".png", s.now().Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		_, err := s.saveImage(data, ".png", s.now().Add(time.Hour))
		if !errors.Is(err, fs.ErrExist) {
			t.Fatalf("expected fs.ErrExist, got: %v", err)
		}
	})
}

func TestDeleteImage(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	data := newPNG(t)
	dir := s.cfg.uploadDir
	imgName, err := s.saveImage(data, ".png", s.now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	entries, _ := os.ReadDir(dir)
	var linkName string
	for _, e := range entries {
		if e.Type()&fs.ModeSymlink != 0 {
			linkName = e.Name()
			break
		}
	}
	if linkName == "" {
		t.Fatal("no symlink created")
	}

	if err := s.deleteImage(linkName); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, imgName)); !os.IsNotExist(err) {
		t.Fatal("image should be deleted")
	}
	if _, err := os.Stat(filepath.Join(dir, linkName)); !os.IsNotExist(err) {
		t.Fatal("link should be deleted")
	}
}

func TestGarbageCollect(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	dir := s.cfg.uploadDir

	// Expired.
	data1 := newPNG(t)
	img1, err := s.saveImage(data1, ".png", s.now().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	// Create a different image.
	data2 := append([]byte(nil), data1...)
	data2 = append(data2, 0x00)
	// Non-exipred.
	img2, err := s.saveImage(data2, ".png", s.now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	s.garbageCollect()

	if _, err := os.Stat(filepath.Join(dir, img1)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("expired image should be removed")
	}
	if _, err := os.Stat(filepath.Join(dir, img2)); err != nil {
		t.Fatal("valid image should remain")
	}
}

func TestCleanOrphanLinks(t *testing.T) {
	t.Parallel()
	s := newTestServer(t)
	linkName := filepath.Join(s.cfg.uploadDir, "1000"+linkTimeDelim+"abc12345")
	if err := os.Symlink("orpahn-link-to-missing.png", linkName); err != nil {
		t.Fatal(err)
	}

	s.cleanOrphanLinks()

	if _, err := os.Stat(linkName); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("dangling symlink should be removed")
	}
}

func TestWriteFileExcl(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "foo.txt")
	if err := writeFileExcl(path, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeFileExcl(path, []byte("b"), 0o644); !errors.Is(err, fs.ErrExist) {
		t.Fatalf("expected fs.ErrExist, got: %v", err)
	}
}

func TestHandleUpload(t *testing.T) {
	t.Parallel()

	t.Run("method_not_allowed", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/upload", nil)
		s.handleUpload(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("missing_image", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", nil)
		s.handleUpload(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("invalid_ttl", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		body, contentType := makeUploadBody(t, "image", "test.png", newPNG(t), map[string]string{"ttl": "bad"})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)
		s.handleUpload(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("ttl_too_large", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		body, contentType := makeUploadBody(t, "image", "test.png", newPNG(t), map[string]string{"ttl": "1000h"})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)
		s.handleUpload(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("too_large", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		s.cfg.maxSize = 10
		body, contentType := makeUploadBody(t, "image", "test.png", newPNG(t), nil)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)
		s.handleUpload(rr, req)
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusRequestEntityTooLarge)
		}
	})

	t.Run("success", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		body, contentType := makeUploadBody(t, "image", "test.png", newPNG(t), nil)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Host", "example.com")
		s.handleUpload(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusOK)
		}
		if !strings.Contains(rr.Body.String(), "http://example.com/") {
			t.Fatalf("unexpected body: %s", rr.Body.String())
		}
	})

	t.Run("redirect", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		body, contentType := makeUploadBody(t, "image", "test.png", newPNG(t), map[string]string{"redirect": "true"})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Host", "example.com")
		s.handleUpload(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusSeeOther)
		}
		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "http://example.com/") {
			t.Fatalf("unexpected location: %s", loc)
		}
	})
}

func TestHandleView(t *testing.T) {
	t.Parallel()

	t.Run("root", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		s.handleView(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusOK)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Fatalf("content-type=%q", ct)
		}
	})

	t.Run("favicon", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
		s.handleView(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusOK)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "image/vnd.microsoft.icon" {
			t.Fatalf("content-type=%q", ct)
		}
		if rr.Body.String() != "favicon" {
			t.Fatalf("body=%q", rr.Body.String())
		}
	})

	t.Run("image", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		imgName, err := s.saveImage(newPNG(t), ".png", s.now().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/"+imgName, nil)
		s.handleView(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusOK)
		}
	})

	t.Run("method_not_allowed", func(t *testing.T) {
		t.Parallel()
		s := newTestServer(t)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		s.handleView(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusMethodNotAllowed)
		}
	})
}

func makeUploadBody(t *testing.T, field, filename string, fileData []byte, extra map[string]string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(fileData); err != nil {
		t.Fatal(err)
	}
	for k, v := range extra {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}
