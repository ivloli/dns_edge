package geo

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap"
)

// UpdaterConfig controls automatic xdb updates from GitHub Releases.
type UpdaterConfig struct {
	// ReleasesURL is the GitHub API endpoint for the latest release.
	// Defaults to https://api.github.com/repos/lionsoul2014/ip2region/releases/latest
	ReleasesURL string

	// GithubToken is an optional personal access token to avoid rate limits.
	GithubToken string

	// VersionFile stores the last successfully installed tag (e.g. "v3.16.0").
	// Defaults to <xdb dir>/.ip2region_release_tag
	VersionFile string

	// Interval between update checks. Defaults to 24h.
	Interval time.Duration

	// DownloadTimeout for fetching the tarball. Defaults to 10 min.
	DownloadTimeout time.Duration
}

// Updater periodically checks for a newer ip2region release and hot-reloads
// the Router when a new version is found.
type Updater struct {
	cfg       UpdaterConfig
	xdbPath   string // path where the v4 xdb is stored / should be written
	xdbPathV6 string // path where the v6 xdb is stored / should be written
	router    *Router
	log       *zap.Logger
	client    *http.Client
}

// NewUpdater creates an Updater for the given Router and xdb file paths.
// Call Start(ctx) in a goroutine to begin periodic checks.
func NewUpdater(cfg UpdaterConfig, xdbPath string, xdbPathV6 string, router *Router, log *zap.Logger) *Updater {
	if cfg.ReleasesURL == "" {
		cfg.ReleasesURL = "https://api.github.com/repos/lionsoul2014/ip2region/releases/latest"
	}
	if cfg.Interval <= 0 {
		cfg.Interval = 24 * time.Hour
	}
	if cfg.DownloadTimeout <= 0 {
		cfg.DownloadTimeout = 10 * time.Minute
	}
	if cfg.VersionFile == "" {
		cfg.VersionFile = filepath.Join(filepath.Dir(xdbPath), ".ip2region_release_tag")
	}
	return &Updater{
		cfg:       cfg,
		xdbPath:   xdbPath,
		xdbPathV6: xdbPathV6,
		router:    router,
		log:       log,
		client:    &http.Client{Timeout: cfg.DownloadTimeout},
	}
}

// CheckAndUpdate runs one update cycle: fetches the latest release tag, skips
// if already current, otherwise downloads and hot-reloads.
// force=true bypasses the version check (used at startup).
func (u *Updater) CheckAndUpdate(force bool) error {
	rel, err := u.fetchLatestRelease()
	if err != nil {
		return fmt.Errorf("fetch release info: %w", err)
	}

	localTag := u.readLocalVersion()
	missing := fileMissing(u.xdbPath) || fileMissing(u.xdbPathV6)

	if !force && !missing && localTag == rel.TagName {
		u.log.Debug("ip2region xdb is up to date", zap.String("tag", rel.TagName))
		return nil
	}

	u.log.Info("downloading ip2region xdb", zap.String("tag", rel.TagName), zap.String("from", localTag))
	if err := u.downloadAndExtract(rel); err != nil {
		return fmt.Errorf("download: %w", err)
	}

	newSearcherV4, err := loadSearcher(u.xdbPath)
	if err != nil {
		return fmt.Errorf("load new v4 xdb: %w", err)
	}
	newSearcherV6, err := loadSearcher(u.xdbPathV6)
	if err != nil {
		return fmt.Errorf("load new v6 xdb: %w", err)
	}
	u.router.swap(newSearcherV4, newSearcherV6)

	if err := os.WriteFile(u.cfg.VersionFile, []byte(rel.TagName+"\n"), 0644); err != nil {
		u.log.Warn("failed to write version file", zap.Error(err))
	}
	u.log.Info("ip2region xdb updated", zap.String("tag", rel.TagName))
	return nil
}

// Start runs periodic update checks until ctx is cancelled.
func (u *Updater) Start(ctx context.Context) {
	ticker := time.NewTicker(u.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := u.CheckAndUpdate(false); err != nil {
				u.log.Warn("ip2region periodic update failed", zap.Error(err))
			}
		}
	}
}

type githubRelease struct {
	TagName    string `json:"tag_name"`
	TarballURL string `json:"tarball_url"`
}

func (u *Updater) fetchLatestRelease() (*githubRelease, error) {
	req, err := http.NewRequest("GET", u.cfg.ReleasesURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	if u.cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+u.cfg.GithubToken)
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github API status %d", resp.StatusCode)
	}
	var rel githubRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	rel.TagName = strings.TrimSpace(rel.TagName)
	rel.TarballURL = strings.TrimSpace(rel.TarballURL)
	if rel.TagName == "" || rel.TarballURL == "" {
		return nil, fmt.Errorf("invalid release payload")
	}
	return &rel, nil
}

func (u *Updater) readLocalVersion() string {
	b, _ := os.ReadFile(u.cfg.VersionFile)
	return strings.TrimSpace(string(b))
}

// downloadAndExtract downloads the release tarball once and extracts both
// data/ip2region_v4.xdb and data/ip2region_v6.xdb from the same pass —
// either one missing is a hard error, since an xdb.Searcher only serves the
// IP version it was built from and a v4-only result would silently break
// IPv6 lookups downstream.
func (u *Updater) downloadAndExtract(rel *githubRelease) error {
	if err := os.MkdirAll(filepath.Dir(u.xdbPath), 0755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(u.xdbPathV6), 0755); err != nil {
		return err
	}
	req, err := http.NewRequest("GET", rel.TarballURL, nil)
	if err != nil {
		return err
	}
	if u.cfg.GithubToken != "" {
		req.Header.Set("Authorization", "Bearer "+u.cfg.GithubToken)
	}
	resp, err := u.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tarball GET status %d", resp.StatusCode)
	}

	gzr, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	defer gzr.Close()
	tr := tar.NewReader(gzr)

	const v4Suffix = "data/ip2region_v4.xdb"
	const v6Suffix = "data/ip2region_v6.xdb"
	var gotV4, gotV6 bool
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return err
		}
		if hdr.Typeflag != tar.TypeReg {
			continue
		}
		name := strings.TrimPrefix(hdr.Name, "./")
		switch {
		case strings.HasSuffix(name, v4Suffix):
			if err := writeAtomic(tr, u.xdbPath); err != nil {
				return err
			}
			gotV4 = true
		case strings.HasSuffix(name, v6Suffix):
			if err := writeAtomic(tr, u.xdbPathV6); err != nil {
				return err
			}
			gotV6 = true
		}
		if gotV4 && gotV6 {
			break
		}
	}
	if !gotV4 {
		return fmt.Errorf("tarball missing %s", v4Suffix)
	}
	if !gotV6 {
		return fmt.Errorf("tarball missing %s", v6Suffix)
	}
	return nil
}

// writeAtomic writes r to destPath via a temp file + rename (atomic on POSIX).
func writeAtomic(r io.Reader, destPath string) error {
	tmp := destPath + ".tmp"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, destPath)
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return err != nil
}
