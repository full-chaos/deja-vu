package instructions

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
)

var ErrConflict = errors.New("instruction registry revision changed")
var ErrBusy = errors.New("instruction registry locked; retry or inspect a stale .lock directory after a crashed writer")

// Store is the boundary for a future database backend. This implementation is
// for bounded, rarely-written approved configuration, never a transcript store.
type Store interface {
	Load() (Snapshot, error)
	Replace(expected uint64, next Snapshot) (Snapshot, error)
}
type FileStore struct{ Path string }

func (f FileStore) Load() (Snapshot, error) {
	var s Snapshot
	if !absolutePath(f.Path) {
		return s, fmt.Errorf("store must be a clean absolute path")
	}
	info, err := os.Lstat(f.Path)
	if err != nil {
		return s, err
	}
	if !info.Mode().IsRegular() {
		return s, fmt.Errorf("store must be a regular file, not a symlink")
	}
	b, err := readBounded(f.Path, MaxRegistryBytes)
	if err != nil {
		return s, err
	}
	if err = decode(bytes.NewReader(b), &s, MaxRegistryBytes, true); err != nil {
		return s, err
	}
	return s, s.Validate()
}
func (f FileStore) Replace(expected uint64, next Snapshot) (result Snapshot, err error) {
	if !absolutePath(f.Path) {
		return result, fmt.Errorf("store must be a clean absolute path")
	}
	if err = next.Validate(); err != nil {
		return result, err
	}
	if next.Revision != expected {
		return result, fmt.Errorf("input revision must equal --expect")
	}
	if expected == ^uint64(0) {
		return result, fmt.Errorf("registry revision exhausted")
	}
	if err = os.MkdirAll(filepath.Dir(f.Path), 0700); err != nil {
		return result, err
	}
	lock := f.Path + ".lock"
	if err = os.Mkdir(lock, 0700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return result, ErrBusy
		}
		return result, err
	}
	defer func() { err = errors.Join(err, os.Remove(lock)) }()
	old, loadErr := f.Load()
	if errors.Is(loadErr, os.ErrNotExist) {
		old = Snapshot{Version: Version}
	} else if loadErr != nil {
		return result, loadErr
	}
	if old.Revision != expected {
		return result, ErrConflict
	}
	incoming := make(map[string]Rule, len(next.Rules))
	for _, r := range next.Rules {
		incoming[r.ID] = r
	}
	for _, r := range old.Rules {
		n, ok := incoming[r.ID]
		if !ok {
			return result, fmt.Errorf("cannot remove rule %q; revoke it with a new revision", r.ID)
		}
		if !reflect.DeepEqual(r, n) && (r.Revision == ^uint64(0) || n.Revision != r.Revision+1) {
			return result, fmt.Errorf("changed rule %q must increment revision by one", r.ID)
		}
		delete(incoming, r.ID)
	}
	for _, r := range incoming {
		if r.Revision != 1 {
			return result, fmt.Errorf("new rule %q must start at revision 1", r.ID)
		}
	}
	previous := make(map[string]Exception, len(old.Exceptions))
	for _, e := range old.Exceptions {
		previous[e.ID] = e
	}
	for _, e := range next.Exceptions {
		if old, ok := previous[e.ID]; ok && !reflect.DeepEqual(old, e) {
			return result, fmt.Errorf("exception %q is immutable; use a new id", e.ID)
		}
	}
	next.Revision = expected + 1
	data, err := json.MarshalIndent(next, "", "  ")
	if err != nil {
		return result, err
	}
	data = append(data, '\n')
	if len(data) > MaxRegistryBytes {
		return result, fmt.Errorf("registry exceeds %d bytes", MaxRegistryBytes)
	}
	history := f.Path + ".revisions"
	if err = os.MkdirAll(history, 0700); err != nil {
		return result, err
	}
	archive := filepath.Join(history, strconv.FormatUint(next.Revision, 10)+".json")
	if b, e := readBounded(archive, MaxRegistryBytes); e == nil {
		if !bytes.Equal(b, data) {
			return result, fmt.Errorf("revision archive differs; inspect %s", archive)
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return result, e
	} else if err = atomicWrite(archive, data); err != nil {
		return result, err
	}
	if err = atomicWrite(f.Path, data); err != nil {
		return result, err
	}
	return next, nil
}

// No unlink-before-rename fallback. Local filesystems only. File contents are
// synced; directory entries are not fsynced in v0, so this is not a power-loss
// durability guarantee. An interrupted archive write is inspectable/retryable.
func atomicWrite(destination string, data []byte) (err error) {
	t, err := os.CreateTemp(filepath.Dir(destination), ".instructions-*")
	if err != nil {
		return err
	}
	name := t.Name()
	defer func() {
		if e := t.Close(); e != nil && !errors.Is(e, os.ErrClosed) {
			err = errors.Join(err, e)
		}
		if e := os.Remove(name); e != nil && !errors.Is(e, os.ErrNotExist) {
			err = errors.Join(err, e)
		}
	}()
	if _, err = t.Write(data); err != nil {
		return err
	}
	if err = t.Sync(); err != nil {
		return err
	}
	if err = t.Close(); err != nil {
		return err
	}
	return os.Rename(name, destination)
}
func readBounded(name string, limit int64) ([]byte, error) {
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	b, e := io.ReadAll(io.LimitReader(f, limit+1))
	err = errors.Join(e, f.Close())
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return b, nil
}
func decode(r io.Reader, out any, limit int64, strict bool) error {
	b, err := io.ReadAll(io.LimitReader(r, limit+1))
	if err != nil {
		return err
	}
	if int64(len(b)) > limit {
		return fmt.Errorf("JSON exceeds %d bytes", limit)
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if err = uniqueKeys(d, 0); err != nil {
		return err
	}
	if _, err = d.Token(); err != io.EOF {
		return fmt.Errorf("expected exactly one JSON value")
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.UseNumber()
	if strict {
		d.DisallowUnknownFields()
	}
	return d.Decode(out)
}
func uniqueKeys(d *json.Decoder, depth int) error {
	if depth > 64 {
		return fmt.Errorf("JSON nesting exceeds 64 levels")
	}
	token, err := d.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		keys := make(map[string]bool)
		for d.More() {
			key, e := d.Token()
			if e != nil {
				return e
			}
			name, ok := key.(string)
			if !ok || keys[name] {
				return fmt.Errorf("duplicate or invalid JSON key %q", key)
			}
			keys[name] = true
			if err = uniqueKeys(d, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for d.More() {
			if err = uniqueKeys(d, depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unexpected JSON delimiter")
	}
	_, err = d.Token()
	return err
}
