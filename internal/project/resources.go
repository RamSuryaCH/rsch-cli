// Package project discovers repository-scoped RSCH resources and project
// memory. Invalid manifests are returned as errors; they are never silently
// ignored.
package project

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type Manifest struct {
	ID            string        `json:"id"`
	Name          string        `json:"name"`
	Version       string        `json:"version"`
	APIVersion    string        `json:"apiVersion,omitempty"`
	Entrypoint    string        `json:"entrypoint,omitempty"`
	Capabilities  []string      `json:"capabilities,omitempty"`
	Role          string        `json:"role,omitempty"`
	Instructions  string        `json:"instructions,omitempty"`
	Skills        []string      `json:"skills,omitempty"`
	Tools         []string      `json:"tools,omitempty"`
	Permissions   []string      `json:"permissions,omitempty"`
	Risk          string        `json:"risk,omitempty"`
	Model         string        `json:"model,omitempty"`
	Provider      string        `json:"provider,omitempty"`
	Budget        int64         `json:"budget,omitempty"`
	Timeout       time.Duration `json:"timeout,omitempty"`
	Events        []string      `json:"events,omitempty"`
	FailurePolicy string        `json:"failurePolicy,omitempty"`
}
type Resource struct {
	Kind     string    `json:"kind"`
	Path     string    `json:"path"`
	Manifest *Manifest `json:"manifest,omitempty"`
	Content  string    `json:"content,omitempty"`
}
type Resources struct {
	Agents   []Resource `json:"agents"`
	Skills   []Resource `json:"skills"`
	Hooks    []Resource `json:"hooks"`
	Plugins  []Resource `json:"plugins"`
	Memory   []Resource `json:"memory"`
	Steering []Resource `json:"steering"`
}

func Discover(root string) (Resources, error) {
	base := filepath.Join(root, ".rsch")
	out := Resources{}
	var err error
	out.Agents, err = loadManifests(filepath.Join(base, "agents"), "agent")
	if err != nil {
		return out, err
	}
	out.Hooks, err = loadManifests(filepath.Join(base, "hooks"), "hook")
	if err != nil {
		return out, err
	}
	out.Plugins, err = loadManifests(filepath.Join(base, "plugins"), "plugin")
	if err != nil {
		return out, err
	}
	out.Skills, err = loadMarkdown(filepath.Join(base, "skills"), "skill", true)
	if err != nil {
		return out, err
	}
	out.Memory, err = loadMarkdown(filepath.Join(base, "memory"), "memory", false)
	if err != nil {
		return out, err
	}
	out.Steering, err = loadMarkdown(filepath.Join(base, "steering"), "steering", false)
	if err != nil {
		return out, err
	}
	if data, readErr := os.ReadFile(filepath.Join(root, "rsch.md")); readErr == nil {
		out.Memory = append(out.Memory, Resource{Kind: "memory", Path: "rsch.md", Content: string(data)})
	} else if !os.IsNotExist(readErr) {
		return out, readErr
	}
	return out, nil
}
func loadManifests(dir, kind string) ([]Resource, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		return []Resource{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := make([]Resource, 0)
	for _, entry := range entries {
		if entry.IsDir() || strings.ToLower(filepath.Ext(entry.Name())) != ".json" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var manifest Manifest
		if err := json.Unmarshal(data, &manifest); err != nil {
			return nil, err
		}
		if manifest.ID == "" || manifest.Name == "" || manifest.Version == "" {
			return nil, errors.New("resource manifest requires id, name, and version: " + path)
		}
		out = append(out, Resource{Kind: kind, Path: path, Manifest: &manifest})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}
func loadMarkdown(dir, kind string, recursive bool) ([]Resource, error) {
	var out []Resource
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if os.IsNotExist(walkErr) {
			return nil
		}
		if walkErr != nil {
			return walkErr
		}
		if info.IsDir() {
			if path != dir && !recursive {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.ToLower(filepath.Ext(path)) != ".md" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out = append(out, Resource{Kind: kind, Path: path, Content: string(data)})
		return nil
	})
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, err
}
