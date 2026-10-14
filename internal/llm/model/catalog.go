package model

import (
	"encoding/json/v2"
	"errors"
	"fmt"
	"os"
	"sync"
)

var (
	ErrNotInCatalog     = errors.New("model: not in catalog")
	ErrCatalogNotLoaded = errors.New("model: catalog not loaded")
)

type catalogEntry struct {
	Provider string `json:"provider"`
	Protocol string `json:"protocol"`
	BaseURL  string `json:"baseUrl,omitempty"`
	Family   string `json:"family,omitempty"`
}

var (
	catalogMu sync.RWMutex
	catalog   map[string]catalogEntry
)

// LoadCatalog TODO: migrate to resource load
func LoadCatalog(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("model: read catalog %s: %w", path, err)
	}

	var m map[string]catalogEntry
	if err := json.Unmarshal(data, &m); err != nil {
		return fmt.Errorf("model: parse catalog %s: %w", path, err)
	}

	catalogMu.Lock()
	catalog = m
	catalogMu.Unlock()
	return nil
}

func LoadCatalogIfExists(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return nil
	}
	return LoadCatalog(path)
}

func CatalogLoaded() bool {
	catalogMu.RLock()
	defer catalogMu.RUnlock()
	return catalog != nil
}
