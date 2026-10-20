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
	// ContextWindow is the model's context window in tokens. Zero means
	// unknown; callers may leave it unset and provide it from configuration.
	ContextWindow int
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
			Provider:      ce.Provider,
			BaseURL:       ce.BaseURL,
			Name:          modelName,
			Protocol:      ce.Protocol,
			ContextWindow: ce.ContextWindow,
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
			Provider:      bestEntry.Provider,
			BaseURL:       bestEntry.BaseURL,
			Name:          modelName,
			Protocol:      bestEntry.Protocol,
			ContextWindow: bestEntry.ContextWindow,
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

	if m.Provider != "" && m.Protocol != "" && m.ContextWindow != 0 {
		return nil
	}

	info, err := lookup(m.Name)
	if err != nil {
		// An explicit provider and protocol are enough to run without a
		// catalog; the context window then stays unknown.
		if m.Provider != "" && m.Protocol != "" {
			return nil
		}
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
	if m.ContextWindow == 0 {
		m.ContextWindow = info.ContextWindow
	}
	return nil
}
