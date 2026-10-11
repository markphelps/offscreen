// Package project reads a repository's .offscreen.toml and resolves run requests.
// It does not execute commands or create jobs; the runner supplies job-specific
// guest paths and performs host preparation before copying resolved shares.
package project

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"
)

const FileName = ".offscreen.toml"

type Lane string

const (
	LaneAuto Lane = "auto"
	LaneVM   Lane = "vm"
	LaneHost Lane = "host"
)

// Contract is the validated repository contract. Root is the absolute directory
// containing the contract, including when it is a separate git worktree.
type Contract struct {
	Root      string          `toml:"-"`
	Project   string          `toml:"project"`
	Exclusive bool            `toml:"exclusive"`
	Prepare   Prepare         `toml:"prepare"`
	Guest     Guest           `toml:"guest"`
	Tasks     map[string]Task `toml:"tasks"`
}

type Prepare struct {
	Run   string   `toml:"run"`
	Share []string `toml:"share"`
}

type Guest struct {
	Setup string `toml:"setup"`
}

type Task struct {
	Lane Lane   `toml:"lane"`
	Run  string `toml:"run"`
}

// Discover locates a contract. With worktree set it must be in that exact
// directory; otherwise the search walks from start to the filesystem root.
// projectName is the CLI's <project> argument and must match the contract.
func Discover(start, worktree, projectName string) (Contract, error) {
	if projectName == "" {
		return Contract{}, errors.New("project name is required")
	}
	if worktree != "" {
		root, err := filepath.Abs(worktree)
		if err != nil {
			return Contract{}, fmt.Errorf("resolve worktree %q: %w", worktree, err)
		}
		cfg, err := LoadFile(filepath.Join(root, FileName))
		if errors.Is(err, fs.ErrNotExist) {
			return Contract{}, fmt.Errorf("worktree %q has no %s: %w", root, FileName, err)
		}
		if err != nil {
			return Contract{}, err
		}
		return matchProject(cfg, projectName)
	}
	if start == "" {
		var err error
		start, err = os.Getwd()
		if err != nil {
			return Contract{}, fmt.Errorf("get current directory: %w", err)
		}
	}
	root, err := filepath.Abs(start)
	if err != nil {
		return Contract{}, fmt.Errorf("resolve start directory %q: %w", start, err)
	}
	for {
		cfg, err := LoadFile(filepath.Join(root, FileName))
		if err == nil {
			return matchProject(cfg, projectName)
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return Contract{}, err
		}
		parent := filepath.Dir(root)
		if parent == root {
			return Contract{}, fmt.Errorf("not inside an offscreen project: no %s found above %q", FileName, start)
		}
		root = parent
	}
}

func matchProject(cfg Contract, name string) (Contract, error) {
	if cfg.Project != name {
		return Contract{}, fmt.Errorf("project %q does not match %s project %q", name, FileName, cfg.Project)
	}
	return cfg, nil
}

// LoadFile parses one contract, rejects unknown TOML keys and invalid task
// definitions. A missing file returns an error wrapping fs.ErrNotExist.
func LoadFile(path string) (Contract, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return Contract{}, fmt.Errorf("resolve contract %q: %w", path, err)
	}
	data, err := os.ReadFile(absolute)
	if err != nil {
		return Contract{}, fmt.Errorf("read contract %q: %w", absolute, err)
	}
	var cfg Contract
	meta, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Contract{}, fmt.Errorf("decode contract %q: %w", absolute, err)
	}
	if keys := meta.Undecoded(); len(keys) > 0 {
		names := make([]string, 0, len(keys))
		for _, key := range keys {
			names = append(names, key.String())
		}
		slices.Sort(names)
		return Contract{}, fmt.Errorf("unknown %s keys: %s", FileName, strings.Join(names, ", "))
	}
	if strings.TrimSpace(cfg.Project) == "" {
		return Contract{}, errors.New("missing project name in .offscreen.toml")
	}
	for name, task := range cfg.Tasks {
		if strings.TrimSpace(name) == "" {
			return Contract{}, errors.New("task name cannot be empty")
		}
		if task.Lane != LaneVM && task.Lane != LaneHost {
			return Contract{}, fmt.Errorf("task %q: invalid lane %q (valid: host, vm)", name, task.Lane)
		}
		if strings.TrimSpace(task.Run) == "" {
			return Contract{}, fmt.Errorf("task %q: run must be a nonempty shell command", name)
		}
	}
	for _, share := range cfg.Prepare.Share {
		if strings.TrimSpace(share) == "" {
			return Contract{}, errors.New("prepare.share entries must be nonempty paths")
		}
	}
	cfg.Root = filepath.Dir(absolute)
	return cfg, nil
}

// Command preserves the distinction between config shell scripts and a direct
// command. A runner executes Shell with a shell (-c) or Argv directly, never
// by joining or quoting Argv into a shell string. Exactly one is set.
type Command struct {
	Shell string
	Argv  []string
}

// RunRequest is one named task or a direct argv (everything after --).
// HostEnv is the host environment at submission time for expanding share paths;
// it is explicit so expansion is deterministic. JobID, OutDir and CacheDir are
// supplied by the runner: OutDir and CacheDir are paths visible to the running
// command (guest paths for a VM job). No filesystem existence check is made on
// shares here: the runner copies them after Prepare.Run completes.
type RunRequest struct {
	Task     string
	Argv     []string
	Lane     Lane
	JobID    string
	OutDir   string
	CacheDir string
	HostEnv  map[string]string
}

// JobSpec is a fully resolved, transportable job contract. Env is an overlay
// containing the four OFFSCREEN variables; the runner merges it with the
// command's regular environment. GuestSetup runs only in VM jobs, before
// Command. Shares are absolute host paths, relative shares resolved against Root.
type JobSpec struct {
	Root       string
	Project    string
	Command    Command
	Lane       Lane
	Prepare    string
	Shares     []string
	GuestSetup string
	Exclusive  bool
	Env        map[string]string
}

// Resolve validates a run request, selects a lane and expands host shares.
func Resolve(cfg Contract, req RunRequest) (JobSpec, error) {
	if req.Lane != "" && req.Lane != LaneAuto && req.Lane != LaneVM && req.Lane != LaneHost {
		return JobSpec{}, fmt.Errorf("invalid lane %q (valid: auto, host, vm)", req.Lane)
	}
	if req.Task == "" && len(req.Argv) == 0 || req.Task != "" && len(req.Argv) != 0 {
		return JobSpec{}, errors.New("supply exactly one task or -- followed by a command")
	}
	if req.JobID == "" || req.OutDir == "" || req.CacheDir == "" {
		return JobSpec{}, errors.New("job ID, output directory and cache directory are required")
	}
	lane := req.Lane
	if lane == "" || lane == LaneAuto {
		lane = LaneVM
	}
	var command Command
	if req.Task != "" {
		task, ok := cfg.Tasks[req.Task]
		if !ok {
			names := make([]string, 0, len(cfg.Tasks))
			for name := range cfg.Tasks {
				names = append(names, name)
			}
			slices.Sort(names)
			if len(names) == 0 {
				return JobSpec{}, fmt.Errorf("unknown task %q (no tasks configured)", req.Task)
			}
			return JobSpec{}, fmt.Errorf("unknown task %q (valid: %s)", req.Task, strings.Join(names, ", "))
		}
		command.Shell = task.Run
		if req.Lane == "" || req.Lane == LaneAuto {
			lane = task.Lane
		}
	} else {
		if req.Argv[0] == "" {
			return JobSpec{}, errors.New("direct command executable cannot be empty")
		}
		command.Argv = slices.Clone(req.Argv)
	}
	env := map[string]string{
		"OFFSCREEN_OUT":   req.OutDir,
		"OFFSCREEN_JOB":   req.JobID,
		"OFFSCREEN_LANE":  string(lane),
		"OFFSCREEN_CACHE": req.CacheDir,
	}
	shares := make([]string, 0, len(cfg.Prepare.Share))
	for _, share := range cfg.Prepare.Share {
		var missing string
		expanded := os.Expand(share, func(name string) string {
			value, ok := req.HostEnv[name]
			if !ok && missing == "" {
				missing = name
			}
			return value
		})
		if missing != "" {
			return JobSpec{}, fmt.Errorf("prepare.share %q: unset host variable %q", share, missing)
		}
		if strings.TrimSpace(expanded) == "" {
			return JobSpec{}, fmt.Errorf("prepare.share %q expands to an empty path", share)
		}
		if !filepath.IsAbs(expanded) {
			expanded = filepath.Join(cfg.Root, expanded)
		}
		shares = append(shares, filepath.Clean(expanded))
	}
	return JobSpec{
		Root: cfg.Root, Project: cfg.Project, Command: command, Lane: lane,
		Prepare: cfg.Prepare.Run, Shares: shares, GuestSetup: cfg.Guest.Setup,
		Exclusive: cfg.Exclusive, Env: env,
	}, nil
}
