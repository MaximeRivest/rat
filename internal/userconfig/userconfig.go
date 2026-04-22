// Package userconfig reads the user's rat preferences from
// ~/.config/rat/config.yaml (or the platform equivalent).
//
// The config is hierarchical: global defaults under `repl:` apply
// to every language; per-language sections (e.g. `python:`, `r:`)
// override those defaults. Missing file or missing fields fall back
// to built-in defaults.
//
// Example config.yaml:
//
//	repl:
//	  activity:
//	    max_code_lines: 0       # 0 = unlimited
//	    max_output_lines: 100
//	  history:
//	    seed_from_runtime: true
//	    seed_limit: 0           # 0 = unlimited
//	  python:
//	    activity:
//	      max_output_lines: 50  # tighter cap for python only
package userconfig

import (
	"fmt"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
)

// Built-in defaults. Kept in one place so Go and Python frontends
// stay in sync (Python uses the same numbers when flags are absent).
const (
	DefaultActivityMaxCodeLines   = 0   // 0 = unlimited
	DefaultActivityMaxOutputLines = 100
	DefaultHistorySeedFromRuntime = true
	DefaultHistorySeedLimit       = 0 // 0 = unlimited
)

// ReplConfig is the fully-resolved config for one REPL session.
type ReplConfig struct {
	Activity ActivityConfig
	History  HistoryConfig
}

// ActivityConfig controls how remote-client activity is rendered
// in the REPL (the dim "activity ✓" blocks above the prompt).
type ActivityConfig struct {
	// MaxCodeLines caps how many code lines are shown per activity
	// entry. 0 means unlimited.
	MaxCodeLines int
	// MaxOutputLines caps how many output lines are shown per
	// activity entry. 0 means unlimited.
	MaxOutputLines int
}

// HistoryConfig controls how the REPL seeds its up-arrow history.
type HistoryConfig struct {
	// SeedFromRuntime, when true, pre-loads every code string from
	// activity.jsonl into prompt_toolkit history so up-arrow cycles
	// through all executions in this kernel, not only the ones
	// typed locally.
	SeedFromRuntime bool
	// SeedLimit caps how many entries to seed (0 = unlimited).
	SeedLimit int
}

// fileConfig is the raw YAML structure. Every field is a pointer so
// we can distinguish "unset" from "set to zero" during merging.
type fileConfig struct {
	Repl    *section            `yaml:"repl"`
	PerLang map[string]*section `yaml:",inline"`
}

type section struct {
	Activity *activitySection `yaml:"activity"`
	History  *historySection  `yaml:"history"`
}

type activitySection struct {
	MaxCodeLines   *int `yaml:"max_code_lines"`
	MaxOutputLines *int `yaml:"max_output_lines"`
}

type historySection struct {
	SeedFromRuntime *bool `yaml:"seed_from_runtime"`
	SeedLimit       *int  `yaml:"seed_limit"`
}

// DefaultPath returns ~/.config/rat/config.yaml (or platform equivalent).
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "rat", "config.yaml"), nil
}

// Resolve reads the user config and returns the effective settings
// for the given language. It never returns an error for a missing
// file — missing file means "use defaults".
//
// lang is the canonical rat language ("py", "r", "jl", "js", "sh", …)
// or "" for the global defaults. Unknown languages fall back to the
// `repl:` section.
func Resolve(lang string) ReplConfig {
	cfg := defaults()
	path, err := DefaultPath()
	if err != nil {
		return cfg
	}
	return loadFromFile(path, lang, cfg)
}

// ResolveFromPath is Resolve but with an explicit config path
// (used by tests).
func ResolveFromPath(path, lang string) ReplConfig {
	return loadFromFile(path, lang, defaults())
}

func defaults() ReplConfig {
	return ReplConfig{
		Activity: ActivityConfig{
			MaxCodeLines:   DefaultActivityMaxCodeLines,
			MaxOutputLines: DefaultActivityMaxOutputLines,
		},
		History: HistoryConfig{
			SeedFromRuntime: DefaultHistorySeedFromRuntime,
			SeedLimit:       DefaultHistorySeedLimit,
		},
	}
}

func loadFromFile(path, lang string, cfg ReplConfig) ReplConfig {
	data, err := os.ReadFile(path)
	if err != nil {
		return cfg // missing or unreadable → defaults
	}
	var file fileConfig
	if err := yaml.Unmarshal(data, &file); err != nil {
		fmt.Fprintf(os.Stderr, "rat: ignoring %s: %v\n", path, err)
		return cfg
	}
	// Global `repl:` first, then per-language override.
	if file.Repl != nil {
		cfg = applySection(cfg, file.Repl)
	}
	if lang != "" {
		// Apply long-form aliases first (python, julia, bash, shell),
		// then the canonical name — so canonical wins if both appear.
		for alias, canon := range map[string]string{
			"python": "py",
			"julia":  "jl",
			"bash":   "sh",
			"shell":  "sh",
		} {
			if canon == lang {
				if s, ok := file.PerLang[alias]; ok && s != nil {
					cfg = applySection(cfg, s)
				}
			}
		}
		if s, ok := file.PerLang[lang]; ok && s != nil {
			cfg = applySection(cfg, s)
		}
	}
	return cfg
}

func applySection(cfg ReplConfig, s *section) ReplConfig {
	if s.Activity != nil {
		if s.Activity.MaxCodeLines != nil {
			cfg.Activity.MaxCodeLines = *s.Activity.MaxCodeLines
		}
		if s.Activity.MaxOutputLines != nil {
			cfg.Activity.MaxOutputLines = *s.Activity.MaxOutputLines
		}
	}
	if s.History != nil {
		if s.History.SeedFromRuntime != nil {
			cfg.History.SeedFromRuntime = *s.History.SeedFromRuntime
		}
		if s.History.SeedLimit != nil {
			cfg.History.SeedLimit = *s.History.SeedLimit
		}
	}
	return cfg
}
