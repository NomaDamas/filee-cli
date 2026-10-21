package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var smokeRequired = []string{"LICENSE", "THIRD-PARTY-NOTICES.md", "UPSTREAM.md", "README.md", "engines/rhwp/LICENSE", "engines/7zip/License.txt"}

// cmdSmoke unpacks a release archive and runs it: version, exit codes, PNG->WebP, MD->DOCX, MD->HWPX->PDF (rhwp, Korean
// text) and ZIP->7Z (7-Zip), plus the package layout. The first failure stops it.
func cmdSmoke(args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: smoke ARCHIVE")
	}
	archive, err := filepath.Abs(args[0])
	if err != nil {
		return err
	}
	base := filepath.Base(archive)
	kind, name := "tar.gz", strings.TrimSuffix(base, ".tar.gz")
	if strings.HasSuffix(base, ".zip") {
		kind, name = "zip", strings.TrimSuffix(base, ".zip")
	}
	tmp, err := os.MkdirTemp("", "filee-smoke-")
	if err != nil {
		return err
	}
	defer rmtree(tmp)
	if err := extractArchive(archive, kind, filepath.Join(tmp, "x")); err != nil {
		return err
	}
	tops, _ := os.ReadDir(filepath.Join(tmp, "x"))
	if len(tops) != 1 || !tops[0].IsDir() {
		return fmt.Errorf("%s must contain exactly one top-level folder", base)
	}
	if tops[0].Name() != name {
		return fmt.Errorf("top-level folder is %s, expected %s", tops[0].Name(), name)
	}
	pkg := filepath.Join(tmp, "x", name)
	exeName := "filee-cli"
	if kind == "zip" {
		exeName = "filee-cli.exe"
	}
	exe := filepath.Join(pkg, exeName)
	if _, err := os.Stat(exe); err != nil {
		return fmt.Errorf("%s is missing", exeName)
	}
	for _, r := range smokeRequired {
		if _, err := os.Stat(filepath.Join(pkg, filepath.FromSlash(r))); err != nil {
			return fmt.Errorf("%s is missing from the package", r)
		}
	}
	if m, _ := filepath.Glob(filepath.Join(pkg, "engines", "rhwp", "rhwp*")); len(m) == 0 {
		return fmt.Errorf("engine programs are missing (engines/rhwp)")
	}
	if m, _ := filepath.Glob(filepath.Join(pkg, "engines", "7zip", "7z*")); len(m) == 0 {
		return fmt.Errorf("engine programs are missing (engines/7zip)")
	}

	work := filepath.Join(tmp, "work")
	if err := os.MkdirAll(work, 0o755); err != nil {
		return err
	}
	s := &smoke{exe: exe, work: work}

	out, err := s.run(0, "version")
	if err != nil {
		return err
	}
	// The package version is v<upstream>-<revision>; the program reports the upstream version.
	wantVersion := ""
	if up, err := readUpstream(); err == nil {
		wantVersion = "filee " + up.UpstreamVersion
	}
	got := strings.TrimSpace(out)
	if !strings.HasPrefix(got, "filee ") || (wantVersion != "" && got != wantVersion) {
		return fmt.Errorf("`version` printed %q, expected %q", got, wantVersion)
	}
	fmt.Println("version:", got)

	if out, err = s.run(0, "formats"); err != nil {
		return err
	}
	if !strings.Contains(out, "png") {
		return fmt.Errorf("`formats` does not list png")
	}
	if _, err := s.run(2, "convert", "missing.png", "--to", "webp"); err != nil {
		return err
	}
	if _, err := s.run(2, "convert", "missing.png", "--to", "nosuchformat"); err != nil {
		return err
	}

	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 5), 128, 255})
		}
	}
	var pngData bytes.Buffer
	if err := png.Encode(&pngData, img); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(work, "pic.png"), pngData.Bytes(), 0o644); err != nil {
		return err
	}
	webp, err := s.convert("pic.png", "webp", "pic.webp")
	if err != nil {
		return err
	}
	if len(webp) < 12 || string(webp[:4]) != "RIFF" || string(webp[8:12]) != "WEBP" {
		return fmt.Errorf("pic.webp is not a WebP file")
	}
	fmt.Println("PNG -> WebP ok")

	md := "# 한글 테스트 문서\n\n안녕하세요. **한글** 렌더링 검사입니다. ABC 123.\n\n- 하나\n- 둘\n"
	if err := os.WriteFile(filepath.Join(work, "doc.md"), []byte(md), 0o644); err != nil {
		return err
	}
	docx, err := s.convert("doc.md", "docx", "doc.docx")
	if err != nil {
		return err
	}
	xmlDoc, err := zipMember(docx, "word/document.xml")
	if err != nil {
		return fmt.Errorf("doc.docx: %w", err)
	}
	if !bytes.Contains(xmlDoc, []byte("한글")) {
		return fmt.Errorf("doc.docx lost the Korean text")
	}
	fmt.Println("MD -> DOCX ok")

	if _, err := s.convert("doc.md", "hwpx", "doc.hwpx"); err != nil {
		return err
	}
	pdf, err := s.convert("doc.hwpx", "pdf", "doc.pdf")
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) || !(bytes.Contains(pdf, []byte("/Type /Page")) || bytes.Contains(pdf, []byte("/Type/Page"))) {
		return fmt.Errorf("doc.pdf (rhwp) is not a PDF with a page")
	}
	fmt.Println("MD -> HWPX -> PDF (rhwp) ok")

	var zbuf bytes.Buffer
	zw := zip.NewWriter(&zbuf)
	w, _ := zw.Create("a.txt")
	w.Write([]byte(strings.Repeat("hello ", 50)))
	zw.Close()
	if err := os.WriteFile(filepath.Join(work, "pack.zip"), zbuf.Bytes(), 0o644); err != nil {
		return err
	}
	sevenZ, err := s.convert("pack.zip", "7z", "pack.7z")
	if err != nil {
		return err
	}
	if !bytes.HasPrefix(sevenZ, []byte{'7', 'z', 0xbc, 0xaf, 0x27, 0x1c}) {
		return fmt.Errorf("pack.7z (7-Zip) has no 7z signature")
	}
	fmt.Println("ZIP -> 7Z (7-Zip) ok")

	if _, err := s.run(2, "convert", filepath.Join(work, "nothing-to-do"), "--to", "pdf"); err != nil {
		return err
	}
	fmt.Println("smoke test passed:", base)
	return nil
}

type smoke struct{ exe, work string }

func (s *smoke) run(expect int, args ...string) (string, error) {
	cmd := exec.Command(s.exe, args...)
	cmd.Dir = s.work
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", err
	}
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(5 * time.Minute):
		cmd.Process.Kill()
		return "", fmt.Errorf("filee-cli %s: timed out", strings.Join(args, " "))
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return "", err
	}
	if code != expect {
		return "", fmt.Errorf("filee-cli %s: exit %d, expected %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, expect, stdout.String(), stderr.String())
	}
	return stdout.String(), nil
}

type convertResult struct {
	Converted int    `json:"converted"`
	Failed    int    `json:"failed"`
	Target    string `json:"target"`
	Files     []struct {
		State   string   `json:"state"`
		Outputs []string `json:"outputs"`
	} `json:"files"`
}

func (s *smoke) convert(source, target, expected string) ([]byte, error) {
	out, err := s.run(0, "convert", source, "--to", target, "--json", "--overwrite")
	if err != nil {
		return nil, err
	}
	var r convertResult
	if err := json.Unmarshal([]byte(out), &r); err != nil {
		return nil, fmt.Errorf("%s -> %s: bad JSON: %w\n%s", source, target, err, out)
	}
	if r.Converted != 1 || r.Failed != 0 || r.Target != target || len(r.Files) != 1 || r.Files[0].State != "done" {
		return nil, fmt.Errorf("unexpected JSON for %s -> %s: %s", source, target, out)
	}
	want := filepath.Join(s.work, expected)
	if len(r.Files[0].Outputs) != 1 {
		return nil, fmt.Errorf("%s -> %s: outputs %v", source, target, r.Files[0].Outputs)
	}
	gotAbs, _ := filepath.EvalSymlinks(r.Files[0].Outputs[0])
	wantAbs, _ := filepath.EvalSymlinks(want)
	if gotAbs != wantAbs {
		return nil, fmt.Errorf("%s -> %s: output %s is not %s", source, target, r.Files[0].Outputs[0], want)
	}
	data, err := os.ReadFile(want)
	if err != nil || len(data) == 0 {
		return nil, fmt.Errorf("%s -> %s: %s is missing or empty", source, target, expected)
	}
	return data, nil
}

func zipMember(data []byte, name string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, err
	}
	for _, f := range zr.File {
		if f.Name == name {
			return readZipFile(f)
		}
	}
	return nil, fmt.Errorf("no %s in the archive", name)
}
