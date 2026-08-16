// Package workspace manages the mtunes workspace directory: the user-chosen
// home of config, profiles, recipes, catalogs, and lockfiles. The workspace
// IS the library definition, so it has no hidden default location — it must
// be given explicitly (--workspace or MTUNES_WORKSPACE) so the user always
// knows where it lives and can back it up / version it.
package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
)

const EnvVar = "MTUNES_WORKSPACE"

var subdirs = []string{"devices", "storage", "views", "locks", "catalog", filepath.Join("cache", "objects")}

type Config struct {
	Locations []LocationConfig `toml:"locations"`
}

type LocationConfig struct {
	Name string `toml:"name"`
	Type string `toml:"type"` // "local" | "ssh"
	Root string `toml:"root"`
	Host string `toml:"host,omitempty"` // ssh only; resolved via ~/.ssh/config
}

type Workspace struct {
	Root   string
	Config Config
}

// Resolve determines the workspace root from the --workspace flag or the
// environment. It does not require the directory to exist (Init creates it).
func Resolve(flag string) (string, error) {
	root := flag
	if root == "" {
		root = os.Getenv(EnvVar)
	}
	if root == "" {
		return "", fmt.Errorf("no workspace: pass --workspace or set %s (create one with `mtunes init <dir>`)", EnvVar)
	}
	return ExpandUser(root)
}

// Init scaffolds a workspace at dir. Safe to run on an existing workspace
// (missing pieces are filled in, nothing is overwritten).
func Init(dir string) (*Workspace, error) {
	dir, err := ExpandUser(dir)
	if err != nil {
		return nil, err
	}
	for _, sub := range subdirs {
		if err := os.MkdirAll(filepath.Join(dir, sub), 0o755); err != nil {
			return nil, err
		}
	}
	cfgPath := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(cfgPath); os.IsNotExist(err) {
		header := "# mtunes workspace config. Locations are added with `mtunes location add`.\n"
		if err := os.WriteFile(cfgPath, []byte(header), 0o644); err != nil {
			return nil, err
		}
	}
	if err := writeTemplates(dir); err != nil {
		return nil, err
	}
	giPath := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(giPath); os.IsNotExist(err) {
		gi := "# The cache is content-addressed and disposable; never version it.\ncache/\n" +
			"# The catalog is regenerable by `mtunes scan`, but versioning it gives you\n" +
			"# history of your source library's state. Uncomment to exclude:\n# catalog/\n"
		if err := os.WriteFile(giPath, []byte(gi), 0o644); err != nil {
			return nil, err
		}
	}
	// A workspace is meant to be synced/versioned across machines; pin LF so
	// core.autocrlf on a Windows checkout never churns the catalog or locks.
	gaPath := filepath.Join(dir, ".gitattributes")
	if _, err := os.Stat(gaPath); os.IsNotExist(err) {
		ga := "# mtunes writes LF everywhere; keep it that way on every OS.\n* text=auto eol=lf\n"
		if err := os.WriteFile(gaPath, []byte(ga), 0o644); err != nil {
			return nil, err
		}
	}
	return Load(dir)
}

// Load reads an existing workspace.
func Load(root string) (*Workspace, error) {
	root, err := ExpandUser(root)
	if err != nil {
		return nil, err
	}
	cfgPath := filepath.Join(root, "config.toml")
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%s is not a workspace (no config.toml; create one with `mtunes init`)", root)
		}
		return nil, err
	}
	var cfg Config
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", cfgPath, err)
	}
	return &Workspace{Root: root, Config: cfg}, nil
}

func (w *Workspace) SaveConfig() error {
	var sb strings.Builder
	sb.WriteString("# mtunes workspace config. Locations are added with `mtunes location add`.\n")
	if err := toml.NewEncoder(&sb).Encode(w.Config); err != nil {
		return err
	}
	return writeFileAtomic(filepath.Join(w.Root, "config.toml"), []byte(sb.String()))
}

func (w *Workspace) CatalogPath(location string) string {
	return filepath.Join(w.Root, "catalog", location+".jsonl")
}

func (w *Workspace) Location(name string) (LocationConfig, bool) {
	for _, lc := range w.Config.Locations {
		if lc.Name == name {
			return lc, true
		}
	}
	return LocationConfig{}, false
}

func ExpandUser(path string) (string, error) {
	// Accept both "~/x" and, on Windows, "~\x".
	if path == "~" || strings.HasPrefix(path, "~/") || strings.HasPrefix(path, "~"+string(filepath.Separator)) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, strings.TrimPrefix(path, "~")), nil
	}
	return path, nil
}

func writeFileAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
