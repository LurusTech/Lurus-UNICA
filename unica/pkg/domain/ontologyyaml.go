package domain

import (
	"bytes"
	"fmt"

	"gopkg.in/yaml.v3"
)

// ApproveAndPublish must turn an approved proposal into source YAML a person
// could have written by hand — comments intact, key order undisturbed —
// rather than re-emitting the whole document from Ontology's struct tags,
// which would lose both. yaml.Marshal(*Ontology) cannot do that: it has no
// memory of the original text. This file instead edits the parsed yaml.v3
// node tree directly, splicing in exactly the addition each YAMLEdit
// describes and leaving every other node untouched.
//
// It only ever adds a sequence entry or a mapping key. Every proposal that
// reaches RenderYAMLPatch already went through ApplyAlias, ApplyDimensionValue
// or ApplyRequires — themselves additive by construction — so there is
// nothing here that removes or rewrites a node, and no ambiguity about where
// a new one belongs: the target mapping either already exists (the change is
// declared against something a person wrote) or is created fresh at the
// point this function appends it.

// YAMLEdit is one additive change RenderYAMLPatch splices into an ontology's
// source YAML. Exactly the fields naming Kind are read; it mirrors the shape
// of a Proposal's ref/phrase/payload rather than embedding one, so this file
// does not have to know about the proposals table.
type YAMLEdit struct {
	Kind ProposalKind

	// alias
	Ref    AliasRef
	Phrase string

	// dimension_value
	Dimension string
	Value     string
	Label     string

	// requires
	Property  string
	Companion string
}

// RenderYAMLPatch applies edits, in order, to a copy of originalYAML's parsed
// node tree and returns the re-rendered document. Order matters when two
// edits touch the same sequence (two new aliases on the same ref, say): they
// are appended in the order given.
func RenderYAMLPatch(originalYAML string, edits []YAMLEdit) (string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal([]byte(originalYAML), &doc); err != nil {
		return "", fmt.Errorf("render yaml patch: parse original: %w", err)
	}
	if len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return "", fmt.Errorf("render yaml patch: original document is not a single YAML mapping")
	}
	root := doc.Content[0]

	for _, e := range edits {
		var err error
		switch e.Kind {
		case ProposalKindAlias:
			err = applyAliasEdit(root, e)
		case ProposalKindDimensionValue:
			err = applyDimensionValueEdit(root, e)
		case ProposalKindRequires:
			err = applyRequiresEdit(root, e)
		default:
			err = fmt.Errorf("unsupported edit kind %q", e.Kind)
		}
		if err != nil {
			return "", fmt.Errorf("render yaml patch: %w", err)
		}
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return "", fmt.Errorf("render yaml patch: encode: %w", err)
	}
	if err := enc.Close(); err != nil {
		return "", fmt.Errorf("render yaml patch: encode: %w", err)
	}
	return buf.String(), nil
}

// applyAliasEdit appends phrase under aliases.<ref>, creating the aliases
// mapping and the ref's own sequence when this is the first alias either has.
func applyAliasEdit(root *yaml.Node, e YAMLEdit) error {
	if e.Ref == "" || e.Phrase == "" {
		return fmt.Errorf("alias edit needs both ref and phrase")
	}
	aliases := mappingGetOrCreate(root, "aliases", yaml.MappingNode)
	seq := mappingGetOrCreate(aliases, string(e.Ref), yaml.SequenceNode)
	seq.Content = append(seq.Content, scalarNode(e.Phrase))
	return nil
}

// applyDimensionValueEdit appends value under dimensions.<dimension>.values,
// and sets dimensions.<dimension>.labels[value] when a label was given. The
// dimension itself must already be declared: ApplyDimensionValue already
// refused a proposal against an undeclared one, so reaching this function
// with one that is missing from the source YAML would mean the compiled
// ontology and its own source text have already drifted apart, which is a
// louder problem than this function can fix.
func applyDimensionValueEdit(root *yaml.Node, e YAMLEdit) error {
	if e.Dimension == "" || e.Value == "" {
		return fmt.Errorf("dimension_value edit needs both dimension and value")
	}
	dims := mappingGet(root, "dimensions")
	if dims == nil {
		return fmt.Errorf("dimension %s: source yaml declares no dimensions", e.Dimension)
	}
	dim := mappingGet(dims, e.Dimension)
	if dim == nil {
		return fmt.Errorf("dimension %s: not declared in source yaml", e.Dimension)
	}
	values := mappingGetOrCreate(dim, "values", yaml.SequenceNode)
	values.Content = append(values.Content, scalarNode(e.Value))
	if e.Label != "" {
		labels := mappingGetOrCreate(dim, "labels", yaml.MappingNode)
		mappingSet(labels, e.Value, scalarNode(e.Label))
	}
	return nil
}

// applyRequiresEdit appends companion under properties.<property>.requires.
// requires may be authored as a bare scalar (StringList accepts either
// form); a scalar is normalised to a one-item sequence before the new
// companion is appended, so the result is always a sequence a person editing
// the file afterwards can keep adding to.
func applyRequiresEdit(root *yaml.Node, e YAMLEdit) error {
	if e.Property == "" || e.Companion == "" {
		return fmt.Errorf("requires edit needs both property and companion")
	}
	props := mappingGet(root, "properties")
	if props == nil {
		return fmt.Errorf("property %s: source yaml declares no properties", e.Property)
	}
	prop := mappingGet(props, e.Property)
	if prop == nil {
		return fmt.Errorf("property %s: not declared in source yaml", e.Property)
	}
	requires := mappingGet(prop, "requires")
	switch {
	case requires == nil:
		requires = newNode(yaml.SequenceNode)
		mappingAppend(prop, "requires", requires)
	case requires.Kind == yaml.ScalarNode:
		existing := scalarNode(requires.Value)
		requires.Kind = yaml.SequenceNode
		requires.Tag = "!!seq"
		requires.Value = ""
		requires.Content = []*yaml.Node{existing}
	}
	requires.Content = append(requires.Content, scalarNode(e.Companion))
	return nil
}

// --- yaml.v3 node helpers -------------------------------------------------
//
// These touch only what a caller explicitly names. None of them iterate a Go
// map, so the key order two calls with the same edits produce is identical
// every time — the node tree is spliced directly, never rebuilt from struct
// tags, so there is no map traversal in this file for order to depend on.

func scalarNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}

func newNode(kind yaml.Kind) *yaml.Node {
	switch kind {
	case yaml.MappingNode:
		return &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	case yaml.SequenceNode:
		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
	default:
		return &yaml.Node{Kind: kind}
	}
}

// mappingGet returns m's value node for key, or nil when m is not a mapping
// or has no such key.
func mappingGet(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

// mappingAppend adds a new key: value pair at the end of m's mapping, without
// checking whether key is already present — callers only use it once
// mappingGet has confirmed it is not.
func mappingAppend(m *yaml.Node, key string, value *yaml.Node) {
	m.Content = append(m.Content, scalarNode(key), value)
}

// mappingSet sets key's value in m, updating it in place when key already
// exists (used for aliases.labels[value], where a mined proposal could in
// principle repeat a value already given a different label) and appending a
// new pair otherwise.
func mappingSet(m *yaml.Node, key string, value *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			m.Content[i+1] = value
			return
		}
	}
	mappingAppend(m, key, value)
}

// mappingGetOrCreate returns m's existing value node for key, or creates an
// empty node of kind, appends it under key, and returns that.
func mappingGetOrCreate(m *yaml.Node, key string, kind yaml.Kind) *yaml.Node {
	if v := mappingGet(m, key); v != nil {
		return v
	}
	v := newNode(kind)
	mappingAppend(m, key, v)
	return v
}
