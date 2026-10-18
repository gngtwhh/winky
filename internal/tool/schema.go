package tool

// JSON Schema builders for tool parameters. They keep the spec literals in fs.go
// readable and emit the subset every provider understands: an object with
// typed, described properties, optional keys, and no additional properties.
//
// The schemas are sent with Strict off, so a property may be omitted by the
// model and a missing one becomes the zero value after decoding. Marking every
// property required would be needed before enabling strict mode.

// object builds an object schema: the properties map plus the list of required
// property names.
func object(props map[string]any, required ...string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	schema := map[string]any{
		"type":                 "object",
		"properties":           props,
		"additionalProperties": false,
	}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

// stringProp builds a string property.
func stringProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

// intProp builds an integer property. A non-positive min or max is omitted, so
// zero can be used for "unbounded".
func intProp(desc string, min, max int) map[string]any {
	prop := map[string]any{"type": "integer", "description": desc}
	if min > 0 {
		prop["minimum"] = min
	}
	if max > 0 {
		prop["maximum"] = max
	}
	return prop
}

// boolProp builds a boolean property.
func boolProp(desc string) map[string]any {
	return map[string]any{"type": "boolean", "description": desc}
}
