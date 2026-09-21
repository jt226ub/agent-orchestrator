package domain

import (
	"encoding/json"
	"fmt"
	"strings"
)

// DefaultProfiles is the user-level profiles document at
// <data dir>/profiles.json: role profiles every project can name without
// declaring them. A project profile of the same name wins, so a project can
// specialise a default without losing the rest.
type DefaultProfiles struct {
	Profiles map[string]RoleProfile `json:"profiles,omitempty"`
}

// ParseDefaultProfiles decodes and validates the profiles document. An empty
// document is valid and means no defaults.
func ParseDefaultProfiles(data []byte) (DefaultProfiles, error) {
	var doc DefaultProfiles
	if strings.TrimSpace(string(data)) == "" {
		return doc, nil
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return DefaultProfiles{}, fmt.Errorf("default profiles: %w", err)
	}
	if err := doc.Validate(); err != nil {
		return DefaultProfiles{}, err
	}
	return doc, nil
}

// Validate checks every default profile the way a project's profiles are
// checked. RulesFile is relative to the data dir's rules folder here, so the
// same "no escape" rule applies with a different root.
func (d DefaultProfiles) Validate() error {
	if err := validateProfiles(d.Profiles); err != nil {
		return fmt.Errorf("default %w", err)
	}
	return nil
}

// WithDefaultProfiles adds every default profile the project does not define
// under the same name and remembers which entries came from the defaults, so
// prompt assembly can read their rules files from the data dir.
func (c ProjectConfig) WithDefaultProfiles(defaults map[string]RoleProfile) ProjectConfig {
	if len(defaults) == 0 {
		return c
	}
	profiles := make(map[string]RoleProfile, len(c.Profiles)+len(defaults))
	for name, profile := range c.Profiles {
		profiles[name] = profile
	}
	added := make(map[string]struct{}, len(defaults))
	for name := range c.defaultProfiles {
		added[name] = struct{}{}
	}
	for name, profile := range defaults {
		if _, ok := profiles[name]; ok {
			continue
		}
		profiles[name] = profile
		added[name] = struct{}{}
	}
	c.Profiles = profiles
	c.defaultProfiles = added
	return c
}

// IsDefaultProfile reports whether the named profile was added by
// WithDefaultProfiles rather than declared by the project.
func (c ProjectConfig) IsDefaultProfile(name string) bool {
	_, ok := c.defaultProfiles[strings.TrimSpace(name)]
	return ok
}

// StripDefaultProfiles removes the entries WithDefaultProfiles added, so a
// config that was merged for validation or template binding can be stored
// with only the project's own profiles.
func (c ProjectConfig) StripDefaultProfiles() ProjectConfig {
	if len(c.defaultProfiles) == 0 {
		return c
	}
	profiles := make(map[string]RoleProfile, len(c.Profiles))
	for name, profile := range c.Profiles {
		if _, added := c.defaultProfiles[name]; !added {
			profiles[name] = profile
		}
	}
	if len(profiles) == 0 {
		profiles = nil
	}
	c.Profiles = profiles
	c.defaultProfiles = nil
	return c
}
