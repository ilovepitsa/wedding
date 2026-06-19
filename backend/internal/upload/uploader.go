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
)

var uuidRe = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

const (
	metaName   = "meta.json"
	dataName   = "data"
	gcGrace    = 10 * time.Minute
	gcInterval = 10 * time.Minute
)

// ChunkUploader stores incoming chunks on disk and assembles them into a
// single file. It is only constructed in chunked mode.
type ChunkUploader struct {
	root    string
	ttl     time.Duration
	mu      sync.Mutex
	uploads map[string]*uploadState
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
	createdAt time.Time
}

type metaFile struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Total     int       `json:"total"`
	Size      int64     `json:"size"`
	MIME      string    `json:"mime"`
	Received  []bool    `json:"received"`
	CreatedAt time.Time `json:"created_at"`
}

func NewChunkUploader(root string, ttl time.Duration) *ChunkUploader {
	u := &ChunkUploader{root: root, ttl: ttl, uploads: map[string]*uploadState{}}
	_ = os.MkdirAll(root, 0o755)
	u.recover()
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
