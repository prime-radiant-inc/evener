package main

import (
	"fmt"
	"os"
	"sort"
	"sync"

	"primeradiant.com/serf/appwire"
	authopenai "primeradiant.com/serf/auth/openai"
	"primeradiant.com/serf/llm/providercfg"
)

// hubInstancesController manages provider instance CRUD: Create, Edit, Remove,
// SetDefault, and List. providers.toml on disk is the single source of truth;
// reads call LoadFile fresh and writes call WriteFile atomically.
type hubInstancesController struct {
	providersConfigPath string
	auth                *hubAuthController
	mu                  sync.Mutex
}

// List returns the current list of instances, each enriched with credential
// status from the auth controller. Results are sorted by Type, then Name.
func (c *hubInstancesController) List() appwire.InstanceListResponse {
	cfg, _, _ := providercfg.LoadFile(c.providersConfigPath)

	entries := make([]appwire.InstanceEntry, 0, len(cfg.Instances))
	for _, inst := range cfg.Instances {
		status := c.auth.instanceStatus(inst.Name, string(inst.Type))
		entry := appwire.InstanceEntry{
			Name:           inst.Name,
			Type:           string(inst.Type),
			APIStyle:       string(inst.APIStyle),
			BaseURL:        inst.BaseURL,
			IsDefault:      inst.Name == cfg.Default,
			AuthModes:      status.AuthModes,
			ActiveSource:   status.ActiveSource,
			HasStoredFile:  status.HasStoredFile,
			HasStoredOAuth: status.HasStoredOAuth,
			EnvVar:         status.EnvVar,
			StoredEmail:    status.StoredEmail,
		}
		entries = append(entries, entry)
	}

	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Type != entries[j].Type {
			return entries[i].Type < entries[j].Type
		}
		return entries[i].Name < entries[j].Name
	})

	return appwire.InstanceListResponse{
		Instances:      entries,
		AvailableTypes: providercfg.KnownTypeNames(),
	}
}

// Create adds a new provider instance to the config. It reloads the config
// from disk before mutating to avoid clobbering manual edits.
func (c *hubInstancesController) Create(params appwire.InstanceCreateParams) error {
	if err := providercfg.ValidateInstanceName(params.Name); err != nil {
		return fmt.Errorf("invalid instance name: %w", err)
	}
	if err := providercfg.ValidateType(providercfg.Type(params.Type)); err != nil {
		return fmt.Errorf("invalid type: %w", err)
	}
	if err := providercfg.ValidateAPIStyle(providercfg.Type(params.Type), providercfg.APIStyle(params.APIStyle)); err != nil {
		return fmt.Errorf("invalid api_style: %w", err)
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	cfg, err := c.reloadFromDisk()
	if err != nil {
		return err
	}

	// Reject duplicate names.
	for _, inst := range cfg.Instances {
		if inst.Name == params.Name {
			return fmt.Errorf("instance %q already exists", params.Name)
		}
	}

	inst := providercfg.InstanceConfig{
		Name:     params.Name,
		Type:     providercfg.Type(params.Type),
		APIStyle: providercfg.APIStyle(params.APIStyle),
		BaseURL:  params.BaseURL,
	}
	newCfg := cfg.Upsert(inst)

	if err := providercfg.WriteFile(c.providersConfigPath, newCfg); err != nil {
		return fmt.Errorf("write providers.toml: %w", err)
	}
	return nil
}

// Edit updates APIStyle and BaseURL for an existing instance. Type is immutable.
// Reloads from disk before mutating.
func (c *hubInstancesController) Edit(params appwire.InstanceEditParams) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg, err := c.reloadFromDisk()
	if err != nil {
		return err
	}

	var existing *providercfg.InstanceConfig
	for i := range cfg.Instances {
		if cfg.Instances[i].Name == params.Name {
			existing = &cfg.Instances[i]
			break
		}
	}
	if existing == nil {
		return fmt.Errorf("instance %q not found", params.Name)
	}

	// Type is immutable; build the updated record from the existing type.
	updated := providercfg.InstanceConfig{
		Name:     existing.Name,
		Type:     existing.Type, // immutable
		APIStyle: providercfg.APIStyle(params.APIStyle),
		BaseURL:  params.BaseURL,
		Quirks:   existing.Quirks,
	}
	if err := providercfg.ValidateAPIStyle(updated.Type, updated.APIStyle); err != nil {
		return fmt.Errorf("invalid api_style: %w", err)
	}

	newCfg := cfg.Upsert(updated)
	if err := providercfg.WriteFile(c.providersConfigPath, newCfg); err != nil {
		return fmt.Errorf("write providers.toml: %w", err)
	}
	return nil
}

// Remove deletes an instance from the config, clears its credentials and OAuth
// state, and reassigns the default if the removed instance was the default.
// Reloads from disk before mutating.
func (c *hubInstancesController) Remove(params appwire.InstanceRemoveParams) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg, err := c.reloadFromDisk()
	if err != nil {
		return err
	}

	newCfg := cfg.RemoveInstance(params.Name)

	// Reassign default when the removed instance held it.
	if cfg.Default == params.Name {
		// Pick the first remaining instance by sorted name, or "" if none left.
		newDefault := ""
		if len(newCfg.Instances) > 0 {
			names := make([]string, 0, len(newCfg.Instances))
			for _, inst := range newCfg.Instances {
				names = append(names, inst.Name)
			}
			sort.Strings(names)
			newDefault = names[0]
		}
		newCfg = newCfg.WithDefault(newDefault)
	}

	if err := providercfg.WriteFile(c.providersConfigPath, newCfg); err != nil {
		return fmt.Errorf("write providers.toml: %w", err)
	}

	// Clear stored credentials (ignore errors for missing entries).
	_ = c.auth.creds.Clear(params.Name)

	// Delete OAuth state file as best-effort: DeleteAuth already ignores
	// not-found. Any other error is logged but does not fail Remove, since
	// the instance is already gone from providers.toml.
	if _, err := authopenai.DeleteAuth(c.auth.stateDir, params.Name); err != nil {
		fmt.Fprintf(os.Stderr, "[hub] remove %s: delete OAuth state: %v\n", params.Name, err)
	}

	return nil
}

// SetDefault sets the named instance as the config default.
// Reloads from disk before mutating.
func (c *hubInstancesController) SetDefault(params appwire.InstanceSetDefaultParams) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	cfg, err := c.reloadFromDisk()
	if err != nil {
		return err
	}

	found := false
	for _, inst := range cfg.Instances {
		if inst.Name == params.Name {
			found = true
			break
		}
	}
	if !found {
		return fmt.Errorf("instance %q not found", params.Name)
	}

	newCfg := cfg.WithDefault(params.Name)
	if err := providercfg.WriteFile(c.providersConfigPath, newCfg); err != nil {
		return fmt.Errorf("write providers.toml: %w", err)
	}
	return nil
}

// reloadFromDisk reads providers.toml and returns the current config.
// When the file is absent it returns an empty Config (no instances). The
// caller must already hold c.mu.
func (c *hubInstancesController) reloadFromDisk() (providercfg.Config, error) {
	cfg, exists, err := providercfg.LoadFile(c.providersConfigPath)
	if err != nil {
		return providercfg.Config{}, fmt.Errorf("reload providers.toml: %w", err)
	}
	if !exists {
		return providercfg.Config{}, nil
	}
	return cfg, nil
}
