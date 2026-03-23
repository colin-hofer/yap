package update

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
)

func TestUpdaterReplacesExecutableFromTarGz(t *testing.T) {
	t.Parallel()

	archiveData := tarGzArchive(t, "yap", []byte("new-binary"))
	checksums := checksumFile("yap_0.2.0_linux_amd64.tar.gz", archiveData)
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/repos/acme/yap/releases/latest":
			return jsonResponse(releaseJSON()), nil
		case "/downloads/yap_0.2.0_linux_amd64.tar.gz":
			return binaryResponse(archiveData), nil
		case "/downloads/checksums.txt":
			return binaryResponse(checksums), nil
		default:
			return notFoundResponse(), nil
		}
	})}

	dir := t.TempDir()
	executablePath := filepath.Join(dir, "yap")
	if err := os.WriteFile(executablePath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}

	updater, err := New(Config{
		RepoOwner:      "acme",
		RepoName:       "yap",
		BinaryName:     "yap",
		CurrentVersion: "v0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecutablePath: executablePath,
		APIBaseURL:     "https://api.example.test",
		Client:         client,
	})
	if err != nil {
		t.Fatalf("create updater: %v", err)
	}

	result, err := updater.Update(context.Background())
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !result.Updated {
		t.Fatalf("expected update to run")
	}
	if !result.Available {
		t.Fatalf("expected update to be available")
	}
	if result.LatestVersion != "v0.2.0" {
		t.Fatalf("latest version = %q", result.LatestVersion)
	}
	if result.ExecutablePath != executablePath {
		t.Fatalf("executable path = %q", result.ExecutablePath)
	}
	data, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	if string(data) != "new-binary" {
		t.Fatalf("binary contents = %q", string(data))
	}
	info, err := os.Stat(executablePath)
	if err != nil {
		t.Fatalf("stat executable: %v", err)
	}
	if info.Mode()&0o111 == 0 {
		t.Fatalf("executable bit not preserved: %v", info.Mode())
	}
}

func TestUpdaterSkipsDownloadWhenAlreadyCurrent(t *testing.T) {
	t.Parallel()

	downloads := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/repos/acme/yap/releases/latest":
			return jsonResponse(releaseJSON()), nil
		case "/downloads/yap_0.2.0_linux_amd64.tar.gz":
			downloads++
			return response(http.StatusInternalServerError, []byte("should not download")), nil
		case "/downloads/checksums.txt":
			downloads++
			return response(http.StatusInternalServerError, []byte("should not download")), nil
		default:
			return notFoundResponse(), nil
		}
	})}

	dir := t.TempDir()
	executablePath := filepath.Join(dir, "yap")
	if err := os.WriteFile(executablePath, []byte("old-binary"), 0o755); err != nil {
		t.Fatalf("write executable: %v", err)
	}

	updater, err := New(Config{
		RepoOwner:      "acme",
		RepoName:       "yap",
		BinaryName:     "yap",
		CurrentVersion: "v0.2.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecutablePath: executablePath,
		APIBaseURL:     "https://api.example.test",
		Client:         client,
	})
	if err != nil {
		t.Fatalf("create updater: %v", err)
	}

	result, err := updater.Update(context.Background())
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if result.Updated {
		t.Fatalf("expected no update")
	}
	if result.Available {
		t.Fatalf("expected no available update")
	}
	if downloads != 0 {
		t.Fatalf("expected no download, got %d", downloads)
	}
	data, err := os.ReadFile(executablePath)
	if err != nil {
		t.Fatalf("read executable: %v", err)
	}
	if string(data) != "old-binary" {
		t.Fatalf("binary contents = %q", string(data))
	}
}

func TestExtractZIPBinary(t *testing.T) {
	t.Parallel()

	data, mode, err := extractBinary("yap_0.2.0_windows_amd64.zip", zipArchive(t, "yap.exe", []byte("windows-binary")), "yap.exe")
	if err != nil {
		t.Fatalf("extract binary: %v", err)
	}
	if string(data) != "windows-binary" {
		t.Fatalf("binary contents = %q", string(data))
	}
	if mode&0o111 == 0 {
		t.Fatalf("expected executable mode, got %v", mode)
	}
}

func TestUpdaterCheckReportsAvailableUpdateWithoutInstalling(t *testing.T) {
	t.Parallel()

	downloads := 0
	client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/repos/acme/yap/releases/latest":
			return jsonResponse(releaseJSON()), nil
		case "/downloads/yap_0.2.0_linux_amd64.tar.gz":
			downloads++
			return response(http.StatusInternalServerError, []byte("should not download")), nil
		case "/downloads/checksums.txt":
			downloads++
			return response(http.StatusInternalServerError, []byte("should not download")), nil
		default:
			return notFoundResponse(), nil
		}
	})}

	updater, err := New(Config{
		RepoOwner:      "acme",
		RepoName:       "yap",
		BinaryName:     "yap",
		CurrentVersion: "v0.1.0",
		GOOS:           "linux",
		GOARCH:         "amd64",
		ExecutablePath: "/tmp/yap",
		APIBaseURL:     "https://api.example.test",
		Client:         client,
	})
	if err != nil {
		t.Fatalf("create updater: %v", err)
	}

	result, err := updater.Check(context.Background())
	if err != nil {
		t.Fatalf("check: %v", err)
	}
	if !result.Available {
		t.Fatalf("expected update to be available")
	}
	if result.Updated {
		t.Fatalf("did not expect installation during check")
	}
	if downloads != 0 {
		t.Fatalf("expected no download during check, got %d", downloads)
	}
}

func TestExtractTarGzBinaryRejectsOversizedBinary(t *testing.T) {
	t.Parallel()

	_, _, err := extractBinaryWithLimit("yap_0.2.0_linux_amd64.tar.gz", tarGzArchive(t, "yap", []byte("too-large")), "yap", 4)
	if err == nil {
		t.Fatalf("expected oversized tar.gz binary to fail")
	}
}

func TestExtractZIPBinaryRejectsOversizedBinary(t *testing.T) {
	t.Parallel()

	_, _, err := extractBinaryWithLimit("yap_0.2.0_windows_amd64.zip", zipArchive(t, "yap.exe", []byte("too-large")), "yap.exe", 4)
	if err == nil {
		t.Fatalf("expected oversized zip binary to fail")
	}
}

func TestVerifyChecksumRejectsMismatch(t *testing.T) {
	t.Parallel()

	err := verifyChecksum("asset.tar.gz", []byte("actual"), []byte("deadbeef  asset.tar.gz\n"))
	if err == nil {
		t.Fatalf("expected checksum mismatch")
	}
}

func tarGzArchive(t *testing.T, name string, contents []byte) []byte {
	t.Helper()

	var buffer bytes.Buffer
	gzipWriter := gzip.NewWriter(&buffer)
	tarWriter := tar.NewWriter(gzipWriter)

	header := &tar.Header{
		Name: name,
		Mode: 0o755,
		Size: int64(len(contents)),
	}
	if err := tarWriter.WriteHeader(header); err != nil {
		t.Fatalf("write tar header: %v", err)
	}
	if _, err := tarWriter.Write(contents); err != nil {
		t.Fatalf("write tar body: %v", err)
	}
	if err := tarWriter.Close(); err != nil {
		t.Fatalf("close tar writer: %v", err)
	}
	if err := gzipWriter.Close(); err != nil {
		t.Fatalf("close gzip writer: %v", err)
	}
	return buffer.Bytes()
}

func zipArchive(t *testing.T, name string, contents []byte) []byte {
	t.Helper()

	var buffer bytes.Buffer
	zipWriter := zip.NewWriter(&buffer)
	header := &zip.FileHeader{Name: name}
	header.SetMode(0o755)
	fileWriter, err := zipWriter.CreateHeader(header)
	if err != nil {
		t.Fatalf("create zip header: %v", err)
	}
	if _, err := fileWriter.Write(contents); err != nil {
		t.Fatalf("write zip body: %v", err)
	}
	if err := zipWriter.Close(); err != nil {
		t.Fatalf("close zip writer: %v", err)
	}
	return buffer.Bytes()
}

func releaseJSON() string {
	return `{"tag_name":"v0.2.0","html_url":"https://example.invalid/release","assets":[{"name":"yap_0.2.0_linux_amd64.tar.gz","browser_download_url":"https://api.example.test/downloads/yap_0.2.0_linux_amd64.tar.gz"},{"name":"checksums.txt","browser_download_url":"https://api.example.test/downloads/checksums.txt"}]}`
}

func checksumFile(name string, body []byte) []byte {
	sum := sha256.Sum256(body)
	return []byte(hex.EncodeToString(sum[:]) + "  " + name + "\n")
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) {
	return fn(r)
}

func jsonResponse(body string) *http.Response {
	resp := response(http.StatusOK, []byte(body))
	resp.Header.Set("Content-Type", "application/json")
	return resp
}

func binaryResponse(body []byte) *http.Response {
	return response(http.StatusOK, body)
}

func notFoundResponse() *http.Response {
	return response(http.StatusNotFound, []byte("not found"))
}

func response(statusCode int, body []byte) *http.Response {
	return &http.Response{
		StatusCode: statusCode,
		Status:     http.StatusText(statusCode),
		Header:     make(http.Header),
		Body:       io.NopCloser(bytes.NewReader(body)),
	}
}
