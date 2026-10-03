package storage

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

// validName allows letters, digits, dot, underscore and hyphen, and must not start with a dot,
// so a name can never climb out of the directory nor collide with a temporary file.
var validName = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9._-]{0,254}$`)

// ValidName reports whether name is acceptable to a Store.
func ValidName(name string) bool {
	return validName.MatchString(name) && !strings.Contains(name, "..")
}

// Disk is a Store backed by one directory.
type Disk struct{ dir string }

var _ Store = (*Disk)(nil)

// NewDisk uses dir, creating it if needed.
func NewDisk(dir string) (*Disk, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return nil, fmt.Errorf("create storage directory: %w", err)
	}
	return &Disk{dir: dir}, nil
}

func (d *Disk) path(name string) (string, error) {
	if !ValidName(name) {
		return "", fmt.Errorf("%w: %q", ErrInvalidName, name)
	}
	return filepath.Join(d.dir, name), nil
}

// Put writes to a temporary file in the same directory and then links it into place. The link
// fails if the name exists, which makes "never overwrite" atomic even under concurrent calls,
// and readers never see a half-written file.
func (d *Disk) Put(name string, data []byte) (err error) {
	final, err := d.path(name)
	if err != nil {
		return err
	}

	tmp, err := os.CreateTemp(d.dir, ".tmp-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }() // the link, not the temp name, is what stays

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("write file: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("sync file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}

	if err := os.Link(tmp.Name(), final); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return ErrExists
		}
		return fmt.Errorf("move file into place: %w", err)
	}
	return nil
}

func (d *Disk) Open(name string) (io.ReadSeekCloser, error) {
	p, err := d.path(name)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(p)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, ErrNotFound
	}
	return f, err
}

func (d *Disk) Remove(name string) error {
	p, err := d.path(name)
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return ErrNotFound
		}
		return err
	}
	return nil
}

func (d *Disk) List() ([]Entry, error) {
	items, err := os.ReadDir(d.dir)
	if err != nil {
		return nil, fmt.Errorf("read storage directory: %w", err)
	}

	entries := make([]Entry, 0, len(items))
	for _, item := range items {
		if !item.Type().IsRegular() || !ValidName(item.Name()) { // skips temporary files and anything foreign
			continue
		}
		info, err := item.Info()
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue // removed between ReadDir and Info
			}
			return nil, err
		}
		entries = append(entries, Entry{Name: item.Name(), Size: info.Size(), ModTime: info.ModTime()})
	}
	slices.SortFunc(entries, func(a, b Entry) int { return strings.Compare(a.Name, b.Name) })
	return entries, nil
}
