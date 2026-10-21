package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/xi2/xz"
)

var rids = []string{"win-x64", "linux-x64", "linux-arm64", "osx-x64", "osx-arm64"}

var rootDir string

// findRoot locates the repository: $FILEE_BUILD_ROOT, or the nearest parent of the working directory with an UPSTREAM file.
func findRoot() error {
	if v := os.Getenv("FILEE_BUILD_ROOT"); v != "" {
		rootDir = v
		return nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "UPSTREAM")); err == nil {
			rootDir = dir
			return nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return errors.New("not inside the filee-cli repository (no UPSTREAM file found); run from the repository or set FILEE_BUILD_ROOT")
		}
		dir = parent
	}
}

func upstreamDir() string { return filepath.Join(rootDir, ".upstream", "Filee") }
func dataDir() string     { return filepath.Join(rootDir, "data") }
func cacheDir() string    { return filepath.Join(rootDir, ".cache") }
func workDir() string     { return filepath.Join(rootDir, "work") }

type Upstream struct {
	Repository, Tag, Commit, Revision string
	UpstreamVersion, Version          string
}

func readUpstream() (Upstream, error) {
	data, err := os.ReadFile(filepath.Join(rootDir, "UPSTREAM"))
	if err != nil {
		return Upstream{}, err
	}
	values := map[string]string{}
	for _, line := range strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, _ := strings.Cut(line, "=")
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	for _, key := range []string{"repository", "tag", "commit", "revision"} {
		if values[key] == "" {
			return Upstream{}, fmt.Errorf("UPSTREAM has no '%s'", key)
		}
	}
	u := Upstream{Repository: values["repository"], Tag: values["tag"], Commit: values["commit"], Revision: values["revision"]}
	u.UpstreamVersion = strings.TrimPrefix(u.Tag, "v")
	u.Version = u.UpstreamVersion + "-" + u.Revision
	return u, nil
}

func packageName(up Upstream, rid string) string { return "filee-cli-" + up.Version + "-" + rid }

func archiveName(up Upstream, rid string) string {
	if strings.HasPrefix(rid, "win-") {
		return packageName(up, rid) + ".zip"
	}
	return packageName(up, rid) + ".tar.gz"
}

type runOpts struct {
	Dir     string
	Env     []string
	Capture bool
	Quiet   bool
}

// run executes a command, echoing it. With Capture the stdout is returned; stderr always goes to the console.
func run(o runOpts, name string, args ...string) (string, error) {
	if !o.Quiet {
		fmt.Println("+ " + name + " " + strings.Join(args, " "))
	}
	cmd := exec.Command(name, args...)
	cmd.Dir = o.Dir
	if o.Env != nil {
		cmd.Env = append(os.Environ(), o.Env...)
	}
	cmd.Stderr = os.Stderr
	var out strings.Builder
	if o.Capture {
		cmd.Stdout = &out
	} else {
		cmd.Stdout = os.Stdout
	}
	if err := cmd.Run(); err != nil {
		return out.String(), fmt.Errorf("%s failed: %w", name, err)
	}
	return out.String(), nil
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// download fetches url into cache/<sha256> and verifies the hash (a cached file is re-verified). A transfer that stalls
// for a minute is cut and resumed with a Range request (up to 8 attempts), so a slow mirror cannot hang the build.
func download(url, sha string, cache string) (string, error) {
	sha = strings.ToLower(sha)
	if err := os.MkdirAll(cache, 0o755); err != nil {
		return "", err
	}
	target := filepath.Join(cache, sha)
	if got, err := sha256File(target); err == nil && got == sha {
		return target, nil
	}
	fmt.Println("get " + url)
	partial := target + ".partial"
	var lastErr error
	for attempt := 1; attempt <= 8; attempt++ {
		done, err := fetchRange(url, partial)
		if err == nil && done {
			got, err := sha256File(partial)
			if err != nil {
				return "", err
			}
			if got == sha {
				return target, os.Rename(partial, target)
			}
			os.Remove(partial)
			return "", fmt.Errorf("checksum mismatch for %s: expected %s, got %s", url, sha, got)
		}
		lastErr = err
		fmt.Printf("download of %s interrupted (%v); attempt %d of 8\n", url, err, attempt)
		time.Sleep(time.Duration(attempt) * 2 * time.Second)
	}
	return "", fmt.Errorf("GET %s: %w", url, lastErr)
}

// fetchRange appends the rest of url to partial. done is true when the whole body was received.
func fetchRange(url, partial string) (done bool, err error) {
	var have int64
	if st, err := os.Stat(partial); err == nil {
		have = st.Size()
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("User-Agent", "filee-build")
	if have > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", have))
	}
	// DisableCompression: some mirrors answer with Content-Encoding: gzip for a .tar.gz, and Go would silently unpack it,
	// which changes the bytes the pinned SHA-256 is about.
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, DisableCompression: true, ResponseHeaderTimeout: time.Minute, TLSHandshakeTimeout: time.Minute}}
	resp, err := client.Do(req)
	if err != nil {
		return false, err
	}
	defer resp.Body.Close()
	flags := os.O_CREATE | os.O_WRONLY
	// A download site that sends an HTML page where an archive is pinned (SourceForge's mirror chooser answers unknown
	// clients that way) is fetched with curl instead, which it serves the file to. The SHA-256 decides if it is right.
	if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/html") && !strings.HasSuffix(strings.SplitN(url, "?", 2)[0], ".html") {
		resp.Body.Close()
		if _, err := exec.LookPath("curl"); err != nil {
			return false, fmt.Errorf("%s answered with an HTML page and curl is not available", url)
		}
		os.Remove(partial)
		cmd := exec.Command("curl", "--fail", "--silent", "--show-error", "--location", "--retry", "3", "--connect-timeout", "30", "--speed-limit", "1024", "--speed-time", "60", "--output", partial, url)
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			return false, fmt.Errorf("curl %s: %w", url, err)
		}
		return true, nil
	}
	switch resp.StatusCode {
	case http.StatusOK:
		flags |= os.O_TRUNC // the server ignored the range: start over
	case http.StatusPartialContent:
		flags |= os.O_APPEND
	case http.StatusRequestedRangeNotSatisfiable:
		return true, nil // the partial file already holds everything
	default:
		return false, fmt.Errorf("%s", resp.Status)
	}
	out, err := os.OpenFile(partial, flags, 0o644)
	if err != nil {
		return false, err
	}
	defer out.Close()
	stall := time.AfterFunc(time.Minute, cancel)
	defer stall.Stop()
	buf := make([]byte, 1<<20)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			stall.Reset(time.Minute)
			if _, werr := out.Write(buf[:n]); werr != nil {
				return false, werr
			}
		}
		if rerr == io.EOF {
			return true, nil
		}
		if rerr != nil {
			return false, rerr
		}
	}
}

// rmtree removes a path, clearing read-only bits first (Windows).
func rmtree(path string) error {
	if _, err := os.Lstat(path); err != nil {
		return nil
	}
	if err := os.RemoveAll(path); err == nil {
		return nil
	}
	filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err == nil {
			os.Chmod(p, 0o700)
		}
		return nil
	})
	return os.RemoveAll(path)
}

func readJSON(path string, v any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

func fail(err error) int {
	if os.Getenv("GITHUB_ACTIONS") != "" {
		fmt.Fprintf(os.Stderr, "::error::%v\n", err)
	} else {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
	}
	return 1
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func hostRID() string {
	arch := map[string]string{"amd64": "x64", "arm64": "arm64"}[runtime.GOARCH]
	os := map[string]string{"windows": "win", "linux": "linux", "darwin": "osx"}[runtime.GOOS]
	return os + "-" + arch
}

// ---- archives ----------------------------------------------------------------------------------------------------

func safeJoin(dest, name string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(name))
	if filepath.IsAbs(clean) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unsafe archive member: %s", name)
	}
	return filepath.Join(dest, clean), nil
}

// pendingLink is a symlink member, created after every regular file so that nothing is ever written through a link.
type pendingLink struct{ name, target, link string }

// makeLinks creates the collected symlinks. A target must be relative and stay inside dest.
func makeLinks(dest string, links []pendingLink) error {
	for _, l := range links {
		if l.target == "" || filepath.IsAbs(l.target) || strings.HasPrefix(l.target, "/") || strings.HasPrefix(l.target, `\`) {
			return fmt.Errorf("unsafe symlink %s -> %s (absolute or empty target)", l.name, l.target)
		}
		resolved := filepath.Join(filepath.Dir(l.link), filepath.FromSlash(l.target))
		rel, err := filepath.Rel(dest, resolved)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("unsafe symlink %s -> %s (leaves the package)", l.name, l.target)
		}
		if err := os.MkdirAll(filepath.Dir(l.link), 0o755); err != nil {
			return err
		}
		if err := os.Symlink(filepath.FromSlash(l.target), l.link); err != nil {
			return err
		}
	}
	return nil
}

// extractArchive unpacks kind ("zip", "tar.gz", "tar.xz") into dest. Hard links and special files are refused;
// symlinks are kept when their target is relative and stays inside dest.
func extractArchive(archive, kind, dest string) error {
	if err := os.MkdirAll(dest, 0o755); err != nil {
		return err
	}
	var links []pendingLink
	if kind == "zip" {
		r, err := zip.OpenReader(archive)
		if err != nil {
			return err
		}
		defer r.Close()
		for _, f := range r.File {
			target, err := safeJoin(dest, f.Name)
			if err != nil {
				return err
			}
			if f.FileInfo().IsDir() {
				if err := os.MkdirAll(target, 0o755); err != nil {
					return err
				}
				continue
			}
			if f.Mode()&fs.ModeSymlink != 0 {
				rc, err := f.Open()
				if err != nil {
					return err
				}
				data, err := io.ReadAll(io.LimitReader(rc, 4096))
				rc.Close()
				if err != nil {
					return err
				}
				links = append(links, pendingLink{f.Name, string(data), target})
				continue
			}
			if !f.Mode().IsRegular() {
				return fmt.Errorf("unsafe archive member (not a regular file): %s", f.Name)
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			rc, err := f.Open()
			if err != nil {
				return err
			}
			err = writeFile(target, rc, f.Mode().Perm()|0o600)
			rc.Close()
			if err != nil {
				return err
			}
		}
		return makeLinks(dest, links)
	}
	file, err := os.Open(archive)
	if err != nil {
		return err
	}
	defer file.Close()
	var reader io.Reader
	switch kind {
	case "tar.gz":
		gz, err := gzip.NewReader(file)
		if err != nil {
			return err
		}
		defer gz.Close()
		reader = gz
	case "tar.xz":
		xr, err := xz.NewReader(file, 0)
		if err != nil {
			return err
		}
		reader = xr
	default:
		return fmt.Errorf("unknown archive kind %q", kind)
	}
	tr := tar.NewReader(reader)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return makeLinks(dest, links)
		}
		if err != nil {
			return err
		}
		target, err := safeJoin(dest, h.Name)
		if err != nil {
			return err
		}
		switch h.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := writeFile(target, tr, os.FileMode(h.Mode).Perm()|0o600); err != nil {
				return err
			}
		case tar.TypeSymlink:
			links = append(links, pendingLink{h.Name, h.Linkname, target})
		case tar.TypeXGlobalHeader:
		default:
			return fmt.Errorf("unsafe archive member (hard links and special files are not allowed): %s", h.Name)
		}
	}
}

func writeFile(path string, r io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, r); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return writeFile(dst, in, info.Mode().Perm()|0o600)
}

// copyTree copies a folder, keeping symlinks and permission bits.
func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		target := filepath.Join(dst, rel)
		switch {
		case d.IsDir():
			return os.MkdirAll(target, 0o755)
		case d.Type()&fs.ModeSymlink != 0:
			link, err := os.Readlink(p)
			if err != nil {
				return err
			}
			return os.Symlink(link, target)
		default:
			return copyFile(p, target)
		}
	})
}

// listFiles returns slash-separated relative paths of all regular files under dir, sorted.
func listFiles(dir string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			files = append(files, filepath.ToSlash(rel))
		}
		return nil
	})
	sort.Strings(files)
	return files, err
}
