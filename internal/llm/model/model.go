package model

import (
	"errors"
	"strings"
)

type Model struct {
	Provider string
	BaseURL  string
	Name     string
	Protocol string
}

// lookup search for model information in the directory.
// Returns an error if the directory is not loaded or not included.
func lookup(modelName string) (Model, error) {
	if modelName == "" {
		return Model{}, errors.New("model: name is required")
	}

	catalogMu.RLock()
	defer catalogMu.RUnlock()

	if catalog == nil {
		return Model{}, ErrCatalogNotLoaded
	}

	var (
		bestFamily string
		bestEntry  catalogEntry
	)

	if ce, ok := catalog[modelName]; ok {
		return Model{
			Provider: ce.Provider,
			BaseURL:  ce.BaseURL,
			Name:     modelName,
			Protocol: ce.Protocol,
		}, nil
	}

	for _, e := range catalog {
		if e.Family != "" &&
			strings.HasPrefix(modelName, e.Family) &&
			len(e.Family) > len(bestFamily) {
			bestFamily = e.Family
			bestEntry = e
		}
	}

	if bestFamily != "" {
		return Model{
			Provider: bestEntry.Provider,
			BaseURL:  bestEntry.BaseURL,
			Name:     modelName,
			Protocol: bestEntry.Protocol,
		}, nil
	}

	return Model{}, ErrNotInCatalog
}

func Fix(m *Model) error {
	if m == nil {
		return errors.New("model: nil pointer")
	}
	if m.Name == "" {
		return errors.New("model: name is required")
	}

	if m.Provider != "" && m.Protocol != "" {
		return nil
	}

	info, err := lookup(m.Name)
	if err != nil {
		if errors.Is(err, ErrNotInCatalog) && m.Protocol != "" {
			return nil
		}
		return err
	}

	if m.Provider == "" {
		m.Provider = info.Provider
	}
	if m.Protocol == "" {
		m.Protocol = info.Protocol
	}
	if m.BaseURL == "" {
		m.BaseURL = info.BaseURL
	}
	return nil
}
