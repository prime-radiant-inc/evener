package main

import (
	"context"
	"fmt"
	"time"

	"primeradiant.com/serf/internal/appwire"
	"primeradiant.com/serf/internal/launchconfig"
)

// hubLaunchController owns the serf/launch/* RPC handlers.
type hubLaunchController struct {
	stateRoot string
	now       func() time.Time
}

func newHubLaunchController(stateRoot string) *hubLaunchController {
	return &hubLaunchController{stateRoot: stateRoot, now: time.Now}
}

func (c *hubLaunchController) Resolve(ctx context.Context, params appwire.LaunchConfigResolveParams) (appwire.LaunchConfigResolved, error) {
	cwd, err := canonicalizeDir(params.CWD)
	if err != nil {
		return appwire.LaunchConfigResolved{}, appwire.InvalidParams("cwd: " + err.Error())
	}
	var overrides launchconfig.Layer
	if params.LaunchOverrides != nil {
		overrides = launchconfig.FromWire(*params.LaunchOverrides)
	}
	resolved, err := launchconfig.Resolve(c.stateRoot, cwd, overrides)
	if err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	return launchconfig.ResolvedToWire(resolved), nil
}

func (c *hubLaunchController) GetLayer(ctx context.Context, params appwire.LaunchConfigGetLayerParams) (appwire.LaunchConfigLayer, error) {
	cwd, err := canonicalizeDir(params.CWD)
	if err != nil {
		return appwire.LaunchConfigLayer{}, appwire.InvalidParams("cwd: " + err.Error())
	}
	paths := launchconfig.PathsFor(c.stateRoot, cwd)
	var path string
	switch params.Layer {
	case "global":
		path = paths.Global
	case "project":
		path = paths.Project
	default:
		return appwire.LaunchConfigLayer{}, appwire.InvalidParams(fmt.Sprintf("layer %q is not writable", params.Layer))
	}
	layer, err := launchconfig.LoadLayer(path)
	if err != nil {
		return appwire.LaunchConfigLayer{}, err
	}
	return launchconfig.ToWire(layer), nil
}

func (c *hubLaunchController) SetLayer(ctx context.Context, params appwire.LaunchConfigSetLayerParams) (appwire.LaunchConfigResolved, error) {
	cwd, err := canonicalizeDir(params.CWD)
	if err != nil {
		return appwire.LaunchConfigResolved{}, appwire.InvalidParams("cwd: " + err.Error())
	}
	paths := launchconfig.PathsFor(c.stateRoot, cwd)
	var path string
	switch params.Layer {
	case "global":
		path = paths.Global
	case "project":
		path = paths.Project
	default:
		return appwire.LaunchConfigResolved{}, appwire.InvalidParams(fmt.Sprintf("layer %q is not writable", params.Layer))
	}
	layer := launchconfig.FromWire(params.Config)
	// Refuse credential keys in env before persisting.
	for k := range layer.Env {
		if launchconfig.IsCredentialEnvKey(k) {
			return appwire.LaunchConfigResolved{}, appwire.InvalidParams(fmt.Sprintf("env key %q looks like a credential; route through serf/auth/apiKey/set", k))
		}
	}
	if err := launchconfig.SaveLayer(path, layer); err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	resolved, err := launchconfig.Resolve(c.stateRoot, cwd, launchconfig.Layer{})
	if err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	return launchconfig.ResolvedToWire(resolved), nil
}

func (c *hubLaunchController) TrustRepo(ctx context.Context, params appwire.LaunchConfigTrustRepoParams) (appwire.LaunchConfigResolved, error) {
	cwd, err := canonicalizeDir(params.CWD)
	if err != nil {
		return appwire.LaunchConfigResolved{}, appwire.InvalidParams("cwd: " + err.Error())
	}
	resolved, err := launchconfig.Resolve(c.stateRoot, cwd, launchconfig.Layer{})
	if err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	if resolved.Repo == nil || resolved.Repo.Trust == launchconfig.TrustAbsent {
		return appwire.LaunchConfigResolved{}, appwire.InvalidParams("no .serf/launch.toml in repo")
	}
	if resolved.Repo.Hash != params.Hash {
		return appwire.LaunchConfigResolved{}, appwire.WireError{Code: -32009, Message: "file changed since review"}
	}
	paths := launchconfig.PathsFor(c.stateRoot, cwd)
	meta, _ := launchconfig.LoadMeta(paths.Meta)
	if meta.Schema == 0 {
		meta = launchconfig.Meta{Schema: 1, CWD: cwd, CreatedAt: c.now()}
	}
	// Append the new hash to the trusted set rather than overwriting, so
	// branch-switching with different .serf/launch.toml content does not
	// require re-prompting for previously-approved hashes. Rejected hashes
	// are not part of the trusted set and must not become trusted by a later
	// trust decision for a different hash.
	var existingHashes []string
	if meta.Trust.Decision == "trusted" {
		existingHashes = launchconfig.TrustHashSet(meta.Trust)
	}
	if !launchconfig.HashInSet(params.Hash, existingHashes) {
		existingHashes = append(existingHashes, params.Hash)
	}
	meta.Trust = launchconfig.MetaTrust{
		Hashes:    existingHashes,
		Decision:  "trusted",
		DecidedAt: c.now(),
	}
	if err := launchconfig.SaveMeta(paths.Meta, meta); err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	resolved, err = launchconfig.Resolve(c.stateRoot, cwd, launchconfig.Layer{})
	if err != nil {
		return appwire.LaunchConfigResolved{}, err
	}
	return launchconfig.ResolvedToWire(resolved), nil
}
