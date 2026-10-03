package storage

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func newDisk(t *testing.T) (*Disk, string) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "uploads") // must be created by NewDisk
	d, err := NewDisk(dir)
	if err != nil {
		t.Fatal(err)
	}
	return d, dir
}

func TestPutOpenRoundTrip(t *testing.T) {
	d, _ := newDisk(t)
	if err := d.Put("a.opus", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	f, err := d.Open("a.opus")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if got, _ := io.ReadAll(f); string(got) != "hello" {
		t.Fatalf("got %q", got)
	}
}

func TestPutNeverOverwrites(t *testing.T) {
	d, _ := newDisk(t)
	if err := d.Put("a.opus", []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := d.Put("a.opus", []byte("second")); !errors.Is(err, ErrExists) {
		t.Fatalf("got %v, want ErrExists", err)
	}
	f, _ := d.Open("a.opus")
	defer f.Close()
	if got, _ := io.ReadAll(f); string(got) != "first" {
		t.Fatalf("the original was replaced: %q", got)
	}
}

func TestConcurrentPutOfOneNameHasExactlyOneWinnerAndLeavesNoTempFiles(t *testing.T) {
	d, dir := newDisk(t)

	var winners, losers atomic.Int32
	var wg sync.WaitGroup
	for range 30 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			switch err := d.Put("race.opus", []byte(strings.Repeat("x", 1<<16))); {
			case err == nil:
				winners.Add(1)
			case errors.Is(err, ErrExists):
				losers.Add(1)
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}()
	}
	wg.Wait()

	if winners.Load() != 1 || losers.Load() != 29 {
		t.Fatalf("winners %d, losers %d", winners.Load(), losers.Load())
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 1 {
		t.Fatalf("expected only race.opus in the directory, found %d entries", len(left))
	}
}

func TestInvalidNamesAreRefusedEverywhere(t *testing.T) {
	d, dir := newDisk(t)
	for _, name := range []string{"", "../escape.opus", "a/b.opus", `a\b.opus`, ".hidden", "..", "a..b.opus", "bad name.opus", strings.Repeat("a", 300)} {
		if err := d.Put(name, []byte("x")); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Put(%q): got %v, want ErrInvalidName", name, err)
		}
		if _, err := d.Open(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Open(%q): got %v, want ErrInvalidName", name, err)
		}
		if err := d.Remove(name); !errors.Is(err, ErrInvalidName) {
			t.Errorf("Remove(%q): got %v, want ErrInvalidName", name, err)
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(dir), "escape.opus")); err == nil {
		t.Fatal("a file escaped the storage directory")
	}
}

func TestOpenAndRemoveMissing(t *testing.T) {
	d, _ := newDisk(t)
	if _, err := d.Open("nothing.wav"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Open: %v", err)
	}
	if err := d.Remove("nothing.wav"); !errors.Is(err, ErrNotFound) {
		t.Errorf("Remove: %v", err)
	}
}

func TestRemove(t *testing.T) {
	d, _ := newDisk(t)
	_ = d.Put("a.wav", []byte("x"))
	if err := d.Remove("a.wav"); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Open("a.wav"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("still there: %v", err)
	}
}

func TestListIsSortedAndIgnoresTemporaryAndForeignFiles(t *testing.T) {
	d, dir := newDisk(t)
	for _, name := range []string{"2026-10-03_b.opus", "2026-10-03_a.wav", "2026-10-02_z.opus"} {
		if err := d.Put(name, []byte(name)); err != nil {
			t.Fatal(err)
		}
	}
	_ = os.WriteFile(filepath.Join(dir, ".tmp-123"), []byte("half"), 0o600)
	_ = os.WriteFile(filepath.Join(dir, ".DS_Store"), nil, 0o600)
	_ = os.Mkdir(filepath.Join(dir, "subdir"), 0o750)

	entries, err := d.List()
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name)
	}
	want := "2026-10-02_z.opus 2026-10-03_a.wav 2026-10-03_b.opus"
	if strings.Join(names, " ") != want {
		t.Fatalf("got %v", names)
	}
	if entries[0].Size != int64(len("2026-10-02_z.opus")) || entries[0].ModTime.IsZero() {
		t.Fatalf("size or time missing: %+v", entries[0])
	}
}

func TestNewDiskFailsWhereItCannotCreateTheDirectory(t *testing.T) {
	blocker := filepath.Join(t.TempDir(), "file")
	_ = os.WriteFile(blocker, nil, 0o600)
	if _, err := NewDisk(filepath.Join(blocker, "uploads")); err == nil {
		t.Fatal("expected an error")
	}
}
