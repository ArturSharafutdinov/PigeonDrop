// Package storage keeps the recordings. Handlers talk to the Store interface, so that
// something other than a local directory (an S3-compatible bucket) can replace Disk later
// without touching them.
package storage

import (
	"errors"
	"io"
	"time"
)

var (
	// ErrExists is returned by Put when the name is already taken. Put never overwrites.
	ErrExists = errors.New("file already exists")
	// ErrNotFound is returned when a name does not exist.
	ErrNotFound = errors.New("file not found")
	// ErrInvalidName is returned for names that are not a plain file name.
	ErrInvalidName = errors.New("invalid file name")
)

// Entry describes one stored file.
type Entry struct {
	Name    string
	Size    int64
	ModTime time.Time
}

// Store holds named blobs.
type Store interface {
	// Put stores data under name. The file appears complete or not at all, and an existing
	// file is never replaced (ErrExists).
	Put(name string, data []byte) error
	// Open returns the file for reading.
	Open(name string) (io.ReadSeekCloser, error)
	// Remove deletes a file (ErrNotFound if there is none).
	Remove(name string) error
	// List returns every file, sorted by name.
	List() ([]Entry, error)
}
