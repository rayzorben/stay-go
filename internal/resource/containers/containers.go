// Package containers implements the "containers" resource.
//
// File layout (Single Responsibility per file):
//
//	containers.go — Resource struct, constructor, Type(), shared helpers
//	knowledge.go  — GatherKnowledge (docker/podman inspect queries)
//	plan.go       — BuildPlan (desired-vs-actual diff)
//	execute.go    — Execute (run / recreate / stop+rm via docker or podman)
package containers

import (
	"os/exec"

	"github.com/rayzorben/stay-go/internal/config"
	"github.com/rayzorben/stay-go/internal/executor"
)

// Resource implements engine.Resource for Docker/Podman container management.
type Resource struct {
	cfg         *config.Config
	exec        *executor.Executor
	nodeConfigs map[string]*config.ContainerEntry // nodeID → config entry; populated in BuildPlan
}

// New creates a containers Resource.
func New(cfg *config.Config, exec *executor.Executor) *Resource {
	return &Resource{
		cfg:         cfg,
		exec:        exec,
		nodeConfigs: make(map[string]*config.ContainerEntry),
	}
}

// Type implements engine.Resource.
func (r *Resource) Type() string { return "containers" }

// ─── Shared helpers ───────────────────────────────────────────────────────────

// resolveRuntime returns the runtime binary to use for this container.
// If the entry specifies a runtime, that is used directly. Otherwise the
// first of docker/podman found in PATH is returned, falling back to "docker".
func resolveRuntime(specified string) string {
	if specified != "" {
		return specified
	}
	for _, rt := range []string{"docker", "podman"} {
		if _, err := exec.LookPath(rt); err == nil {
			return rt
		}
	}
	return "docker"
}

// containerHash returns a deterministic hash of all container config fields
// that define the desired runtime state. Depends and Level are excluded.
func containerHash(e *config.ContainerEntry, name string) string {
	return config.Hash(struct {
		Name        string
		Image       string
		Runtime     string
		Ports       []string
		Volumes     []string
		Environment []string
		EnvFile     []string
		Labels      map[string]string
		Restart     string
		NetworkMode string
		Networks    []string
		Command     []string
		Entrypoint  string
		User        string
		Privileged  bool
		CapAdd      []string
		CapDrop     []string
		Devices     []string
		DNS         []string
		ExtraHosts  []string
		Hostname    string
		Pull        string
		Sudo        bool
	}{
		name, e.Image, e.Runtime, e.Ports, e.Volumes, e.Environment,
		e.EnvFile, e.Labels, e.Restart, e.NetworkMode, e.Networks,
		e.Command, e.Entrypoint, e.User, e.Privileged, e.CapAdd,
		e.CapDrop, e.Devices, e.DNS, e.ExtraHosts, e.Hostname,
		e.Pull, e.Sudo,
	})
}

// buildRunArgs constructs the argument list for `docker/podman run`.
func buildRunArgs(e *config.ContainerEntry, name string) []string {
	args := []string{"run", "-d", "--name", name}

	for _, p := range e.Ports {
		args = append(args, "-p", p)
	}
	for _, v := range e.Volumes {
		args = append(args, "-v", v)
	}
	for _, env := range e.Environment {
		args = append(args, "-e", env)
	}
	for _, f := range e.EnvFile {
		args = append(args, "--env-file", f)
	}
	if e.NetworkMode != "" {
		args = append(args, "--network", e.NetworkMode)
	} else {
		for _, n := range e.Networks {
			args = append(args, "--network", n)
		}
	}
	if e.Restart != "" {
		args = append(args, "--restart", e.Restart)
	}
	for k, v := range e.Labels {
		args = append(args, "--label", k+"="+v)
	}
	if e.Hostname != "" {
		args = append(args, "--hostname", e.Hostname)
	}
	if e.User != "" {
		args = append(args, "--user", e.User)
	}
	if e.Privileged {
		args = append(args, "--privileged")
	}
	for _, c := range e.CapAdd {
		args = append(args, "--cap-add", c)
	}
	for _, c := range e.CapDrop {
		args = append(args, "--cap-drop", c)
	}
	for _, d := range e.Devices {
		args = append(args, "--device", d)
	}
	for _, d := range e.DNS {
		args = append(args, "--dns", d)
	}
	for _, h := range e.ExtraHosts {
		args = append(args, "--add-host", h)
	}
	if e.Entrypoint != "" {
		args = append(args, "--entrypoint", e.Entrypoint)
	}
	if e.Pull != "" && e.Pull != "missing" {
		args = append(args, "--pull", e.Pull)
	}

	args = append(args, e.Image)

	args = append(args, e.Command...)

	return args
}
