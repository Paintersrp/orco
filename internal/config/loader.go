package config

import (
	"bufio"
	"fmt"
	"os"
	pathpkg "path"
	"path/filepath"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// Load reads a stack manifest from the provided path.
func Load(path string) (*Stack, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve stack path: %w", err)
	}

	f, err := os.Open(absPath)
	if err != nil {
		return nil, fmt.Errorf("open stack file: %w", err)
	}
	defer f.Close()

	decoder := yaml.NewDecoder(f)
	decoder.KnownFields(true)
	var doc Stack
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("%s: decode: %w", absPath, err)
	}

	stackDir := filepath.Dir(absPath)
	resolvedWorkdir := resolveWorkdir(stackDir, os.ExpandEnv(doc.Stack.Workdir))
	doc.Stack.Workdir = resolvedWorkdir

	if doc.Logging != nil {
		doc.Logging.Directory = os.ExpandEnv(doc.Logging.Directory)
		if doc.Logging.Directory != "" && !filepath.IsAbs(doc.Logging.Directory) {
			doc.Logging.Directory = filepath.Clean(filepath.Join(resolvedWorkdir, doc.Logging.Directory))
		}
	}

	for name, svc := range doc.Services {
		if svc == nil {
			continue
		}
		svc.ResolvedWorkdir = resolvedWorkdir

		var inlineEnv map[string]string
		if len(svc.Env) > 0 {
			inlineEnv = make(map[string]string, len(svc.Env))
			for k, v := range svc.Env {
				inlineEnv[k] = os.ExpandEnv(v)
			}
		}

		var fileEnv map[string]string
		if svc.EnvFromFile != "" {
			expanded := os.ExpandEnv(svc.EnvFromFile)
			if !filepath.IsAbs(expanded) {
				expanded = filepath.Clean(filepath.Join(resolvedWorkdir, expanded))
			}
			svc.EnvFromFile = expanded

			var err error
			fileEnv, err = loadEnvFile(expanded)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", serviceField(name, "envFromFile"), err)
			}
		}

		var merged map[string]string
		if len(fileEnv) > 0 {
			merged = make(map[string]string, len(fileEnv))
			for k, v := range fileEnv {
				merged[k] = v
			}
		}
		if len(inlineEnv) > 0 {
			if merged == nil {
				merged = make(map[string]string, len(inlineEnv))
			}
			for k, v := range inlineEnv {
				merged[k] = v
			}
		}
		if merged != nil {
			svc.Env = merged
		} else {
			svc.Env = nil
		}

		if len(svc.Volumes) > 0 {
			resolved := make([]string, 0, len(svc.Volumes))
			for i, volume := range svc.Volumes {
				expanded := os.ExpandEnv(volume)
				host, container, mode, err := splitVolumeSpec(expanded)
				if err != nil {
					return nil, fmt.Errorf("%s: %w", serviceField(name, fmt.Sprintf("volumes[%d]", i)), err)
				}
				if !filepath.IsAbs(host) {
					host = filepath.Join(resolvedWorkdir, host)
				}
				host = filepath.Clean(host)
				container = pathpkg.Clean(container)
				if !pathpkg.IsAbs(container) {
					return nil, fmt.Errorf("%s: container path %q must be absolute", serviceField(name, fmt.Sprintf("volumes[%d]", i)), container)
				}
				normalized := host + ":" + container
				if mode != "" {
					normalized += ":" + mode
				}
				resolved = append(resolved, normalized)
			}
			svc.Volumes = resolved
		}
	}

	if err := doc.ApplyDefaults(); err != nil {
		return nil, fmt.Errorf("%s: %w", absPath, err)
	}
	if err := doc.Validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", absPath, err)
	}
	return &doc, nil
}

func resolveWorkdir(base, workdir string) string {
	if workdir == "" {
		return base
	}
	if filepath.IsAbs(workdir) {
		return filepath.Clean(workdir)
	}
	return filepath.Clean(filepath.Join(base, workdir))
}

func loadEnvFile(path string) (map[string]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("load env file %q: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	values := make(map[string]string)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := strings.TrimSpace(scanner.Text())
		if raw == "" || strings.HasPrefix(raw, "#") {
			continue
		}
		if strings.HasPrefix(raw, "export ") {
			raw = strings.TrimSpace(raw[len("export "):])
		}
		sep := strings.IndexRune(raw, '=')
		if sep <= 0 {
			return nil, fmt.Errorf("load env file %q: invalid line %d", path, lineNo)
		}
		key := strings.TrimSpace(raw[:sep])
		if key == "" {
			return nil, fmt.Errorf("load env file %q: invalid key on line %d", path, lineNo)
		}
		value := strings.TrimSpace(raw[sep+1:])
		if strings.HasPrefix(value, "\"") || strings.HasPrefix(value, "'") {
			quote := value[0]
			end := -1
			switch quote {
			case '"':
				escaped := false
				for i := 1; i < len(value); i++ {
					c := value[i]
					if escaped {
						escaped = false
						continue
					}
					if c == '\\' {
						escaped = true
						continue
					}
					if c == quote {
						end = i
						break
					}
				}
			case '\'':
				if idx := strings.IndexByte(value[1:], quote); idx >= 0 {
					end = idx + 1
				}
			}
			if end == -1 {
				return nil, fmt.Errorf("load env file %q: unmatched quote on line %d", path, lineNo)
			}
			quoted := value[:end+1]
			remainder := strings.TrimSpace(value[end+1:])
			if comment := strings.IndexRune(remainder, '#'); comment >= 0 {
				remainder = strings.TrimSpace(remainder[:comment])
			}
			if remainder != "" {
				return nil, fmt.Errorf("load env file %q: invalid characters after quoted value on line %d", path, lineNo)
			}
			if quote == '"' {
				unquoted, err := strconv.Unquote(quoted)
				if err != nil {
					return nil, fmt.Errorf("load env file %q: parse value for %s on line %d: %w", path, key, lineNo, err)
				}
				value = unquoted
			} else {
				value = quoted[1 : len(quoted)-1]
			}
		} else if comment := strings.IndexRune(value, '#'); comment >= 0 {
			value = strings.TrimSpace(value[:comment])
		}
		values[key] = os.ExpandEnv(value)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("load env file %q: %w", path, err)
	}
	return values, nil
}
