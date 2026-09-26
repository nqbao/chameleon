package environment

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// CredDir is a credential path a provider needs from the host.
type CredDir struct {
	HomeRelPath   string // target relative to $HOME (e.g. ".claude")
	DockerRelPath string // optional: path under ~/.chameleon/docker/<agent>/ (defaults to HomeRelPath)
	IsDir         bool   // true if this is a directory mount, false if a single file
	HostPath      string // absolute path on host (resolved at runtime)
}

// CredentialProvider declares what credentials a tool needs.
// It is backend-agnostic — knows nothing about docker mounts or sandbox paths.
type CredentialProvider interface {
	Name() string
	Dirs() []CredDir
	EnvVars() []string
}

// dockerDirs derives docker mount dirs for a provider, rooted in
// DockerHome/<provider-name>/ so each agent has its own isolated subtree.
func dockerDirs(root string, p CredentialProvider) []CredDir {
	base := filepath.Join(root, p.Name())
	var dirs []CredDir
	for _, dir := range p.Dirs() {
		dockerRel := dir.DockerRelPath
		if dockerRel == "" {
			dockerRel = dir.HomeRelPath
		}
		dirs = append(dirs, CredDir{
			HostPath:    filepath.Join(base, dockerRel),
			HomeRelPath: dir.HomeRelPath,
			IsDir:       dir.IsDir,
		})
	}
	return dirs
}

// DockerContainerSetup builds docker run arguments and an init script from credential providers.
type DockerContainerSetup struct {
	DockerHome string
	Providers  []CredentialProvider
	ExtraInit  string // sh fragment appended before exec "$@" in the init script
}

// EnsureDirs creates host-side credential dirs for all providers so they can be mounted.
// Directories are created empty; files are created with "{}" if they don't exist yet.
func (d *DockerContainerSetup) EnsureDirs() error {
	if len(d.Providers) > 0 && d.DockerHome == "" {
		return fmt.Errorf("docker credential home is required")
	}
	for _, p := range d.Providers {
		for _, dir := range dockerDirs(d.DockerHome, p) {
			if dir.IsDir {
				if err := os.MkdirAll(dir.HostPath, 0700); err != nil {
					return fmt.Errorf("mkdir %s: %w", dir.HostPath, err)
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(dir.HostPath), 0700); err != nil {
					return fmt.Errorf("mkdir %s: %w", filepath.Dir(dir.HostPath), err)
				}
				if _, err := os.Stat(dir.HostPath); os.IsNotExist(err) {
					// Use {} so claude doesn't fail on a missing/invalid config file.
					if err := os.WriteFile(dir.HostPath, []byte("{}"), 0600); err != nil {
						return fmt.Errorf("create %s: %w", dir.HostPath, err)
					}
				}
			}
		}
	}
	return nil
}

// MountArgs returns -v flags for all credential dirs and the init script.
func (d *DockerContainerSetup) MountArgs(initScriptPath string) []string {
	var args []string
	if initScriptPath != "" {
		args = append(args, "-v", initScriptPath+":/cham-init.sh:ro")
	}
	seen := map[string]bool{}
	for _, p := range d.Providers {
		for _, dir := range dockerDirs(d.DockerHome, p) {
			target := "/cham-creds/" + dir.HomeRelPath
			if seen[target] {
				continue
			}
			seen[target] = true
			args = append(args, "-v", dir.HostPath+":"+target)
		}
	}
	return args
}

// InitScript generates the /bin/sh entrypoint that symlinks credentials from
// /cham-creds/ into $HOME. Symlinks mean writes (e.g. during --setup) go directly
// to the host-mounted dirs and persist automatically.
// Only symlinks when the source has content, so empty mounts don't wipe existing
// container credentials.
func (d *DockerContainerSetup) InitScript() string {
	type credLink struct {
		homeRelPath string
		isDir       bool
	}
	var links []credLink
	seen := map[string]bool{}
	for _, p := range d.Providers {
		for _, dir := range dockerDirs(d.DockerHome, p) {
			if seen[dir.HomeRelPath] {
				continue
			}
			seen[dir.HomeRelPath] = true
			links = append(links, credLink{dir.HomeRelPath, dir.IsDir})
		}
	}

	// Collect parent dirs that must exist before symlinking.
	mkdirSet := map[string]bool{}
	for _, l := range links {
		if parent := filepath.Dir(l.homeRelPath); parent != "." {
			mkdirSet[parent] = true
		}
	}
	mkdirs := make([]string, 0, len(mkdirSet))
	for p := range mkdirSet {
		mkdirs = append(mkdirs, p)
	}
	sort.Strings(mkdirs)

	var sb strings.Builder
	sb.WriteString("#!/bin/sh\n")
	if len(mkdirs) > 0 {
		sb.WriteString("mkdir -p")
		for _, p := range mkdirs {
			fmt.Fprintf(&sb, ` "$HOME/%s"`, p)
		}
		sb.WriteString("\n")
	}
	for _, l := range links {
		// Only symlink if the source has content, so empty mounts don't wipe
		// existing container credentials. Dirs always symlink since they get
		// populated at runtime; files check size.
		if l.isDir {
			fmt.Fprintf(&sb, "rm -rf \"$HOME/%s\"\n", l.homeRelPath)
			fmt.Fprintf(&sb, "ln -s \"/cham-creds/%s\" \"$HOME/%s\"\n", l.homeRelPath, l.homeRelPath)
		} else {
			fmt.Fprintf(&sb, "if [ -s \"/cham-creds/%s\" ]; then\n", l.homeRelPath)
			fmt.Fprintf(&sb, "  rm -rf \"$HOME/%s\"\n", l.homeRelPath)
			fmt.Fprintf(&sb, "  ln -s \"/cham-creds/%s\" \"$HOME/%s\"\n", l.homeRelPath, l.homeRelPath)
			fmt.Fprintf(&sb, "fi\n")
		}
	}
	// Source login profiles and nvm so PATH picks up user-installed binaries.
	sb.WriteString("[ -f /etc/profile ] && . /etc/profile\n")
	sb.WriteString("[ -f \"$HOME/.bash_profile\" ] && . \"$HOME/.bash_profile\" || [ -f \"$HOME/.bashrc\" ] && . \"$HOME/.bashrc\"\n")
	sb.WriteString("[ -s \"$HOME/.nvm/nvm.sh\" ] && . \"$HOME/.nvm/nvm.sh\"\n")
	if d.ExtraInit != "" {
		sb.WriteString(d.ExtraInit)
		if !strings.HasSuffix(d.ExtraInit, "\n") {
			sb.WriteString("\n")
		}
	}
	sb.WriteString("exec \"$@\"\n")
	return sb.String()
}
