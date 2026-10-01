// Package runtime defines the abstraction every language/tool provider
// (Node.js, Java, Python, ...) implements, and a registry the CLI uses
// to look providers up by name.
package runtime

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Version identifies one installable release in a provider-defined
// granularity (e.g. "22" for Node's major-version lines).
type Version struct {
	Name string
	LTS  bool
}

// Runtime is implemented once per managed language/tool.
type Runtime interface {
	// Name is the identifier used on the command line, e.g. "node".
	Name() string
	// ListRemoteVersions returns installable versions, newest first.
	ListRemoteVersions(ctx context.Context) ([]Version, error)
	// ListInstalledVersions returns versions currently installed locally.
	ListInstalledVersions() ([]Version, error)
	// CurrentVersion returns the active version, or (nil, nil) if none.
	CurrentVersion() (*Version, error)
	// Install resolves name against the remote versions and installs it.
	Install(ctx context.Context, name string) error
	// Uninstall removes an installed version.
	Uninstall(name string) error
	// Activate makes an installed version the active one.
	Activate(name string) error

	// BinaryPath resolves the absolute path to binName inside an
	// installed version at versionDir (e.g. "<DEV_HOME>/versions/node/22").
	// Each provider owns its own on-disk layout knowledge here.
	BinaryPath(versionDir, binName string) (string, error)

	// BinDir returns the directory whose contents should be exposed on
	// PATH for an active version installed at versionDir — e.g.
	// "<versionDir>/bin" on most platforms, or versionDir itself where a
	// provider's binaries sit at the version root (Python's Windows
	// builds). dev env uses this directly to compute PATH, so tools
	// installed by a language's own package manager (npm -g,
	// pip console-scripts — not go install, which writes to
	// GOBIN/GOPATH outside any managed version's own directory, a
	// deliberate non-goal of the Go provider) are reachable on PATH
	// the moment they're installed, with no dev-side configuration.
	BinDir(versionDir string) (string, error)
}

// ValidVersionName reports an error if name could escape a provider's
// versions directory when used as a path component: empty, ".", "..",
// or containing a path separator. This is the one place that protects
// every consumer that builds a filesystem path from a provider-defined
// version identifier — currently cmd/lang.go's install/uninstall/use
// commands.
func ValidVersionName(name string) error {
	if name == "" {
		return fmt.Errorf("version name must not be empty")
	}
	if name == "." || name == ".." {
		return fmt.Errorf("invalid version name: %q", name)
	}
	if strings.ContainsAny(name, "/\\") {
		return fmt.Errorf("invalid version name: %q", name)
	}
	return nil
}

// Manager is a registry of Runtimes keyed by name. Safe for concurrent
// use.
type Manager struct {
	mu       sync.RWMutex
	runtimes map[string]Runtime
}

// NewManager returns an empty Manager.
func NewManager() *Manager {
	return &Manager{runtimes: make(map[string]Runtime)}
}

// Register adds r to the registry, keyed by r.Name().
func (m *Manager) Register(r Runtime) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runtimes[r.Name()] = r
}

// Get looks up a Runtime by name.
func (m *Manager) Get(name string) (Runtime, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	r, ok := m.runtimes[name]
	return r, ok
}

// Names returns every registered Runtime's name, sorted.
func (m *Manager) Names() []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	names := make([]string, 0, len(m.runtimes))
	for name := range m.runtimes {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
