package geo

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// APISource is implemented by edgeagent.Agent — it lets the API-based
// updater below pull the active ip2region artifact from edgeapi over the
// same authenticated gRPC connection edgeagent already maintains, instead
// of downloading directly from GitHub Releases (see Updater in updater.go).
// Prefer this for anything shipped to a customer site: it only needs
// connectivity to edgeapi (already required for NS sync to work at all),
// not outbound internet access to GitHub.
type APISource interface {
	// FindPublicArtifact returns the fileId and a short version code for the
	// ip2region artifact currently marked active on edgeapi. fileId <= 0
	// means no ip2region artifact has been uploaded/activated yet.
	FindPublicArtifact() (fileId int64, code string, err error)

	// DownloadFile writes the artifact's raw bytes to w.
	DownloadFile(fileId int64, w io.Writer) error
}

// APIUpdaterConfig controls automatic xdb updates pulled from edgeapi.
type APIUpdaterConfig struct {
	// Source fetches artifact metadata/bytes from edgeapi. Required.
	Source APISource

	// VersionFile stores the last successfully installed artifact code.
	// Defaults to "<dir of xdbPath>/.ip2region_api_code"
	VersionFile string

	// Interval between update checks. Defaults to 24h.
	Interval time.Duration
}

// APIUpdater periodically checks edgeapi for a newer ip2region artifact,
// downloads it if missing/outdated, and hot-reloads the given Router.
type APIUpdater struct {
	cfg     APIUpdaterConfig
	xdbPath string
	router  *Router
	log     *zap.Logger
}

// NewAPIUpdater creates an APIUpdater for the given Router and xdb file path.
// Call Start(ctx) in a goroutine to begin periodic checks.
func NewAPIUpdater(cfg APIUpdaterConfig, xdbPath string, router *Router, log *zap.Logger) *APIUpdater {
	if cfg.Interval <= 0 {
		cfg.Interval = 24 * time.Hour
	}
	if cfg.VersionFile == "" {
		cfg.VersionFile = filepath.Join(filepath.Dir(xdbPath), ".ip2region_api_code")
	}
	return &APIUpdater{
		cfg:     cfg,
		xdbPath: xdbPath,
		router:  router,
		log:     log,
	}
}

// CheckAndUpdate runs one update cycle: asks edgeapi for the currently active
// ip2region artifact, skips if the local copy is already current, otherwise
// downloads and hot-reloads.
// force=true bypasses the version check even when the local xdb file is
// already current — use force=false for normal startup/periodic checks (a
// missing local file always triggers a download regardless of force).
func (u *APIUpdater) CheckAndUpdate(force bool) error {
	fileId, code, err := u.cfg.Source.FindPublicArtifact()
	if err != nil {
		return fmt.Errorf("find public ip2region artifact: %w", err)
	}
	if fileId <= 0 {
		u.log.Debug("no ip2region artifact activated on edgeapi yet")
		return nil
	}

	localCode := u.readLocalVersion()
	missing := fileMissing(u.xdbPath)

	if !force && !missing && localCode == code {
		u.log.Debug("ip2region xdb is up to date", zap.String("code", code))
		return nil
	}

	u.log.Info("downloading ip2region xdb from edgeapi", zap.String("code", code), zap.String("from", localCode))
	if err := u.downloadFrom(fileId); err != nil {
		return fmt.Errorf("download from edgeapi: %w", err)
	}

	newSearcher, err := loadSearcher(u.xdbPath)
	if err != nil {
		return fmt.Errorf("load new xdb: %w", err)
	}
	u.router.swap(newSearcher)

	if err := os.WriteFile(u.cfg.VersionFile, []byte(code+"\n"), 0644); err != nil {
		u.log.Warn("failed to write version file", zap.Error(err))
	}
	u.log.Info("ip2region xdb updated", zap.String("code", code))
	return nil
}

// Start runs periodic update checks until ctx is cancelled.
func (u *APIUpdater) Start(ctx context.Context) {
	ticker := time.NewTicker(u.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := u.CheckAndUpdate(false); err != nil {
				u.log.Warn("ip2region periodic update from edgeapi failed", zap.Error(err))
			}
		}
	}
}

func (u *APIUpdater) readLocalVersion() string {
	b, _ := os.ReadFile(u.cfg.VersionFile)
	return strings.TrimSpace(string(b))
}

func (u *APIUpdater) downloadFrom(fileId int64) error {
	if err := os.MkdirAll(filepath.Dir(u.xdbPath), 0755); err != nil {
		return err
	}
	tmp := u.xdbPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if err := u.cfg.Source.DownloadFile(fileId, f); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, u.xdbPath)
}
