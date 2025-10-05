package cli

import (
	"fmt"
	"sort"
	"strings"

	"github.com/Paintersrp/orco/internal/stack"
)

type serviceDiff struct {
	Name    string
	Changes []string
}

func diffStackServices(oldSpec, newSpec map[string]*stack.Service) (diffs []serviceDiff, updateTargets []string, unsupported []string) {
	if len(oldSpec) == 0 && len(newSpec) == 0 {
		return nil, nil, nil
	}
	names := make(map[string]struct{}, len(oldSpec)+len(newSpec))
	for name := range oldSpec {
		names[name] = struct{}{}
	}
	for name := range newSpec {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for name := range names {
		ordered = append(ordered, name)
	}
	sort.Strings(ordered)

	for _, name := range ordered {
		oldSvc := oldSpec[name]
		newSvc := newSpec[name]
		switch {
		case oldSvc == nil && newSvc == nil:
			continue
		case oldSvc == nil:
			diffs = append(diffs, serviceDiff{Name: name, Changes: []string{"Service added."}})
			unsupported = append(unsupported, name)
		case newSvc == nil:
			diffs = append(diffs, serviceDiff{Name: name, Changes: []string{"Service removed."}})
			unsupported = append(unsupported, name)
		default:
			changes := diffServiceFields(oldSvc, newSvc)
			if len(changes) == 0 {
				continue
			}
			diffs = append(diffs, serviceDiff{Name: name, Changes: changes})
			updateTargets = append(updateTargets, name)
		}
	}
	return diffs, updateTargets, unsupported
}

func diffServiceFields(oldSvc, newSvc *stack.Service) []string {
	if oldSvc == nil || newSvc == nil {
		return nil
	}
	var changes []string
	if strings.TrimSpace(oldSvc.Image) != strings.TrimSpace(newSvc.Image) {
		changes = append(changes, fmt.Sprintf("Image: %s -> %s", valueOrPlaceholder(oldSvc.Image), valueOrPlaceholder(newSvc.Image)))
	}
	if !equalCommands(oldSvc.Command, newSvc.Command) {
		changes = append(changes, fmt.Sprintf("Command: %s -> %s", formatCommand(oldSvc.Command), formatCommand(newSvc.Command)))
	}
	if envLines := diffEnv(oldSvc.Env, newSvc.Env); len(envLines) > 0 {
		changes = append(changes, "Env:")
		for _, line := range envLines {
			changes = append(changes, "  "+line)
		}
	}
	return changes
}

func equalCommands(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func formatCommand(cmd []string) string {
	if len(cmd) == 0 {
		return "[]"
	}
	quoted := make([]string, len(cmd))
	for i, arg := range cmd {
		quoted[i] = fmt.Sprintf("%q", arg)
	}
	return "[" + strings.Join(quoted, ", ") + "]"
}

func valueOrPlaceholder(v string) string {
	if strings.TrimSpace(v) == "" {
		return "<empty>"
	}
	return v
}

func diffEnv(oldEnv, newEnv map[string]string) []string {
	if len(oldEnv) == 0 && len(newEnv) == 0 {
		return nil
	}
	keys := make(map[string]struct{}, len(oldEnv)+len(newEnv))
	for k := range oldEnv {
		keys[k] = struct{}{}
	}
	for k := range newEnv {
		keys[k] = struct{}{}
	}
	ordered := make([]string, 0, len(keys))
	for k := range keys {
		ordered = append(ordered, k)
	}
	sort.Strings(ordered)

	var lines []string
	for _, key := range ordered {
		oldVal, oldOK := oldEnv[key]
		newVal, newOK := newEnv[key]
		switch {
		case !oldOK && newOK:
			lines = append(lines, fmt.Sprintf("+ %s=%s", key, newVal))
		case oldOK && !newOK:
			lines = append(lines, fmt.Sprintf("- %s", key))
		case oldVal != newVal:
			lines = append(lines, fmt.Sprintf("~ %s: %s -> %s", key, oldVal, newVal))
		}
	}
	return lines
}

func formatServiceDiffs(diffs []serviceDiff) string {
	if len(diffs) == 0 {
		return "No changes detected."
	}
	var b strings.Builder
	for i, diff := range diffs {
		fmt.Fprintf(&b, "Service %s:\n", diff.Name)
		for _, line := range diff.Changes {
			fmt.Fprintf(&b, "  %s\n", line)
		}
		if i != len(diffs)-1 {
			b.WriteByte('\n')
		}
	}
	return strings.TrimRight(b.String(), "\n")
}
