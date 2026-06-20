package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"wedding/internal/config"
	"wedding/internal/mockdisk"
	"wedding/internal/upload"
)

// fakeJPEG returns n bytes that smell like a JPEG to http.DetectContentType
// (magic \xff\xd8\xff) followed by padding. The mock disk stores raw bytes,
// so the file need not be a real image.
func fakeJPEG(n int) []byte {
	b := make([]byte, n)
	copy(b, []byte{0xff, 0xd8, 0xff, 0xe0})
	for i := 4; i < n; i++ {
		b[i] = byte(i)
	}
	return b
}

type harness struct {
	t         *testing.T
	api       *httptest.Server
	uploads   string // mockdisk save dir
	tmpUpload string // backend chunk temp dir
	uploader  *upload.ChunkUploader
}

func newHarness(t *testing.T, mode string, ttl time.Duration) *harness {
	t.Helper()
	uploads := t.TempDir()
	mockSrv := httptest.NewServer(mockdisk.NewHandler(uploads))

	tmpUpload := t.TempDir()
	cfg := config.Config{
		YandexAPIBase: mockSrv.URL + "/v1/disk",
		YandexFolder:  "/wedding/photos",
		UploadMode:    mode,
		UploadTmpDir:  tmpUpload,
		UploadGCTTL:   ttl,
	}
	handler, up := build(cfg)
	api := httptest.NewServer(handler)

	t.Cleanup(func() {
		api.Close()
		mockSrv.Close()
	})
	return &harness{t: t, api: api, uploads: uploads, tmpUpload: tmpUpload, uploader: up}
}

func (h *harness) get(path string) *http.Response {
	h.t.Helper()
	resp, err := http.Get(h.api.URL + path)
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func (h *harness) post(path string, body []byte) *http.Response {
	h.t.Helper()
	resp, err := http.Post(h.api.URL+path, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		h.t.Fatal(err)
	}
	return resp
}

func postAt(t *testing.T, base, path string, body []byte) *http.Response {
	t.Helper()
	resp, err := http.Post(base+path, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func getAt(t *testing.T, base, path string) *http.Response {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

func readBody(t *testing.T, r *http.Response) string {
	t.Helper()
	b, _ := io.ReadAll(r.Body)
	r.Body.Close()
	return string(b)
}

func listDir(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return names
}

// waitForFile polls dir until at least one file appears or the timeout elapses.
// The shipper offloads to Yandex (mockdisk) asynchronously, so the assembled
// file lands in uploads/ a few milliseconds after /complete returns 200.
func waitForFile(t *testing.T, dir string, timeout time.Duration) []string {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if entries, _ := os.ReadDir(dir); len(entries) > 0 {
			var names []string
			for _, e := range entries {
				names = append(names, e.Name())
			}
			return names
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for file in %s", dir)
	return nil
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func decodeJSON(t *testing.T, r *http.Response) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	return m
}

func TestSimpleUpload(t *testing.T) {
	h := newHarness(t, "simple", 6*time.Hour)
	body := fakeJPEG(1234)

	resp := h.post("/api/upload?name=test.jpg", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("upload: %d %s", resp.StatusCode, readBody(t, resp))
	}

	files := listDir(t, h.uploads)
	if len(files) != 1 {
		t.Fatalf("expected 1 saved file, got %v", files)
	}
	if !bytes.Equal(readFile(t, filepath.Join(h.uploads, files[0])), body) {
		t.Fatal("saved content mismatch")
	}

	c := decodeJSON(t, h.get("/api/config"))
	if c["uploadMode"] != "simple" {
		t.Fatalf("uploadMode = %v", c["uploadMode"])
	}
}

func TestSimpleModeDisablesChunkEndpoints(t *testing.T) {
	h := newHarness(t, "simple", 6*time.Hour)
	resp := h.post("/api/upload/chunk?uploadId=11111111-1111-1111-1111-111111111111&index=0&offset=0&total=1&size=10&name=x.jpg", []byte("x"))
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 for chunk endpoint in simple mode, got %d", resp.StatusCode)
	}
}

func TestChunkedUploadEndToEnd(t *testing.T) {
	h := newHarness(t, "chunked", 6*time.Hour)
	uid := "22222222-2222-2222-2222-222222222222"
	data := fakeJPEG(2500)
	const total = 3
	const chunkSize = 1000

	for i := 0; i < total; i++ {
		off := i * chunkSize
		end := off + chunkSize
		if end > len(data) {
			end = len(data)
		}
		resp := h.post(fmt.Sprintf("/api/upload/chunk?uploadId=%s&index=%d&offset=%d&total=%d&size=%d&name=test.jpg",
			uid, i, off, total, len(data)), data[off:end])
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("chunk %d: %d %s", i, resp.StatusCode, readBody(t, resp))
		}
	}

	st := decodeJSON(t, h.get("/api/upload/status?uploadId="+uid))
	if int(st["total"].(float64)) != total {
		t.Fatalf("status total = %v", st["total"])
	}

	resp := h.post("/api/upload/complete?uploadId="+uid+"&name=test.jpg", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: %d %s", resp.StatusCode, readBody(t, resp))
	}

	files := waitForFile(t, h.uploads, 5*time.Second)
	if len(files) != 1 {
		t.Fatalf("expected 1 assembled file, got %v", files)
	}
	if !bytes.Equal(readFile(t, filepath.Join(h.uploads, files[0])), data) {
		t.Fatal("assembled content mismatch")
	}

	// temp dir cleaned up immediately on successful ship
	if entries, _ := os.ReadDir(h.tmpUpload); len(entries) != 0 {
		t.Fatalf("temp dir not cleaned: %v", entries)
	}
}

func TestChunkedIncompleteComplete(t *testing.T) {
	h := newHarness(t, "chunked", 6*time.Hour)
	uid := "33333333-3333-3333-3333-333333333333"
	h.post("/api/upload/chunk?uploadId="+uid+"&index=0&offset=0&total=3&size=30&name=test.jpg", fakeJPEG(10))

	resp := h.post("/api/upload/complete?uploadId="+uid+"&name=test.jpg", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("expected 409 for incomplete upload, got %d %s", resp.StatusCode, readBody(t, resp))
	}
}

func TestChunkedDuplicateChunkIdempotent(t *testing.T) {
	h := newHarness(t, "chunked", 6*time.Hour)
	uid := "44444444-4444-4444-4444-444444444444"
	body := fakeJPEG(10)
	for i := 0; i < 2; i++ {
		resp := h.post("/api/upload/chunk?uploadId="+uid+"&index=0&offset=0&total=1&size=10&name=test.jpg", body)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("dup chunk %d: %d %s", i, resp.StatusCode, readBody(t, resp))
		}
	}
	st := decodeJSON(t, h.get("/api/upload/status?uploadId="+uid))
	rec := st["received"].([]any)
	if len(rec) != 1 {
		t.Fatalf("expected 1 received, got %v", rec)
	}
}

func TestChunkedInvalidTypeRejected(t *testing.T) {
	h := newHarness(t, "chunked", 6*time.Hour)
	uid := "55555555-5555-5555-5555-555555555555"
	resp := h.post("/api/upload/chunk?uploadId="+uid+"&index=0&offset=0&total=1&size=10&name=test.txt", []byte("plaintext-data"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid type, got %d %s", resp.StatusCode, readBody(t, resp))
	}
	if entries, _ := os.ReadDir(h.tmpUpload); len(entries) != 0 {
		t.Fatalf("temp dir not cleaned after rejection: %v", entries)
	}
}

func TestChunkedResumeAcrossRestart(t *testing.T) {
	uploads := t.TempDir()
	mockSrv := httptest.NewServer(mockdisk.NewHandler(uploads))
	t.Cleanup(mockSrv.Close)
	tmpUpload := t.TempDir()

	mkcfg := func() config.Config {
		return config.Config{
			YandexAPIBase: mockSrv.URL + "/v1/disk",
			YandexFolder:  "/wedding/photos",
			UploadMode:    "chunked",
			UploadTmpDir:  tmpUpload,
			UploadGCTTL:   6 * time.Hour,
		}
	}

	uid := "66666666-6666-6666-6666-666666666666"
	h1, _ := build(mkcfg())
	api1 := httptest.NewServer(h1)
	t.Cleanup(api1.Close)

	data := fakeJPEG(2500)
	for _, i := range []int{0, 1} {
		off := i * 1000
		resp := postAt(t, api1.URL, fmt.Sprintf("/api/upload/chunk?uploadId=%s&index=%d&offset=%d&total=3&size=%d&name=test.jpg", uid, i, off, len(data)), data[off:off+1000])
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("chunk %d: %d", i, resp.StatusCode)
		}
	}

	// Second server instance simulating a backend restart (same tmp dir).
	h2, _ := build(mkcfg())
	api2 := httptest.NewServer(h2)
	t.Cleanup(api2.Close)

	st := decodeJSON(t, getAt(t, api2.URL, "/api/upload/status?uploadId="+uid))
	rec := st["received"].([]any)
	if len(rec) != 2 {
		t.Fatalf("expected 2 received chunks after restart, got %v", rec)
	}

	off := 2 * 1000
	resp := postAt(t, api2.URL, fmt.Sprintf("/api/upload/chunk?uploadId=%s&index=2&offset=%d&total=3&size=%d&name=test.jpg", uid, off, len(data)), data[off:])
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("chunk 2: %d", resp.StatusCode)
	}
	resp = postAt(t, api2.URL, "/api/upload/complete?uploadId="+uid+"&name=test.jpg", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("complete: %d %s", resp.StatusCode, readBody(t, resp))
	}

	files := waitForFile(t, uploads, 5*time.Second)
	if len(files) != 1 {
		t.Fatalf("expected 1 file, got %v", files)
	}
	if !bytes.Equal(readFile(t, filepath.Join(uploads, files[0])), data) {
		t.Fatal("assembled content mismatch after resume")
	}
}

func TestCompleteIdempotentAsync(t *testing.T) {
	h := newHarness(t, "chunked", 6*time.Hour)
	uid := "99999999-9999-9999-9999-999999999999"
	data := fakeJPEG(1500)
	const total = 2
	for i := 0; i < total; i++ {
		off := i * 1000
		end := off + 1000
		if end > len(data) {
			end = len(data)
		}
		resp := h.post(fmt.Sprintf("/api/upload/chunk?uploadId=%s&index=%d&offset=%d&total=%d&size=%d&name=test.jpg",
			uid, i, off, total, len(data)), data[off:end])
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("chunk %d: %d", i, resp.StatusCode)
		}
	}

	// Call complete twice — second must be a no-op (already queued).
	for i := 0; i < 2; i++ {
		resp := h.post("/api/upload/complete?uploadId="+uid+"&name=test.jpg", nil)
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("complete %d: %d %s", i, resp.StatusCode, readBody(t, resp))
		}
	}

	files := waitForFile(t, h.uploads, 5*time.Second)
	if len(files) != 1 {
		t.Fatalf("expected 1 file (idempotent), got %v", files)
	}
}

func TestGCreapsExpiredUpload(t *testing.T) {
	h := newHarness(t, "chunked", 1*time.Millisecond)
	uid := "77777777-7777-7777-7777-777777777777"
	h.post("/api/upload/chunk?uploadId="+uid+"&index=0&offset=0&total=3&size=30&name=test.jpg", fakeJPEG(10))

	time.Sleep(20 * time.Millisecond) // definitely past the 1ms TTL
	h.uploader.GCOnce()

	if entries, _ := os.ReadDir(h.tmpUpload); len(entries) != 0 {
		t.Fatalf("expected temp dir empty after GC, got %v", entries)
	}
	resp := h.get("/api/upload/status?uploadId=" + uid)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("expected 404 after GC, got %d", resp.StatusCode)
	}
}

func TestGCKeepsFreshUpload(t *testing.T) {
	h := newHarness(t, "chunked", 6*time.Hour)
	uid := "88888888-8888-8888-8888-888888888888"
	h.post("/api/upload/chunk?uploadId="+uid+"&index=0&offset=0&total=3&size=30&name=test.jpg", fakeJPEG(10))
	h.uploader.GCOnce()
	resp := h.get("/api/upload/status?uploadId=" + uid)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("fresh upload should survive GC, got %d", resp.StatusCode)
	}
}
