package config

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

func resolveIncludes(path string) (map[string]any, []string, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, fmt.Errorf("resolve include path: %w", err)
	}

	resolver := includeResolver{}
	doc, includes, err := resolver.resolve(absPath, nil, true)
	if err != nil {
		return nil, nil, err
	}
	if len(includes) > 0 {
		doc["includes"] = append([]string(nil), includes...)
	}
	return doc, includes, nil
}

type includeResolver struct{}

func (includeResolver) resolve(path string, stack []string, root bool) (map[string]any, []string, error) {
	if containsPath(stack, path) {
		cycle := append(append([]string{}, stack...), path)
		return nil, nil, fmt.Errorf("detected include cycle: %s", strings.Join(cycle, " -> "))
	}

	stack = append(stack, path)
	doc, includes, err := loadIncludeDocument(path, root)
	if err != nil {
		return nil, nil, err
	}

	merged := make(map[string]any)
	for _, includeRef := range includes {
		includePath, err := resolveIncludePath(path, includeRef)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: include %q: %w", path, includeRef, err)
		}
		if idx := indexOfPath(stack, includePath); idx >= 0 {
			cycle := append(append([]string{}, stack[idx:]...), includePath)
			return nil, nil, fmt.Errorf("detected include cycle: %s", strings.Join(cycle, " -> "))
		}
		childDoc, _, err := includeResolver{}.resolve(includePath, stack, false)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: include %q: %w", path, includeRef, err)
		}
		merged = mergeYAMLMaps(merged, childDoc)
	}

	merged = mergeYAMLMaps(merged, doc)
	return merged, includes, nil
}

func loadIncludeDocument(path string, root bool) (map[string]any, []string, error) {
	f, err := os.Open(path)
	if err != nil {
		if root {
			return nil, nil, fmt.Errorf("open stack file: %w", err)
		}
		return nil, nil, fmt.Errorf("open include file: %w", err)
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	var raw map[string]any
	if err := decoder.Decode(&raw); err != nil {
		return nil, nil, fmt.Errorf("%s: decode: %w", path, err)
	}
	if raw == nil {
		raw = make(map[string]any)
	}

	includes, err := extractIncludes(path, raw)
	if err != nil {
		return nil, nil, err
	}
	delete(raw, "includes")

	expandYAMLValues(raw)

	return raw, includes, nil
}

func extractIncludes(path string, raw map[string]any) ([]string, error) {
	includesValue, ok := raw["includes"]
	if !ok || includesValue == nil {
		return nil, nil
	}

	switch v := includesValue.(type) {
	case []string:
		if len(v) == 0 {
			return nil, nil
		}
		expanded := make([]string, len(v))
		for i, include := range v {
			expanded[i] = expandEnvWithDefault(include)
		}
		return expanded, nil
	case []any:
		includes := make([]string, len(v))
		for i, entry := range v {
			s, ok := entry.(string)
			if !ok {
				return nil, fmt.Errorf("%s: includes[%d] must be a string", path, i)
			}
			includes[i] = expandEnvWithDefault(s)
		}
		if len(includes) == 0 {
			return nil, nil
		}
		return includes, nil
	default:
		return nil, fmt.Errorf("%s: includes must be a list of strings", path)
	}
}

func resolveIncludePath(parent, includeRef string) (string, error) {
	if strings.TrimSpace(includeRef) == "" {
		return "", fmt.Errorf("include path is empty")
	}
	if looksLikeURL(includeRef) {
		return "", fmt.Errorf("remote include %q is not supported", includeRef)
	}

	includePath := includeRef
	if !filepath.IsAbs(includeRef) {
		includePath = filepath.Join(filepath.Dir(parent), includeRef)
	}
	abs, err := filepath.Abs(includePath)
	if err != nil {
		return "", fmt.Errorf("resolve include path: %w", err)
	}
	return abs, nil
}

func looksLikeURL(path string) bool {
	if strings.Contains(path, "://") {
		if u, err := url.Parse(path); err == nil && u.Scheme != "" {
			return true
		}
	}
	return false
}

func mergeYAMLMaps(dst, src map[string]any) map[string]any {
	if dst == nil {
		dst = make(map[string]any, len(src))
	}
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, key := range keys {
		srcVal := src[key]
		if srcMap, ok := toStringMap(srcVal); ok {
			dstVal, _ := toStringMap(dst[key])
			dst[key] = mergeYAMLMaps(dstVal, srcMap)
			continue
		}
		dst[key] = cloneValue(srcVal)
	}
	return dst
}

func toStringMap(value any) (map[string]any, bool) {
	switch typed := value.(type) {
	case map[string]any:
		return typed, true
	case map[any]any:
		converted := make(map[string]any, len(typed))
		for k, v := range typed {
			ks, ok := k.(string)
			if !ok {
				return nil, false
			}
			converted[ks] = v
		}
		return converted, true
	default:
		return nil, false
	}
}

func cloneValue(value any) any {
	if m, ok := toStringMap(value); ok {
		return cloneMap(m)
	}
	switch typed := value.(type) {
	case []any:
		cloned := make([]any, len(typed))
		for i, v := range typed {
			cloned[i] = cloneValue(v)
		}
		return cloned
	case []string:
		return append([]string(nil), typed...)
	default:
		return typed
	}
}

func cloneMap(src map[string]any) map[string]any {
	keys := make([]string, 0, len(src))
	for k := range src {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	cloned := make(map[string]any, len(src))
	for _, key := range keys {
		cloned[key] = cloneValue(src[key])
	}
	return cloned
}

func expandYAMLValues(doc map[string]any) {
	for key, value := range doc {
		doc[key] = expandValueRecursive(value)
	}
}

func expandValueRecursive(value any) any {
	if m, ok := toStringMap(value); ok {
		expandYAMLValues(m)
		return m
	}
	switch typed := value.(type) {
	case []any:
		for i, elem := range typed {
			typed[i] = expandValueRecursive(elem)
		}
		return typed
	case []string:
		expanded := make([]string, len(typed))
		for i, elem := range typed {
			expanded[i] = expandEnvWithDefault(elem)
		}
		return expanded
	case string:
		return expandEnvWithDefault(typed)
	default:
		return value
	}
}

func containsPath(paths []string, target string) bool {
	for _, p := range paths {
		if p == target {
			return true
		}
	}
	return false
}

func indexOfPath(paths []string, target string) int {
	for i, p := range paths {
		if p == target {
			return i
		}
	}
	return -1
}
