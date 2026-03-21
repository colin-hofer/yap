package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"golang.org/x/mod/semver"
)

const (
	defaultAPIBaseURL = "https://api.github.com"
	maxArchiveSize    = 200 << 20
)

type Config struct {
	RepoOwner      string
	RepoName       string
	BinaryName     string
	CurrentVersion string
	GOOS           string
	GOARCH         string
	ExecutablePath string
	APIBaseURL     string
	Client         *http.Client
}

type Result struct {
	PreviousVersion string
	LatestVersion   string
	AssetName       string
	ReleaseURL      string
	ExecutablePath  string
	Available       bool
	Updated         bool
	RestartRequired bool
}

type Updater struct {
	cfg Config
}

type githubRelease struct {
	TagName string        `json:"tag_name"`
	HTMLURL string        `json:"html_url"`
	Assets  []githubAsset `json:"assets"`
}

type githubAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
}

func New(cfg Config) (*Updater, error) {
	if strings.TrimSpace(cfg.RepoOwner) == "" {
		return nil, fmt.Errorf("update repo owner cannot be empty")
	}
	if strings.TrimSpace(cfg.RepoName) == "" {
		return nil, fmt.Errorf("update repo name cannot be empty")
	}
	if strings.TrimSpace(cfg.BinaryName) == "" {
		return nil, fmt.Errorf("update binary name cannot be empty")
	}
	if strings.TrimSpace(cfg.GOOS) == "" {
		cfg.GOOS = runtime.GOOS
	}
	if strings.TrimSpace(cfg.GOARCH) == "" {
		cfg.GOARCH = runtime.GOARCH
	}
	if strings.TrimSpace(cfg.APIBaseURL) == "" {
		cfg.APIBaseURL = defaultAPIBaseURL
	}
	if cfg.Client == nil {
		cfg.Client = &http.Client{Timeout: 2 * time.Minute}
	}
	if strings.TrimSpace(cfg.ExecutablePath) == "" {
		executablePath, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("resolve executable path: %w", err)
		}
		cfg.ExecutablePath = executablePath
	}
	return &Updater{cfg: cfg}, nil
}

func (u *Updater) Update(ctx context.Context) (Result, error) {
	check, err := u.prepareUpdate(ctx)
	if err != nil {
		return Result{}, err
	}
	if !check.result.Available {
		return check.result, nil
	}

	archiveData, err := u.download(ctx, check.asset.BrowserDownloadURL)
	if err != nil {
		return Result{}, err
	}
	binaryData, mode, err := extractBinary(check.asset.Name, archiveData, u.binaryFilename())
	if err != nil {
		return Result{}, err
	}
	restartRequired, err := u.replaceExecutable(binaryData, mode)
	if err != nil {
		return Result{}, err
	}
	check.result.Updated = true
	check.result.RestartRequired = restartRequired
	return check.result, nil
}

func (u *Updater) Check(ctx context.Context) (Result, error) {
	check, err := u.prepareUpdate(ctx)
	if err != nil {
		return Result{}, err
	}
	return check.result, nil
}

func (u *Updater) latestRelease(ctx context.Context) (githubRelease, githubAsset, error) {
	var release githubRelease
	url := strings.TrimRight(u.cfg.APIBaseURL, "/") + "/repos/" + u.cfg.RepoOwner + "/" + u.cfg.RepoName + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return release, githubAsset{}, fmt.Errorf("build release request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", userAgent(u.cfg.CurrentVersion))

	resp, err := u.cfg.Client.Do(req)
	if err != nil {
		return release, githubAsset{}, fmt.Errorf("request latest release: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return release, githubAsset{}, fmt.Errorf("request latest release: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return release, githubAsset{}, fmt.Errorf("decode latest release: %w", err)
	}
	asset, err := selectAsset(release, u.cfg.BinaryName, u.cfg.GOOS, u.cfg.GOARCH)
	if err != nil {
		return release, githubAsset{}, err
	}
	return release, asset, nil
}

func (u *Updater) download(ctx context.Context, url string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("build asset request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent(u.cfg.CurrentVersion))

	resp, err := u.cfg.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("download release asset: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, fmt.Errorf("download release asset: %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxArchiveSize+1))
	if err != nil {
		return nil, fmt.Errorf("read release asset: %w", err)
	}
	if len(data) > maxArchiveSize {
		return nil, fmt.Errorf("release asset exceeds %d bytes", maxArchiveSize)
	}
	return data, nil
}

func (u *Updater) replaceExecutable(binaryData []byte, mode os.FileMode) (bool, error) {
	executablePath := u.targetExecutablePath()
	targetMode := executableMode(executablePath, mode)
	if u.cfg.GOOS == "windows" {
		if err := stageWindowsReplacement(executablePath, binaryData, targetMode); err != nil {
			return false, err
		}
		return true, nil
	}
	if err := replaceInPlace(executablePath, binaryData, targetMode); err != nil {
		return false, err
	}
	return false, nil
}

func (u *Updater) targetExecutablePath() string {
	if resolved, err := filepath.EvalSymlinks(u.cfg.ExecutablePath); err == nil {
		return resolved
	}
	return u.cfg.ExecutablePath
}

type preparedUpdate struct {
	result Result
	asset  githubAsset
}

func (u *Updater) prepareUpdate(ctx context.Context) (preparedUpdate, error) {
	release, asset, err := u.latestRelease(ctx)
	if err != nil {
		return preparedUpdate{}, err
	}
	result := Result{
		PreviousVersion: strings.TrimSpace(u.cfg.CurrentVersion),
		LatestVersion:   release.TagName,
		AssetName:       asset.Name,
		ReleaseURL:      release.HTMLURL,
		ExecutablePath:  u.targetExecutablePath(),
		Available:       shouldUpdate(u.cfg.CurrentVersion, release.TagName),
	}
	return preparedUpdate{
		result: result,
		asset:  asset,
	}, nil
}

func selectAsset(release githubRelease, binaryName, goos, goarch string) (githubAsset, error) {
	expectedName := assetName(binaryName, release.TagName, goos, goarch)
	for _, asset := range release.Assets {
		if asset.Name == expectedName {
			return asset, nil
		}
	}
	return githubAsset{}, fmt.Errorf("latest release %s does not contain %s", release.TagName, expectedName)
}

func assetName(binaryName, tag, goos, goarch string) string {
	version := strings.TrimPrefix(strings.TrimSpace(tag), "v")
	if goos == "windows" {
		return fmt.Sprintf("%s_%s_%s_%s.zip", binaryName, version, goos, goarch)
	}
	return fmt.Sprintf("%s_%s_%s_%s.tar.gz", binaryName, version, goos, goarch)
}

func extractBinary(archiveName string, archiveData []byte, binaryName string) ([]byte, os.FileMode, error) {
	return extractBinaryWithLimit(archiveName, archiveData, binaryName, maxArchiveSize)
}

func extractBinaryWithLimit(archiveName string, archiveData []byte, binaryName string, maxSize int64) ([]byte, os.FileMode, error) {
	switch {
	case strings.HasSuffix(archiveName, ".tar.gz"):
		return extractTarGzBinaryWithLimit(archiveData, binaryName, maxSize)
	case strings.HasSuffix(archiveName, ".zip"):
		return extractZIPBinaryWithLimit(archiveData, binaryName, maxSize)
	default:
		return nil, 0, fmt.Errorf("unsupported release asset format %q", archiveName)
	}
}

func extractTarGzBinary(archiveData []byte, binaryName string) ([]byte, os.FileMode, error) {
	return extractTarGzBinaryWithLimit(archiveData, binaryName, maxArchiveSize)
}

func extractTarGzBinaryWithLimit(archiveData []byte, binaryName string, maxSize int64) ([]byte, os.FileMode, error) {
	gzipReader, err := gzip.NewReader(bytes.NewReader(archiveData))
	if err != nil {
		return nil, 0, fmt.Errorf("open tar.gz asset: %w", err)
	}
	defer gzipReader.Close()

	tarReader := tar.NewReader(gzipReader)
	for {
		header, err := tarReader.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, fmt.Errorf("read tar.gz asset: %w", err)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			continue
		}
		if path.Base(header.Name) != binaryName {
			continue
		}
		if header.Size > maxSize {
			return nil, 0, fmt.Errorf("binary %q exceeds %d bytes in tar.gz asset", binaryName, maxSize)
		}
		data, err := io.ReadAll(io.LimitReader(tarReader, maxSize+1))
		if err != nil {
			return nil, 0, fmt.Errorf("read binary from tar.gz asset: %w", err)
		}
		if int64(len(data)) > maxSize {
			return nil, 0, fmt.Errorf("binary %q exceeds %d bytes in tar.gz asset", binaryName, maxSize)
		}
		return data, os.FileMode(header.Mode), nil
	}
	return nil, 0, fmt.Errorf("binary %q not found in tar.gz asset", binaryName)
}

func extractZIPBinary(archiveData []byte, binaryName string) ([]byte, os.FileMode, error) {
	return extractZIPBinaryWithLimit(archiveData, binaryName, maxArchiveSize)
}

func extractZIPBinaryWithLimit(archiveData []byte, binaryName string, maxSize int64) ([]byte, os.FileMode, error) {
	reader, err := zip.NewReader(bytes.NewReader(archiveData), int64(len(archiveData)))
	if err != nil {
		return nil, 0, fmt.Errorf("open zip asset: %w", err)
	}
	for _, file := range reader.File {
		if path.Base(file.Name) != binaryName {
			continue
		}
		if int64(file.UncompressedSize64) > maxSize {
			return nil, 0, fmt.Errorf("binary %q exceeds %d bytes in zip asset", binaryName, maxSize)
		}
		fileReader, err := file.Open()
		if err != nil {
			return nil, 0, fmt.Errorf("open binary from zip asset: %w", err)
		}
		data, readErr := io.ReadAll(io.LimitReader(fileReader, maxSize+1))
		closeErr := fileReader.Close()
		if readErr != nil {
			return nil, 0, fmt.Errorf("read binary from zip asset: %w", readErr)
		}
		if closeErr != nil {
			return nil, 0, fmt.Errorf("close binary from zip asset: %w", closeErr)
		}
		if int64(len(data)) > maxSize {
			return nil, 0, fmt.Errorf("binary %q exceeds %d bytes in zip asset", binaryName, maxSize)
		}
		return data, file.Mode(), nil
	}
	return nil, 0, fmt.Errorf("binary %q not found in zip asset", binaryName)
}

func replaceInPlace(executablePath string, binaryData []byte, mode os.FileMode) error {
	dir := filepath.Dir(executablePath)
	file, err := os.CreateTemp(dir, "."+filepath.Base(executablePath)+".update-*")
	if err != nil {
		return fmt.Errorf("create staged executable: %w", err)
	}
	stagedPath := file.Name()
	defer os.Remove(stagedPath)

	if _, err := file.Write(binaryData); err != nil {
		file.Close()
		return fmt.Errorf("write staged executable: %w", err)
	}
	if err := file.Chmod(mode); err != nil {
		file.Close()
		return fmt.Errorf("chmod staged executable: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close staged executable: %w", err)
	}
	if err := os.Rename(stagedPath, executablePath); err != nil {
		return fmt.Errorf("replace executable: %w", err)
	}
	return nil
}

func stageWindowsReplacement(executablePath string, binaryData []byte, mode os.FileMode) error {
	stagedPath := executablePath + ".new"
	if err := os.WriteFile(stagedPath, binaryData, mode); err != nil {
		return fmt.Errorf("write staged executable: %w", err)
	}

	script, err := os.CreateTemp("", "yap-update-*.cmd")
	if err != nil {
		return fmt.Errorf("create updater script: %w", err)
	}
	scriptPath := script.Name()
	content := windowsUpdateScript(stagedPath, executablePath)
	if _, err := script.WriteString(content); err != nil {
		script.Close()
		return fmt.Errorf("write updater script: %w", err)
	}
	if err := script.Close(); err != nil {
		return fmt.Errorf("close updater script: %w", err)
	}

	// On Windows the running executable is locked, so a short-lived helper
	// process waits for exit and then swaps the new binary into place.
	cmd := exec.Command("cmd", "/C", scriptPath)
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start updater script: %w", err)
	}
	return nil
}

func windowsUpdateScript(stagedPath, executablePath string) string {
	return "@echo off\r\n" +
		"setlocal\r\n" +
		":wait\r\n" +
		"move /Y " + windowsQuote(stagedPath) + " " + windowsQuote(executablePath) + " >nul 2>&1\r\n" +
		"if errorlevel 1 (\r\n" +
		"  ping -n 2 127.0.0.1 >nul\r\n" +
		"  goto wait\r\n" +
		")\r\n" +
		"del \"%~f0\"\r\n"
}

func windowsQuote(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}

func executableMode(executablePath string, fallback os.FileMode) os.FileMode {
	if info, err := os.Stat(executablePath); err == nil && info.Mode().Perm() != 0 {
		fallback = info.Mode().Perm()
	}
	if fallback&0o111 == 0 {
		fallback |= 0o755
	}
	return fallback
}

func shouldUpdate(currentVersion, latestVersion string) bool {
	currentVersion = strings.TrimSpace(currentVersion)
	latestVersion = strings.TrimSpace(latestVersion)
	if latestVersion == "" {
		return false
	}
	if currentVersion == "" || currentVersion == "dev" || currentVersion == "(devel)" {
		return true
	}
	currentSemver := normalizeSemver(currentVersion)
	latestSemver := normalizeSemver(latestVersion)
	if semver.IsValid(currentSemver) && semver.IsValid(latestSemver) {
		return semver.Compare(latestSemver, currentSemver) > 0
	}
	return currentVersion != latestVersion
}

func normalizeSemver(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if strings.HasPrefix(value, "v") {
		return value
	}
	return "v" + value
}

func userAgent(currentVersion string) string {
	value := strings.TrimSpace(currentVersion)
	if value == "" {
		value = "dev"
	}
	return "yap/" + value
}

func (u *Updater) binaryFilename() string {
	if u.cfg.GOOS == "windows" {
		return u.cfg.BinaryName + ".exe"
	}
	return u.cfg.BinaryName
}
