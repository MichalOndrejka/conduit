package sources

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
)

// ArchiveCache shares repository downloads (an Azure DevOps zip, a GitHub
// tarball) between the sources of one sync batch: a Source Code and a Test
// Code source of the same repository, branch and credential download it
// once. Entries live until Close, which the batch calls when it ends;
// nothing is kept between batches.
type ArchiveCache struct {
	mu      sync.Mutex
	entries map[string]*archiveEntry
	closed  bool
}

type archiveEntry struct {
	done chan struct{} // closed once path or err is set
	path string
	err  error
}

func NewArchiveCache() *ArchiveCache {
	return &ArchiveCache{entries: map[string]*archiveEntry{}}
}

type archiveCacheKey struct{}

// WithArchiveCache makes the syncs run under ctx share c's downloads.
func WithArchiveCache(ctx context.Context, c *ArchiveCache) context.Context {
	return context.WithValue(ctx, archiveCacheKey{}, c)
}

// Close removes every downloaded file; later lookups download on their own.
func (c *ArchiveCache) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.closed = true
	for _, e := range c.entries {
		select {
		case <-e.done:
			if e.path != "" {
				os.Remove(e.path)
			}
		default: // still downloading: its downloader removes it (see fetch)
		}
	}
	c.entries = map[string]*archiveEntry{}
}

// fetchArchive returns a local file holding the download for key and a
// release func the caller runs when done with it. Under a batch's cache the
// first caller downloads and later ones wait for it; a download that failed
// (or was cancelled with its sync) is retried by the next caller rather
// than shared. Without a cache it simply downloads, and release deletes.
func fetchArchive(ctx context.Context, key string, download func(context.Context) (string, error)) (string, func(), error) {
	c, _ := ctx.Value(archiveCacheKey{}).(*ArchiveCache)
	if c == nil {
		name, err := download(ctx)
		if err != nil {
			return "", nil, err
		}
		return name, func() { os.Remove(name) }, nil
	}
	keep := func() {} // the batch's Close deletes shared files
	for {
		c.mu.Lock()
		if c.closed {
			c.mu.Unlock()
			return fetchArchive(context.WithValue(ctx, archiveCacheKey{}, (*ArchiveCache)(nil)), key, download)
		}
		e, ok := c.entries[key]
		if !ok {
			e = &archiveEntry{done: make(chan struct{})}
			c.entries[key] = e
			c.mu.Unlock()
			e.path, e.err = download(ctx)
			c.mu.Lock()
			if e.err != nil {
				delete(c.entries, key)
			} else if c.closed {
				os.Remove(e.path) // the batch ended meanwhile: nobody else will use it
				e.path, keep = "", func() {}
			}
			close(e.done)
			c.mu.Unlock()
			if e.err != nil {
				return "", nil, e.err
			}
			if e.path == "" {
				return "", nil, errors.New("sync batch ended during the repository download")
			}
			return e.path, keep, nil
		}
		c.mu.Unlock()
		select {
		case <-e.done:
		case <-ctx.Done():
			return "", nil, ctx.Err()
		}
		if e.err == nil && e.path != "" {
			return e.path, keep, nil
		}
		// The download failed for its own caller (perhaps its sync was
		// cancelled): try again, this time as the downloader.
	}
}

// downloadToFile saves url's body to a new temporary file. check reads the
// saved file back: an archive that ends early (the connection dropped, yet
// the response ended cleanly) fails it with io.ErrUnexpectedEOF, and the
// download is restarted from the beginning, like any download cut off.
func downloadToFile(ctx context.Context, client *http.Client, url, pattern string, prepare func(*http.Request), check func(*os.File) error) (string, error) {
	return requestToFile(ctx, client, http.MethodGet, url, nil, pattern, prepare, check)
}

// requestToFile is downloadToFile for any method and request body.
func requestToFile(ctx context.Context, client *http.Client, method, url string, reqBody []byte, pattern string, prepare func(*http.Request), check func(*os.File) error) (string, error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	name := f.Name()
	err = streamRequestWithRetry(ctx, client, method, url, reqBody, prepare, func(body io.Reader) error {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		if err := f.Truncate(0); err != nil {
			return err
		}
		if _, err := io.Copy(f, body); err != nil {
			return err
		}
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return err
		}
		return check(f)
	})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(name)
		return "", err
	}
	return name, nil
}

// checkTarGz reads a gzipped tarball to its end: gzip verifies its length
// and checksum there, so a truncated download fails with io.ErrUnexpectedEOF.
func checkTarGz(f *os.File) error {
	gz, err := gzip.NewReader(f)
	if err != nil {
		if errors.Is(err, io.EOF) {
			return io.ErrUnexpectedEOF
		}
		return fmt.Errorf("repository archive is not gzip: %w", err)
	}
	_, err = io.Copy(io.Discard, gz)
	return err
}

// checkZip opens a zip, whose directory sits at its end: a file that starts
// like a zip but can't be opened was cut short (io.ErrUnexpectedEOF); one
// that doesn't start like a zip isn't one.
func checkZip(f *os.File) error {
	st, err := f.Stat()
	if err != nil {
		return err
	}
	head := make([]byte, 4)
	n, _ := io.ReadFull(f, head)
	if n < 4 || head[0] != 'P' || head[1] != 'K' {
		if n < 4 && n > 0 && head[0] == 'P' {
			return io.ErrUnexpectedEOF
		}
		return errNotZip
	}
	if _, err := zip.NewReader(f, st.Size()); err != nil {
		return io.ErrUnexpectedEOF
	}
	return nil
}

var errNotZip = errors.New("download is not a zip")
