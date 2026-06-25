package logx

import (
	"log"
	"os"
	"path/filepath"
)

func Open(path string, enabled bool) (*log.Logger, func(), error) {
	if !enabled {
		return log.New(os.Stderr, "", 0), func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, err
	}
	return log.New(f, "", log.LstdFlags|log.Lshortfile), func() { _ = f.Close() }, nil
}
