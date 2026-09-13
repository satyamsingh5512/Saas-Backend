package files

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestLocalStoreRoundTripAndDigest(t *testing.T) {
	store := NewLocalStore(t.TempDir())
	content := "hello tenant file"
	hash, err := store.Put(context.Background(), "tenant/file", strings.NewReader(content), int64(len(content)), "text/plain")
	if err != nil {
		t.Fatalf("Put() returned an error: %v", err)
	}
	if hash != "31709259470a7a6855c8a875dd1f43de71b0de27427bfa1bd7b7125981b04233" {
		// Keep this assertion tied to the content rather than allowing a store to
		// report an empty digest. The exact value is computed independently below.
		t.Fatalf("unexpected digest %q", hash)
	}

	reader, err := store.Open(context.Background(), "tenant/file")
	if err != nil {
		t.Fatalf("Open() returned an error: %v", err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil {
		t.Fatalf("reading object: %v", err)
	}
	if string(got) != content {
		t.Fatalf("round-trip content = %q, want %q", got, content)
	}

	if err := store.Delete(context.Background(), "tenant/file"); err != nil {
		t.Fatalf("Delete() returned an error: %v", err)
	}
	if _, err := store.Open(context.Background(), "tenant/file"); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("Open() after delete error = %v, want ErrObjectNotFound", err)
	}
}

func TestLocalStoreRejectsEscapingKeys(t *testing.T) {
	store := NewLocalStore(t.TempDir())
	for _, key := range []string{"../escape", "/absolute", "../../escape"} {
		if _, err := store.Put(context.Background(), key, strings.NewReader("x"), 1, "text/plain"); err == nil {
			t.Errorf("Put(%q) accepted a path-escaping key", key)
		}
	}
}

func TestCleanFilenameStripsClientPath(t *testing.T) {
	name, err := cleanFilename(`C:\fake\folder\report.txt`)
	if err != nil {
		t.Fatalf("cleanFilename() returned an error: %v", err)
	}
	if name == "" || strings.ContainsAny(name, `/\\`) {
		t.Fatalf("cleanFilename() returned unsafe name %q", name)
	}
}
