// Package profiles owns the user-level role profiles and the daemon-wide
// rules files: the defaults every project can name without declaring them,
// and the Markdown that every session's standing rules are assembled from.
//
// Both live as plain files under the data dir (profiles.json and rules/*.md)
// so an installer, a script or an editor can write them as well as the
// desktop app; the session manager reads the same files at spawn.
package profiles

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/aoagents/agent-orchestrator/backend/internal/domain"
	"github.com/aoagents/agent-orchestrator/backend/internal/httpd/apierr"
	sessionmanager "github.com/aoagents/agent-orchestrator/backend/internal/session_manager"
)

const (
	rulesFileMaxBytes = 1 << 20
)

// The fixed rules files every session may read; profile rules files are
// named by the profile.
const (
	// ContractRulesFile is the operating contract every session starts with.
	ContractRulesFile = "contract.md"
	// OrchestratorRulesFile follows the contract for orchestrator sessions.
	OrchestratorRulesFile = "orchestrator.md"
	// WorkerRulesFile follows the contract for worker sessions.
	WorkerRulesFile = "worker.md"
)

var rulesFileNamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*\.md$`)

// RulesFileInfo describes one rules file without its content.
type RulesFileInfo struct {
	Name      string
	SizeBytes int64
	UpdatedAt time.Time
}

// RulesFile is one rules file with its content.
type RulesFile struct {
	Name      string
	Content   string
	UpdatedAt time.Time
}

// Snapshot is the default profiles document plus the rules files on disk.
type Snapshot struct {
	Profiles   map[string]domain.RoleProfile
	RulesFiles []RulesFileInfo
}

// Service reads and writes the profiles document and the rules files.
type Service struct {
	dataDir string
	now     func() time.Time
}

// New builds the service over one data dir.
func New(dataDir string, now func() time.Time) *Service {
	if now == nil {
		now = func() time.Time { return time.Now().UTC() }
	}
	return &Service{dataDir: dataDir, now: now}
}

// Get returns the default profiles and the rules files present on disk.
func (s *Service) Get(ctx context.Context) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	profiles, err := s.readProfiles()
	if err != nil {
		return Snapshot{}, err
	}
	files, err := s.listRules()
	if err != nil {
		return Snapshot{}, err
	}
	return Snapshot{Profiles: profiles, RulesFiles: files}, nil
}

// SetProfiles replaces the default profiles document after validating it the
// way a project's profiles are validated.
func (s *Service) SetProfiles(ctx context.Context, profiles map[string]domain.RoleProfile) (Snapshot, error) {
	if err := ctx.Err(); err != nil {
		return Snapshot{}, err
	}
	doc := domain.DefaultProfiles{Profiles: profiles}
	if err := doc.Validate(); err != nil {
		return Snapshot{}, apierr.Invalid("INVALID_DEFAULT_PROFILES", err.Error(), nil)
	}
	if err := os.MkdirAll(s.dataDir, 0o700); err != nil {
		return Snapshot{}, fmt.Errorf("default profiles: %w", err)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return Snapshot{}, fmt.Errorf("default profiles: %w", err)
	}
	if err := writeFileAtomic(sessionmanager.DefaultProfilesPath(s.dataDir), append(data, '\n')); err != nil {
		return Snapshot{}, fmt.Errorf("default profiles: %w", err)
	}
	return s.Get(ctx)
}

// ReadRules returns one rules file. A missing file is a not-found error so
// the client can offer to create it.
func (s *Service) ReadRules(ctx context.Context, name string) (RulesFile, error) {
	if err := ctx.Err(); err != nil {
		return RulesFile{}, err
	}
	path, err := s.rulesPath(name)
	if err != nil {
		return RulesFile{}, err
	}
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		return RulesFile{}, apierr.NotFound("RULES_FILE_NOT_FOUND", fmt.Sprintf("rules file %q does not exist", name))
	}
	if err != nil {
		return RulesFile{}, fmt.Errorf("rules file %s: %w", name, err)
	}
	if !info.Mode().IsRegular() || info.Size() > rulesFileMaxBytes {
		return RulesFile{}, apierr.Invalid("RULES_FILE_UNREADABLE", fmt.Sprintf("rules file %q is not a regular file under %d bytes", name, rulesFileMaxBytes), nil)
	}
	data, err := os.ReadFile(path) //nolint:gosec // validated flat name under the daemon-owned rules dir
	if err != nil {
		return RulesFile{}, fmt.Errorf("rules file %s: %w", name, err)
	}
	return RulesFile{Name: name, Content: string(data), UpdatedAt: info.ModTime().UTC()}, nil
}

// WriteRules creates or replaces one rules file.
func (s *Service) WriteRules(ctx context.Context, name, content string) (RulesFile, error) {
	if err := ctx.Err(); err != nil {
		return RulesFile{}, err
	}
	path, err := s.rulesPath(name)
	if err != nil {
		return RulesFile{}, err
	}
	if len(content) > rulesFileMaxBytes {
		return RulesFile{}, apierr.Invalid("RULES_FILE_TOO_LARGE", fmt.Sprintf("rules file %q must stay under %d bytes", name, rulesFileMaxBytes), nil)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return RulesFile{}, fmt.Errorf("rules file %s: %w", name, err)
	}
	if err := writeFileAtomic(path, []byte(content)); err != nil {
		return RulesFile{}, fmt.Errorf("rules file %s: %w", name, err)
	}
	return s.ReadRules(ctx, name)
}

// DeleteRules removes one rules file; a missing file is not an error.
func (s *Service) DeleteRules(ctx context.Context, name string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := s.rulesPath(name)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("rules file %s: %w", name, err)
	}
	return nil
}

// ValidRulesFileName reports whether name is a flat Markdown file name the
// rules dir accepts: no directories, no traversal, a .md suffix.
func ValidRulesFileName(name string) bool {
	return rulesFileNamePattern.MatchString(name) && !strings.Contains(name, "..")
}

func (s *Service) rulesPath(name string) (string, error) {
	if !ValidRulesFileName(name) {
		return "", apierr.Invalid("RULES_FILE_NAME_INVALID", fmt.Sprintf("rules file name %q must be a flat Markdown file name", name), nil)
	}
	return filepath.Join(sessionmanager.RulesDir(s.dataDir), name), nil
}

func (s *Service) readProfiles() (map[string]domain.RoleProfile, error) {
	data, err := os.ReadFile(sessionmanager.DefaultProfilesPath(s.dataDir)) //nolint:gosec // daemon-owned data dir
	if errors.Is(err, os.ErrNotExist) {
		return map[string]domain.RoleProfile{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("default profiles: %w", err)
	}
	doc, err := domain.ParseDefaultProfiles(data)
	if err != nil {
		return nil, apierr.Invalid("INVALID_DEFAULT_PROFILES", err.Error(), nil)
	}
	if doc.Profiles == nil {
		doc.Profiles = map[string]domain.RoleProfile{}
	}
	return doc.Profiles, nil
}

func (s *Service) listRules() ([]RulesFileInfo, error) {
	entries, err := os.ReadDir(sessionmanager.RulesDir(s.dataDir))
	if errors.Is(err, os.ErrNotExist) {
		return []RulesFileInfo{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("rules dir: %w", err)
	}
	files := make([]RulesFileInfo, 0, len(entries))
	for _, entry := range entries {
		if !entry.Type().IsRegular() || !ValidRulesFileName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			continue
		}
		files = append(files, RulesFileInfo{Name: entry.Name(), SizeBytes: info.Size(), UpdatedAt: info.ModTime().UTC()})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// writeFileAtomic writes through a temp file in the same directory so a
// reader never sees a half-written document.
func writeFileAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpPath, path)
}
