package local_test

import (
	"path/filepath"
	"testing"

	"github.com/tlalocweb/hulation/pkg/store/storage"
	"github.com/tlalocweb/hulation/pkg/store/storage/local"
	"github.com/tlalocweb/hulation/pkg/store/storage/storagetest"
)

func TestLocalStorage_Contract(t *testing.T) {
	storagetest.Run(t, func(t *testing.T) (storage.Storage, func()) {
		dir := t.TempDir()
		s, err := local.Open(local.Options{Path: filepath.Join(dir, "data.db")})
		if err != nil {
			t.Fatal(err)
		}
		return s, func() { _ = s.Close() }
	})
}
