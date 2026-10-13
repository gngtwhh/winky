package model

type Model struct {
	Provider string
	BaseURL  string
	Name     string
	Protocol string
}

func lookup(modelName string) (Model, error) {
	panic("not implemented")
}

// Fix completes the Model information by referring to the table,
// without overwriting the existing information of the parameter.
func Fix(model *Model) error {
	panic("not implemented")
}
