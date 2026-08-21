// Package stdio implements isolated command-backed MCP components.
package stdio

import (
	"runtime"
	"sort"
)

var (
	baselineNames = []string{
		"PATH", "PWD", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "LC_CTYPE", "TZ",
	}
	windowsLaunchNames = []string{"SystemRoot", "WINDIR", "ComSpec", "PATHEXT"}
)

// Environment builds a non-nil child environment from an allow-listed host
// baseline and explicit component values. Explicit values are appended last.
func Environment(lookup func(string) (string, bool), configured map[string]string) []string {
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	names := append([]string(nil), baselineNames...)
	if runtime.GOOS == "windows" {
		names = append(names, windowsLaunchNames...)
	}
	env := make([]string, 0, len(names)+len(configured))
	for _, name := range names {
		if value, ok := lookup(name); ok {
			env = append(env, name+"="+value)
		}
	}
	keys := make([]string, 0, len(configured))
	for name := range configured {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		env = append(env, name+"="+configured[name])
	}
	return env
}
