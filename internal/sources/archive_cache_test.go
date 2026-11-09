package sources

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/MichalOndrejka/conduit/internal/models"
)

func tempArchive(t *testing.T) string {
	t.Helper()
	f, err := os.CreateTemp("", "conduit-test-*")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	return f.Name()
}

// Concurrent sources of a batch asking for the same archive download it
// once; another key downloads separately; Close deletes the files.
func TestArchiveCacheSharesOneDownloadPerKey(t *testing.T) {
	c := NewArchiveCache()
	ctx := WithArchiveCache(context.Background(), c)
	var downloads atomic.Int32
	gate := make(chan struct{})
	download := func(context.Context) (string, error) {
		downloads.Add(1)
		<-gate
		return tempArchive(t), nil
	}
	var wg sync.WaitGroup
	paths := make([]string, 4)
	for i := range paths {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, release, err := fetchArchive(ctx, "repo@main", download)
			if err != nil {
				t.Error(err)
				return
			}
			release()
			paths[i] = p
		}()
	}
	close(gate)
	wg.Wait()
	if downloads.Load() != 1 {
		t.Fatalf("%d downloads, want 1", downloads.Load())
	}
	for _, p := range paths[1:] {
		if p != paths[0] {
			t.Fatalf("paths differ: %v", paths)
		}
	}
	if _, err := os.Stat(paths[0]); err != nil {
		t.Fatal("a released shared file must stay until the batch ends")
	}

	other, release, _ := fetchArchive(ctx, "repo@dev", download)
	release()
	if downloads.Load() != 2 || other == paths[0] {
		t.Errorf("another branch must download separately")
	}

	c.Close()
	for _, p := range []string{paths[0], other} {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Errorf("%s survived Close", p)
		}
	}
}

// A failed download isn't shared: the next caller tries again.
func TestArchiveCacheRetriesAFailedDownload(t *testing.T) {
	c := NewArchiveCache()
	defer c.Close()
	ctx := WithArchiveCache(context.Background(), c)
	calls := 0
	download := func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("cancelled with its sync")
		}
		return tempArchive(t), nil
	}
	if _, _, err := fetchArchive(ctx, "k", download); err == nil {
		t.Fatal("first call: want its error")
	}
	if _, release, err := fetchArchive(ctx, "k", download); err != nil || calls != 2 {
		t.Fatalf("second call: err %v, %d downloads", err, calls)
	} else {
		release()
	}
}

// Without a batch, every sync downloads for itself and deletes the file.
func TestArchiveWithoutBatchIsNotShared(t *testing.T) {
	p, release, err := fetchArchive(context.Background(), "k", func(context.Context) (string, error) { return tempArchive(t), nil })
	if err != nil {
		t.Fatal(err)
	}
	release()
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Error("an unshared download must be deleted on release")
	}
}

// Source Code and Test Code sources of one repository share the zip within
// a batch; a source with another credential downloads its own.
func TestAdoCodeAndTestCodeShareTheRepositoryZip(t *testing.T) {
	zipBody := repoZip(t, map[string]string{"src/App.cs": "class App {}", "tests/AppTests.cs": "class AppTests {}"})
	var downloads atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		downloads.Add(1)
		_, _ = w.Write(zipBody)
	}))
	defer srv.Close()
	src := func(typ, filter, password string) *APISource {
		return &APISource{src: &models.SourceDefinition{ID: typ + password, Type: typ, Config: map[string]string{
			"Url": srv.URL + "/_apis/git/repositories/repo/items", "PathFilter": filter, "AuthType": "basic", "Password": password,
		}}}
	}
	c := NewArchiveCache()
	defer c.Close()
	ctx := WithArchiveCache(context.Background(), c)

	code, err := src(models.SourceCodeRepo, "src/**", "pat").FetchDocuments(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	tests, err := src(models.SourceTestCodeRepo, "tests/**", "pat").FetchDocuments(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(code) != 1 || len(tests) != 1 || downloads.Load() != 1 {
		t.Fatalf("code %d, tests %d, downloads %d; want 1, 1, 1", len(code), len(tests), downloads.Load())
	}
	if _, err := src(models.SourceCodeRepo, "src/**", "other-pat").FetchDocuments(ctx, nil); err != nil {
		t.Fatal(err)
	}
	if downloads.Load() != 2 {
		t.Errorf("another credential must download on its own, got %d downloads", downloads.Load())
	}
}
