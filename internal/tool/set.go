package tool

// NewDefault builds the standard tool set: filesystem tools rooted at root, web
// search/fetch, and shell, all governed by p.
func NewDefault(root string, p *Policy) (*Registry, error) {
	if p == nil {
		p = NewPolicy(WorkspaceWrite)
	}
	fs, err := NewFS(root, p)
	if err != nil {
		return nil, err
	}

	reg := NewRegistry()
	for _, t := range fs.Tools() {
		if err := reg.Register(t); err != nil {
			return nil, err
		}
	}
	for _, t := range []Tool{webSearch(p), webFetch(p), shell(p, fs.Root)} {
		if err := reg.Register(t); err != nil {
			return nil, err
		}
	}
	return reg, nil
}
