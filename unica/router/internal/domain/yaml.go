package domain

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// StringList accepts either a scalar or a sequence in YAML and normalises both
// to a slice, so a single-valued property and a multi-valued one can sit side by
// side in the same assertion block:
//
//	warranty_months: 12
//	return_precondition: [unopened, receipt]
//
// Normalising at parse time is what lets the functional-property check be a
// simple length test everywhere downstream.
type StringList []string

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *StringList) UnmarshalYAML(value *yaml.Node) error {
	switch value.Kind {
	case yaml.ScalarNode:
		*s = StringList{value.Value}
		return nil
	case yaml.SequenceNode:
		out := make(StringList, 0, len(value.Content))
		for _, node := range value.Content {
			if node.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: assertion values must be scalars", node.Line)
			}
			out = append(out, node.Value)
		}
		*s = out
		return nil
	case yaml.AliasNode:
		return s.UnmarshalYAML(value.Alias)
	default:
		return fmt.Errorf("line %d: expected a scalar or a sequence", value.Line)
	}
}

// ParseYAML decodes and validates one ontology document.
//
// Unknown fields are rejected: a mistyped constraint key would otherwise be
// silently dropped, producing an ontology that looks stricter than it is.
func ParseYAML(data []byte) (*Ontology, error) {
	var o Ontology

	dec := yaml.NewDecoder(strings.NewReader(string(data)))
	dec.KnownFields(true)
	if err := dec.Decode(&o); err != nil {
		return nil, fmt.Errorf("parse ontology: %w", err)
	}

	if err := o.Validate(); err != nil {
		return nil, err
	}
	return &o, nil
}

// LoadYAMLFile reads one ontology file from disk.
func LoadYAMLFile(path string) (*Ontology, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	o, err := ParseYAML(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return o, nil
}

// LoadYAMLDir reads every *.yaml file in dir, sorted by product line. Product
// line names must be unique: two ontologies claiming the same line would make
// which one is authoritative depend on filesystem order.
func LoadYAMLDir(dir string) ([]*Ontology, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return nil, fmt.Errorf("glob %s: %w", dir, err)
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no ontology files found in %s", dir)
	}
	sort.Strings(paths)

	seen := make(map[string]string, len(paths))
	out := make([]*Ontology, 0, len(paths))
	for _, path := range paths {
		o, err := LoadYAMLFile(path)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[o.ProductLine]; dup {
			return nil, fmt.Errorf("%s: product line %q already defined in %s", path, o.ProductLine, prev)
		}
		seen[o.ProductLine] = path
		out = append(out, o)
	}

	sort.Slice(out, func(i, j int) bool { return out[i].ProductLine < out[j].ProductLine })
	return out, nil
}

// Compile serialises an ontology for the compiled column of ontology_versions.
// The router loads this form, so it must round-trip losslessly.
func (o *Ontology) Compile() ([]byte, error) {
	data, err := json.Marshal(o)
	if err != nil {
		return nil, fmt.Errorf("compile ontology %s: %w", o.ProductLine, err)
	}
	return data, nil
}

// Decompile is the inverse of Compile, re-validating on the way in so a row
// written by an older schema cannot put the router into an inconsistent state.
func Decompile(data []byte) (*Ontology, error) {
	var o Ontology
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("decompile ontology: %w", err)
	}
	if err := o.Validate(); err != nil {
		return nil, err
	}
	return &o, nil
}
