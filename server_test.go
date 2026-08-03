// SPDX-License-Identifier: MIT
// Copyright (c) 2026 Ivan Kovmir
package main

import (
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"io/fs"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func newTestServer(t *testing.T) *server {
	t.Helper()

	cfg := config{
		uploadPath: t.TempDir(),
		hashLen:    16,
		maxImgSize: 1 << 20,
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
	fixedTime := time.Date(2024, 1, 1, 12, 0, 0, 0, time.UTC)
	s.currTime = func() time.Time { return fixedTime }
	s.randID = func(n int) string { return strings.Repeat("a", n) }
	// Silence logs.
	s.logger = slog.New(slog.NewTextHandler(io.Discard, nil))

	return s
}

func newPNG(t *testing.T, size int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func makeUploadBody(t *testing.T, field, filename string, fileData io.Reader, extra map[string]string) (io.Reader, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	fw, err := mw.CreateFormFile(field, filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(fw, fileData); err != nil {
		t.Fatal(err)
	}
	for k, v := range extra {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func TestSaveImage(t *testing.T) {
	t.Parallel()

	t.Run("save_image", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		img := bytes.NewReader(newPNG(t, 1))
		ttl := srv.currTime()

		imgName, err := srv.saveImage(img, ttl)
		if err != nil {
			t.Fatal(err)
		}
		// Image must exist.
		if _, err := srv.chroot.Stat(imgName); err != nil {
			t.Fatalf("no image: %v", err)
		}
		// Symlink must exist.
		lnName := fmt.Sprintf("%d%s%s", ttl.Unix(), linkTimeDelim, srv.randID(linkRandIDLen))
		target, err := srv.chroot.Readlink(lnName)
		if err != nil {
			t.Fatalf("no symlink: %v", err)
		}
		// Symlink must point at the image.
		if target != imgName {
			t.Fatal("wrong link target")
		}
	})

	t.Run("reject_invalid_image", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		img := bytes.NewReader([]byte("notanimage"))
		_, err := srv.saveImage(img, srv.currTime())
		if !strings.Contains(err.Error(), "invalid image type") {
			t.Fatalf("expected invalid image type error, got: %v", err)
		}
	})

	t.Run("reject_duplicate", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		data := bytes.NewReader(newPNG(t, 1))
		if _, err := srv.saveImage(data, srv.currTime()); err != nil {
			t.Fatal(err)
		}
		data.Seek(0, io.SeekStart)
		if _, err := srv.saveImage(data, srv.currTime()); !errors.Is(err, fs.ErrExist) {
			t.Fatalf("expected fs.ErrExist, got: %v", err)
		}
	})
}

func TestDeleteImage(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	img := bytes.NewReader(newPNG(t, 1))
	ttl := srv.currTime()

	// Save an image.
	imgName, err := srv.saveImage(img, ttl)
	if err != nil {
		t.Fatal(err)
	}

	// Delete the image.
	lnName := fmt.Sprintf("%d%s%s", ttl.Unix(), linkTimeDelim, srv.randID(linkRandIDLen))
	if err := srv.deleteImage(lnName); err != nil {
		t.Fatal(err)
	}
	if _, err := srv.chroot.Stat(imgName); !os.IsNotExist(err) {
		t.Fatal("the deleted image is still there")
	}
	if _, err := srv.chroot.Stat(lnName); !os.IsNotExist(err) {
		t.Fatal("link to the deleted image is still there")
	}
}

func TestGarbageCollect(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)

	// Expired.
	rdr1 := bytes.NewReader(newPNG(t, 1))
	img1, err := srv.saveImage(rdr1, srv.currTime().Add(-time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	// Non-expired.
	rdr2 := bytes.NewReader(newPNG(t, 2))
	img2, err := srv.saveImage(rdr2, srv.currTime().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}

	srv.imgGarbageCollect()

	if _, err := srv.chroot.Stat(img1); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("expired image is still there")
	}
	if _, err := srv.chroot.Stat(img2); err != nil {
		t.Fatal("non-expired image is missing")
	}
}

func TestCleanOrphans(t *testing.T) {
	t.Parallel()
	srv := newTestServer(t)
	data := newPNG(t, 1)
	lnName := "1000" + linkTimeDelim + "abc12345"
	tmpName := ".whatever"
	if err := srv.chroot.Symlink("orpahn-link-to-missing.png", lnName); err != nil {
		t.Fatal(err)
	}
	if err := srv.chroot.WriteFile(tmpName, data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := srv.cleanOrphans(); err != nil {
		t.Fatal(err)
	}

	if _, err := srv.chroot.Stat(lnName); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("dangling symlink not removed")
	}
	if _, err := srv.chroot.Stat(tmpName); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("temporary file not removed")
	}
}

func TestHandleUpload(t *testing.T) {
	t.Parallel()

	t.Run("upload_image", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		body, contentType := makeUploadBody(t, "image", "test.png", bytes.NewReader(newPNG(t, 1)), nil)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Host", "example.com")
		srv.handleUpload(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusOK)
		}
		if !strings.Contains(rr.Body.String(), "http://example.com/") {
			t.Fatalf("unexpected body: %s", rr.Body.String())
		}
	})

	t.Run("reject_invalid_method", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/upload", nil)

		srv.handleUpload(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("reject_no_image", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", nil)
		srv.handleUpload(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("reject_invalid_ttl", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		body, contentType := makeUploadBody(t, "image", "test.png", bytes.NewReader(newPNG(t, 1)), map[string]string{"ttl": "bad"})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)

		srv.handleUpload(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("reject_ttl_too_long", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		pngReader := bytes.NewReader(newPNG(t, 1))
		tooHighTTL := srv.currTime().Add(srv.cfg.maxTTL).Add(time.Hour)
		body, contentType := makeUploadBody(t, "image", "test.png", pngReader, map[string]string{"ttl": tooHighTTL.String()})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)

		srv.handleUpload(rr, req)
		if rr.Code != http.StatusBadRequest {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusBadRequest)
		}
	})

	t.Run("reject_image_too_large", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		srv.cfg.maxImgSize = 10
		body, contentType := makeUploadBody(t, "image", "test.png", bytes.NewReader(newPNG(t, 1)), nil)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)
		srv.handleUpload(rr, req)
		if rr.Code != http.StatusRequestEntityTooLarge {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusRequestEntityTooLarge)
		}
	})

	t.Run("redirect_to_image", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		body, contentType := makeUploadBody(t, "image", "test.png", bytes.NewReader(newPNG(t, 1)), map[string]string{"redirect": "true"})
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Host", "example.com")
		srv.handleUpload(rr, req)

		if rr.Code != http.StatusSeeOther {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusSeeOther)
		}
		loc := rr.Header().Get("Location")
		if !strings.Contains(loc, "http://example.com/") {
			t.Fatalf("unexpected location: %s", loc)
		}
	})

	t.Run("ignore_client_supplied_extension", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		body, contentType := makeUploadBody(t, "image", "evil.html", bytes.NewReader(newPNG(t, 1)), nil)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/upload", body)
		req.Header.Set("Content-Type", contentType)
		req.Header.Set("Host", "example.com")
		srv.handleUpload(rr, req)

		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusOK)
		}
		respBody := strings.TrimSpace(rr.Body.String())
		if strings.HasSuffix(respBody, ".html") {
			t.Fatalf("response leaks client-supplied extension: %s", respBody)
		}
		if !strings.HasSuffix(respBody, ".png") {
			t.Fatalf("expected .png extension, got: %s", respBody)
		}
	})
}

func TestHandleView(t *testing.T) {
	t.Parallel()

	t.Run("index.html", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		srv.handleView(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusOK)
		}
		if ct := rr.Header().Get("Content-Type"); ct != "text/html; charset=utf-8" {
			t.Fatalf("content-type=%q", ct)
		}
	})

	t.Run("favicon", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/favicon.ico", nil)
		srv.handleView(rr, req)
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
		srv := newTestServer(t)
		imgName, err := srv.saveImage(bytes.NewReader(newPNG(t, 1)), srv.currTime().Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}

		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/"+imgName, nil)
		srv.handleView(rr, req)
		if rr.Code != http.StatusOK {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusOK)
		}
	})

	t.Run("reject_invalid_method", func(t *testing.T) {
		t.Parallel()
		srv := newTestServer(t)
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/", nil)
		srv.handleView(rr, req)
		if rr.Code != http.StatusMethodNotAllowed {
			t.Fatalf("code=%d, want %d", rr.Code, http.StatusMethodNotAllowed)
		}
	})
}
