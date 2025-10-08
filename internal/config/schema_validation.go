package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"

	orcoschema "github.com/Paintersrp/orco/schema"
	"github.com/santhosh-tekuri/jsonschema/v5"
)

var (
	schemaOnce  sync.Once
	stackSchema *jsonschema.Schema
	schemaErr   error
)

func loadStackSchema() (*jsonschema.Schema, error) {
	schemaOnce.Do(func() {
		compiler := jsonschema.NewCompiler()
		compiler.Draft = jsonschema.Draft2020
		if err := compiler.AddResource("stack.v1.json", bytes.NewReader(orcoschema.StackV1Schema)); err != nil {
			schemaErr = fmt.Errorf("add stack schema resource: %w", err)
			return
		}
		stackSchema, schemaErr = compiler.Compile("stack.v1.json")
		if schemaErr != nil {
			schemaErr = fmt.Errorf("compile stack schema: %w", schemaErr)
		}
	})
	if schemaErr != nil {
		return nil, schemaErr
	}
	return stackSchema, nil
}

func validateAgainstSchema(doc map[string]any) error {
	schema, err := loadStackSchema()
	if err != nil {
		return fmt.Errorf("load stack schema: %w", err)
	}

	normalized, err := normalizeForSchema(doc)
	if err != nil {
		return fmt.Errorf("prepare manifest for schema validation: %w", err)
	}

	if err := schema.Validate(normalized); err != nil {
		if vErr, ok := err.(*jsonschema.ValidationError); ok {
			return fmt.Errorf("schema validation failed:\n%s", formatValidationError(vErr))
		}
		return fmt.Errorf("schema validation failed: %w", err)
	}
	return nil
}

func normalizeForSchema(doc map[string]any) (any, error) {
	buf := &bytes.Buffer{}
	encoder := json.NewEncoder(buf)
	if err := encoder.Encode(doc); err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(buf.Bytes()))
	decoder.UseNumber()
	var out any
	if err := decoder.Decode(&out); err != nil {
		return nil, err
	}
	return out, nil
}

func formatValidationError(err *jsonschema.ValidationError) string {
	var b strings.Builder
	writeValidationError(&b, err, 0)
	return strings.TrimRight(b.String(), "\n")
}

func writeValidationError(b *strings.Builder, err *jsonschema.ValidationError, depth int) {
	show := true
	if len(err.Causes) > 0 && strings.HasPrefix(err.Message, "doesn't validate with") {
		show = false
	}
	if show {
		indent := strings.Repeat("  ", depth)
		location := formatInstanceLocation(err.InstanceLocation)
		fmt.Fprintf(b, "%s- %s: %s\n", indent, location, err.Message)
		depth++
	}
	for _, cause := range err.Causes {
		writeValidationError(b, cause, depth)
	}
}

func formatInstanceLocation(ptr string) string {
	if ptr == "" || ptr == "/" {
		return "manifest"
	}
	segments := strings.Split(ptr, "/")
	if len(segments) > 0 {
		segments = segments[1:]
	}
	if len(segments) == 0 {
		return "manifest"
	}
	var b strings.Builder
	for _, segment := range segments {
		decoded := strings.ReplaceAll(strings.ReplaceAll(segment, "~1", "/"), "~0", "~")
		if _, err := strconv.Atoi(decoded); err == nil {
			fmt.Fprintf(&b, "[%s]", decoded)
			continue
		}
		if b.Len() > 0 {
			b.WriteByte('.')
		}
		b.WriteString(decoded)
	}
	if b.Len() == 0 {
		return "manifest"
	}
	return b.String()
}
