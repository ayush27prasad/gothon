// Package schema reads the task signatures that a Python module exports as JSON Schema.
package schema

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
)

// Export is the output of `python -m gothon._schema <module>`.
type Export struct {
	Tasks []Task             `json:"tasks"`
	Defs  map[string]*Schema `json:"defs"`
	Hash  string             `json:"hash"`
}

// Task is one @gothon.task function.
type Task struct {
	Name   string  `json:"name"`
	Doc    string  `json:"doc"`
	Input  *Schema `json:"input"` // nil when the task takes no argument
	Output *Schema `json:"output"`
}

// Schema is the subset of JSON Schema that pydantic emits.
type Schema struct {
	Ref                  string     `json:"$ref"`
	Type                 string     `json:"type"`
	Format               string     `json:"format"`
	Title                string     `json:"title"`
	Description          string     `json:"description"`
	Properties           Properties `json:"properties"`
	Required             []string   `json:"required"`
	Items                *Schema    `json:"items"`
	PrefixItems          []*Schema  `json:"prefixItems"`
	AdditionalProperties *Schema    `json:"additionalProperties"`
	AnyOf                []*Schema  `json:"anyOf"`
	AllOf                []*Schema  `json:"allOf"`
	OneOf                []*Schema  `json:"oneOf"`
	Enum                 []any      `json:"enum"`
	Const                any        `json:"const"`
}

// UnmarshalJSON accepts `true` and `false`, which JSON Schema allows in place of a schema.
func (s *Schema) UnmarshalJSON(b []byte) error {
	switch string(bytes.TrimSpace(b)) {
	case "true":
		*s = Schema{}
		return nil
	case "false":
		return fmt.Errorf("schema: `false` schema is not supported")
	}
	type plain Schema
	return json.Unmarshal(b, (*plain)(s))
}

// Property is one field of an object schema.
type Property struct {
	Name   string
	Schema *Schema
}

// Properties keeps object fields in declaration order.
type Properties []Property

// UnmarshalJSON decodes a JSON object while preserving key order.
func (p *Properties) UnmarshalJSON(b []byte) error {
	dec := json.NewDecoder(bytes.NewReader(b))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return fmt.Errorf("schema: properties must be an object")
	}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return err
		}
		var s Schema
		if err := dec.Decode(&s); err != nil {
			return err
		}
		*p = append(*p, Property{Name: t.(string), Schema: &s})
	}
	_, err := dec.Token()
	return err
}

// Run exports the signatures of module using python, with env set for the child.
func Run(ctx context.Context, python string, env []string, module string) (*Export, error) {
	cmd := exec.CommandContext(ctx, python, "-m", "gothon._schema", module)
	cmd.Env = env
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if i := strings.LastIndex(msg, "\n"); i >= 0 && strings.Contains(msg, "Traceback") {
			msg = msg[i+1:] // the exception line is what matters
		}
		return nil, fmt.Errorf("gothon: export schema of %q: %v: %s", module, err, msg)
	}
	var e Export
	if err := json.Unmarshal(stdout.Bytes(), &e); err != nil {
		return nil, fmt.Errorf("gothon: parse schema of %q: %w", module, err)
	}
	return &e, nil
}
