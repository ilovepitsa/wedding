package upload

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"

	"wedding/internal/config"
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const (
	metaName   = "meta.json"
	dataName   = "data"
	gcGrace    = 10 * time.Minute
	gcInterval = 10 * time.Minute
)

// ChunkUploader stores incoming chunks on disk and assembles them into a
// single file. It is only constructed in chunked mode. A pool of shipper
// goroutines drains fully-received uploads to Yandex Disk asynchronously.
type ChunkUploader struct {
	root    string
	ttl     time.Duration
	mu      sync.Mutex
	uploads map[string]*uploadState

	cfg         config.Config
	ts          TokenSource
	dc          DiskClient
	shipCh      chan string
	shipWorkers int
}

type uploadState struct {
	mu        sync.Mutex
	dir       string
	id        string
	total     int
	size      int64
	name      string
	mime      string
	received  []bool
	shipping  bool
	createdAt time.Time
}

type metaFile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Total     int       `json:"total"`
	Size      int64     `json:"size"`
	MIME      string    `json:"mime"`
	Received  []bool    `json:"received"`
	Shipping  bool      `json:"shipping"`
	CreatedAt time.Time `json:"created_at"`
}

// NewChunkUploader loads in-flight uploads from disk and starts the shipper
// worker pool. ts/dc may be nil in mock mode (shipper then skips Yandex).
func NewChunkUploader(root string, ttl time.Duration, cfg config.Config, ts TokenSource, dc DiskClient, workers int) *ChunkUploader {
	if workers < 1 {
		workers = 1
	}
	u := &ChunkUploader{
		root:        root,
		ttl:         ttl,
		uploads:     map[string]*uploadState{},
		cfg:         cfg,
		ts:          ts,
		dc:          dc,
		shipCh:      make(chan string, 256),
		shipWorkers: workers,
	}
	_ = os.MkdirAll(root, 0o755)
	u.recover()
	u.requeueReady()
	for i := 0; i < workers; i++ {
		go u.shipLoop()
	}
	return u
}

// recover reloads in-flight uploads from disk so resume survives backend restarts.
func (u *ChunkUploader) recover() {
	entries, err := os.ReadDir(u.root)
	if err != nil {
		return
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		id := e.Name()
		if !uuidRe.MatchString(id) {
			continue
		}
		st, err := loadMeta(filepath.Join(u.root, id))
		if err != nil {
			log.Printf("uploader: skip unparseable %s: %v", id, err)
			continue
		}
		st.dir = filepath.Join(u.root, id)
		u.uploads[id] = st
	}
}

func loadMeta(dir string) (*uploadState, error) {
	b, err := os.ReadFile(filepath.Join(dir, metaName))
	if err != nil {
		return nil, err
	}
	var m metaFile
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, err
	}
	if len(m.Received) != m.Total {
		m.Received = make([]bool, m.Total)
	}
	return &uploadState{
		dir:       dir,
		id:        m.ID,
		total:     m.Total,
		size:      m.Size,
		name:      m.Name,
		mime:      m.MIME,
		received:  m.Received,
		shipping:  m.Shipping,
		createdAt: m.CreatedAt,
	}, nil
}

func (u *ChunkUploader) getOrCreate(id, name string, total int, size int64) (*uploadState, bool, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	st, ok := u.uploads[id]
	if ok {
		// consistency check
		if st.total != total || st.name != name {
			return nil, false, fmt.Errorf("inconsistent upload params")
		}
		return st, false, nil
	}
	dir := filepath.Join(u.root, id)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, false, err
	}
	st = &uploadState{
		dir:       dir,
		id:        id,
		total:     total,
		size:      size,
		name:      name,
		received:  make([]bool, total),
		createdAt: time.Now(),
	}
	u.uploads[id] = st
	return st, true, nil
}

// WriteChunk writes a chunk at the given offset and marks it received.
// index==0 sniffs and validates MIME; failure removes the upload.
func (u *ChunkUploader) WriteChunk(id, name string, index, total int, offset int64, size int64, body []byte) error {
	if !uuidRe.MatchString(id) {
		return fmt.Errorf("invalid uploadId")
	}
	if total <= 0 || index < 0 || index >= total {
		return fmt.Errorf("invalid chunk index")
	}
	st, _, err := u.getOrCreate(id, name, total, size)
	if err != nil {
		return err
	}

	if index == 0 {
		sniff := body
		if len(sniff) > 512 {
			sniff = sniff[:512]
		}
		mime := SniffMIME(sniff)
		if !Allowed(mime, Ext(name)) {
			u.Remove(id)
			return errInvalidType
		}
		st.mu.Lock()
		st.mime = mime
		st.mu.Unlock()
	}

	f, err := os.OpenFile(filepath.Join(st.dir, dataName), os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	if _, err := f.WriteAt(body, offset); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}

	st.mu.Lock()
	if index < len(st.received) {
		st.received[index] = true
	}
	persistErr := st.persist()
	st.mu.Unlock()
	return persistErr
}

var errInvalidType = fmt.Errorf("разрешены только изображения и видео")

func (s *uploadState) persist() error {
	m := metaFile{
		ID:        s.id,
		Name:      s.name,
		Total:     s.total,
		Size:      s.size,
		MIME:      s.mime,
		Received:  s.received,
		Shipping:  s.shipping,
		CreatedAt: s.createdAt,
	}
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	tmp := filepath.Join(s.dir, metaName+".tmp")
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, filepath.Join(s.dir, metaName))
}

// Status returns the received and missing chunk indices.
func (u *ChunkUploader) Status(id string) (received []int, missing []int, total int, ok bool) {
	u.mu.Lock()
	st, exists := u.uploads[id]
	u.mu.Unlock()
	if !exists {
		return nil, nil, 0, false
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for i, got := range st.received {
		if got {
			received = append(received, i)
		} else {
			missing = append(missing, i)
		}
	}
	return received, missing, st.total, true
}

// DataFile verifies all chunks are present and returns the assembled file path.
func (u *ChunkUploader) DataFile(id string) (path string, size int64, mime string, name string, err error) {
	u.mu.Lock()
	st, exists := u.uploads[id]
	u.mu.Unlock()
	if !exists {
		return "", 0, "", "", fmt.Errorf("upload not found")
	}
	st.mu.Lock()
	defer st.mu.Unlock()
	for _, got := range st.received {
		if !got {
			return "", 0, "", "", fmt.Errorf("upload incomplete")
		}
	}
	info, err := os.Stat(filepath.Join(st.dir, dataName))
	if err != nil {
		return "", 0, "", "", err
	}
	return filepath.Join(st.dir, dataName), info.Size(), st.mime, st.name, nil
}

// Remove deletes the upload's temp dir and in-memory state.
func (u *ChunkUploader) Remove(id string) {
	u.mu.Lock()
	st, ok := u.uploads[id]
	if ok {
		delete(u.uploads, id)
	}
	u.mu.Unlock()
	if st != nil {
		_ = os.RemoveAll(st.dir)
	}
}

// allReceived reports whether every chunk is present (caller holds st.mu).
func allReceived(st *uploadState) bool {
	for _, got := range st.received {
		if !got {
			return false
		}
	}
	return true
}

// MarkShipping marks the upload ready for async offload to Yandex Disk and
// enqueues it. Returns false if the upload is missing or incomplete (caller
// maps that to 404/409). Idempotent: a second call on an already-shipping
// upload returns true without re-enqueuing.
func (u *ChunkUploader) MarkShipping(id string) bool {
	u.mu.Lock()
	st, exists := u.uploads[id]
	u.mu.Unlock()
	if !exists {
		return false
	}
	st.mu.Lock()
	if st.shipping {
		st.mu.Unlock()
		return true
	}
	if !allReceived(st) {
		st.mu.Unlock()
		return false
	}
	st.shipping = true
	st.createdAt = time.Now() // fresh TTL window for the ship phase
	persistErr := st.persist()
	st.mu.Unlock()
	if persistErr != nil {
		log.Printf("markshipping %s: persist: %v", id, persistErr)
	}
	u.enqueue(id)
	return true
}

// enqueue sends id to the shipper channel. If the channel is full, spawns a
// goroutine for a blocking send so the upload is never lost.
func (u *ChunkUploader) enqueue(id string) {
	select {
	case u.shipCh <- id:
	default:
		go func() { u.shipCh <- id }()
	}
}

// requeueReady re-enqueues any upload whose chunks are all present — covers
// "answered 200 but crashed before ship" and "all chunks received but
// /complete never arrived". Called once on startup after recover().
func (u *ChunkUploader) requeueReady() {
	u.mu.Lock()
	ids := make([]string, 0, len(u.uploads))
	for id, st := range u.uploads {
		ids = append(ids, id)
		_ = st // access under st.mu below
	}
	u.mu.Unlock()
	for _, id := range ids {
		u.mu.Lock()
		st := u.uploads[id]
		u.mu.Unlock()
		if st == nil {
			continue
		}
		st.mu.Lock()
		ready := allReceived(st)
		if ready {
			st.shipping = true
			_ = st.persist()
		}
		st.mu.Unlock()
		if ready {
			log.Printf("requeue %s: восстановлен как готовый к отгрузке", id)
			u.enqueue(id)
		}
	}
}

// shipLoop drains the shipper channel. Each upload is shipped with retries
// until success or until GC reaps the temp dir (DataFile then returns 404).
func (u *ChunkUploader) shipLoop() {
	for id := range u.shipCh {
		u.shipOne(id)
	}
}

var shipBackoff = []time.Duration{
	5 * time.Second, 10 * time.Second, 30 * time.Second,
	1 * time.Minute, 2 * time.Minute, 5 * time.Minute,
}

func (u *ChunkUploader) shipOne(id string) {
	dataPath, size, mime, name, err := u.DataFile(id)
	if err != nil {
		// upload gone (GC reaped it or already shipped) — nothing to do
		return
	}

	var token string
	if u.ts != nil {
		token, err = u.ts.Token()
		if err != nil {
			log.Printf("ship %s: token: %v — ретраю", id, err)
			u.sleepAndCheck(id, 0)
			u.enqueue(id)
			return
		}
	}

	filename := fmt.Sprintf("%s_%s", id, Sanitize(name))
	remotePath := u.cfg.YandexFolder + "/" + filename

	for attempt := 0; ; attempt++ {
		uploadURL, err := u.dc.UploadURL(token, remotePath)
		if err != nil {
			log.Printf("ship %s: upload url (%s): %v", id, u.boff(attempt), err)
			if !u.sleepAndCheck(id, attempt) {
				return
			}
			continue
		}
		f, err := os.Open(dataPath)
		if err != nil {
			log.Printf("ship %s: open: %v", id, err)
			return // file gone — stop
		}
		t0 := time.Now()
		log.Printf("ship %s: начал заливку на Диск %s (%d байт)", id, remotePath, size)
		err = u.dc.Put(uploadURL, f, size, mime)
		f.Close()
		if err == nil {
			u.Remove(id)
			log.Printf("ship %s: залит на Диск за %s, всего попыток %d", id, time.Since(t0), attempt+1)
			return
		}
		log.Printf("ship %s: put (%s): %v", id, u.boff(attempt), err)
		if !u.sleepAndCheck(id, attempt) {
			return
		}
	}
}

// boff returns the backoff for the given attempt index (clamped).
func (u *ChunkUploader) boff(attempt int) time.Duration {
	if attempt < len(shipBackoff) {
		return shipBackoff[attempt]
	}
	return shipBackoff[len(shipBackoff)-1]
}

// sleepAndCheck waits for the backoff duration, then reports whether the
// upload still exists (true = retry, false = gone, stop).
func (u *ChunkUploader) sleepAndCheck(id string, attempt int) bool {
	time.Sleep(u.boff(attempt))
	u.mu.Lock()
	_, ok := u.uploads[id]
	u.mu.Unlock()
	return ok
}

// GCLoop periodically removes uploads older than ttl.
func (u *ChunkUploader) GCLoop(ctx context.Context) {
	t := time.NewTicker(gcInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			u.GCOnce()
		}
	}
}

// GCOnce performs a single GC pass (also used by tests).
func (u *ChunkUploader) GCOnce() {
	u.mu.Lock()
	ids := make([]string, 0, len(u.uploads))
	for id := range u.uploads {
		ids = append(ids, id)
	}
	u.mu.Unlock()

	now := time.Now()
	for _, id := range ids {
		u.mu.Lock()
		st := u.uploads[id]
		u.mu.Unlock()
		if st == nil {
			continue
		}
		st.mu.Lock()
		expired := now.Sub(st.createdAt) > u.ttl
		st.mu.Unlock()
		if expired {
			u.Remove(id)
		}
	}
}
