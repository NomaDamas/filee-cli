package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

var smokeRequired = []string{"LICENSE", "THIRD-PARTY-NOTICES.md", "UPSTREAM.md", "README.md", "engines/rhwp/LICENSE", "engines/7zip/License.txt"}

// cmdSmoke unpacks a release archive and runs it: version, exit codes, PNG->WebP, MD->DOCX, MD->HWPX->PDF (rhwp, Korean
// text) and ZIP->7Z (7-Zip), plus the package layout, then downloads Pandoc, FFmpeg and Ghostscript on demand. With
// --heavy it also downloads calibre and LibreOffice. The first failure stops it.
func cmdSmoke(args []string) error {
	fl := newFlags("smoke")
	heavy := fl.Bool("heavy", false, "also download calibre and LibreOffice (about 650 MB) and convert with them")
	if err := fl.Parse(reorder(args)); err != nil || fl.NArg() != 1 {
		return fmt.Errorf("usage: smoke [--heavy] ARCHIVE")
	}
	archive, err := filepath.Abs(fl.Arg(0))
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
	s := &smoke{exe: exe, work: work, timeout: 5 * time.Minute}
	if *heavy {
		// Installing LibreOffice or calibre can take a quarter of an hour on a slow connection.
		s.timeout = 60 * time.Minute
	}

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
	if err := smokeEngines(s, pkg, name, kind == "zip", *heavy); err != nil {
		return err
	}
	fmt.Println("smoke test passed:", base)
	return nil
}

type smoke struct {
	exe, work string
	timeout   time.Duration
	env       []string // extra environment for filee-cli (nil: inherit)
}

// run returns the standard output of filee-cli.
func (s *smoke) run(expect int, args ...string) (string, error) {
	out, _, err := s.runSplit(expect, args...)
	return out, err
}

// runBoth returns standard output and error together.
func (s *smoke) runBoth(expect int, args ...string) (string, error) {
	out, errOut, err := s.runSplit(expect, args...)
	return out + errOut, err
}

func (s *smoke) runSplit(expect int, args ...string) (string, string, error) {
	cmd := exec.Command(s.exe, args...)
	cmd.Dir = s.work
	if s.env != nil {
		cmd.Env = append(os.Environ(), s.env...)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		return "", "", err
	}
	go func() { done <- cmd.Wait() }()
	var err error
	select {
	case err = <-done:
	case <-time.After(s.timeout):
		cmd.Process.Kill()
		return "", "", fmt.Errorf("filee-cli %s: timed out", strings.Join(args, " "))
	}
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		return "", "", err
	}
	if code != expect {
		return "", "", fmt.Errorf("filee-cli %s: exit %d, expected %d\nstdout: %s\nstderr: %s", strings.Join(args, " "), code, expect, stdout.String(), stderr.String())
	}
	return stdout.String(), stderr.String(), nil
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

// smokeEngines proves the packages hold no conversion engine except rhwp and 7-Zip, then converts the way a user
// does: with a PATH that has no engines and an empty data folder, so every engine is downloaded on demand (SHA-256
// checked by filee-cli) and one conversion only that engine can do runs. calibre and LibreOffice (about 650 MB together)
// are not downloaded here; `filee-cli engines list` must still offer them.
func smokeEngines(s *smoke, pkg, name string, windows, heavy bool) error {
	entries, err := os.ReadDir(filepath.Join(pkg, "engines"))
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() != "rhwp" && e.Name() != "7zip" {
			return fmt.Errorf("engines/%s is in the package; only rhwp and 7zip belong there (the rest are downloaded on demand)", e.Name())
		}
	}
	data, err := os.MkdirTemp("", "filee-smoke-data-")
	if err != nil {
		return err
	}
	defer rmtree(data)
	cleanPath := "/usr/bin:/bin"
	if windows {
		root := os.Getenv("SystemRoot")
		if root == "" {
			root = `C:\Windows`
		}
		cleanPath = filepath.Join(root, "System32") + ";" + root
	}
	s.env = []string{"FILEE_DATA_DIR=" + data, "PATH=" + cleanPath}
	defer func() { s.env = nil }()
	failed := func(source, target, why string) error { return fmt.Errorf("%s -> %s: %s", source, target, why) }
	downloads := func(id string) bool {
		switch id {
		case "ghostscript", "libreoffice":
			return !strings.Contains(name, "linux")
		}
		return true
	}

	onPC := map[string]bool{}
	list, err := s.run(0, "engines", "list")
	if err != nil {
		return err
	}
	for _, id := range []string{"libreoffice", "pandoc", "ghostscript", "ffmpeg", "calibre"} {
		if !downloads(id) {
			continue
		}
		line := ""
		for _, l := range strings.Split(list, "\n") {
			if strings.HasPrefix(l, id+" ") {
				line = l
			}
		}
		if line == "" {
			return fmt.Errorf("engines list does not offer %s", id)
		}
		if strings.Contains(line, "installed (older") {
			return fmt.Errorf("engines list: %s is installed in an empty data folder: %q", id, line)
		}
		onPC[id] = !strings.Contains(line, "not installed")
	}
	fmt.Println("engines list offers every engine of this platform ok")

	// A developer machine may have engines (Homebrew, an installed LibreOffice); Filee finds those even with a clean
	// PATH. Then the download is requested by name (that installs Filee's own copy, which is used before the PC's);
	// otherwise the conversion itself must download it.
	prepare := func(id string) error {
		if !onPC[id] {
			return nil
		}
		_, err := s.run(0, "engines", "install", id)
		return err
	}
	// --no-install: with the engine missing the conversion fails and names it; nothing is downloaded.
	if !onPC["pandoc"] {
		if _, err := s.run(1, "convert", "doc.md", "--to", "pptx", "--no-install", "--overwrite"); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(data, "engines", "pandoc")); err == nil {
			return fmt.Errorf("--no-install downloaded pandoc")
		}
	}
	for _, id := range []string{"pandoc", "ffmpeg", "ghostscript"} {
		if downloads(id) {
			if err := prepare(id); err != nil {
				return err
			}
		}
	}

	// Pandoc: only it writes PowerPoint from Markdown. The first conversion downloads it; the second must not.
	out, err := s.convert("doc.md", "pptx", "doc.pptx")
	if err != nil {
		return err
	}
	if _, err := zipMember(out, "ppt/presentation.xml"); err != nil {
		return failed("doc.md", "pptx", err.Error())
	}
	fmt.Println("MD -> PPTX (Pandoc, downloaded on demand) ok")
	again, err := s.runBoth(0, "convert", "doc.md", "--to", "pptx", "--overwrite")
	if err != nil {
		return err
	}
	if strings.Contains(again, "installing") {
		return fmt.Errorf("pandoc was downloaded a second time")
	}

	// FFmpeg.
	var pcm bytes.Buffer
	for i := 0; i < 8000; i++ {
		binary.Write(&pcm, binary.LittleEndian, int16(8000*math.Sin(float64(i)/10)))
	}
	var wav bytes.Buffer
	wav.WriteString("RIFF")
	binary.Write(&wav, binary.LittleEndian, uint32(36+pcm.Len()))
	wav.WriteString("WAVEfmt ")
	for _, v := range []any{uint32(16), uint16(1), uint16(1), uint32(8000), uint32(16000), uint16(2), uint16(16)} {
		binary.Write(&wav, binary.LittleEndian, v)
	}
	wav.WriteString("data")
	binary.Write(&wav, binary.LittleEndian, uint32(pcm.Len()))
	wav.Write(pcm.Bytes())
	if err := os.WriteFile(filepath.Join(s.work, "tone.wav"), wav.Bytes(), 0o644); err != nil {
		return err
	}
	mp3, err := s.convert("tone.wav", "mp3", "tone.mp3")
	if err != nil {
		return err
	}
	if !(bytes.HasPrefix(mp3, []byte("ID3")) || (len(mp3) > 2 && mp3[0] == 0xff && mp3[1]&0xe0 == 0xe0)) {
		return failed("tone.wav", "mp3", "not an MP3 file")
	}
	fmt.Println("WAV -> MP3 (FFmpeg, downloaded on demand) ok")

	// Ghostscript, where Filee offers a download for the platform.
	if downloads("ghostscript") {
		ps := "%!PS\n/Helvetica findfont 24 scalefont setfont 72 700 moveto (Filee) show showpage\n"
		if err := os.WriteFile(filepath.Join(s.work, "page.ps"), []byte(ps), 0o644); err != nil {
			return err
		}
		pdf, err := s.convert("page.ps", "pdf", "page.pdf")
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
			return failed("page.ps", "pdf", "not a PDF")
		}
		fmt.Println("PS -> PDF (Ghostscript, downloaded on demand) ok")
	}

	list, err = s.run(0, "engines", "list")
	if err != nil {
		return err
	}
	checked := []string{"pandoc", "ffmpeg", "ghostscript"}
	if heavy {
		checked = append(checked, "calibre", "libreoffice")
	}
	for _, id := range checked {
		if !downloads(id) {
			continue
		}
		for _, l := range strings.Split(list, "\n") {
			if strings.HasPrefix(l, id+" ") && !strings.Contains(l, " installed ") {
				return fmt.Errorf("after the conversions engines list says: %q (expected installed)", l)
			}
		}
	}
	if heavy {
		if err := smokeHeavy(s, name, windows, onPC, downloads, prepare); err != nil {
			return err
		}
	}
	// remove deletes only what filee-cli installed.
	if _, err := s.run(0, "engines", "remove", "pandoc"); err != nil {
		return err
	}
	if _, err := os.Stat(filepath.Join(data, "engines", "pandoc")); err == nil {
		return fmt.Errorf("engines remove left pandoc behind")
	}
	return nil
}

// smokeHeavy downloads calibre and LibreOffice on demand and converts with them: EPUB -> MOBI (only calibre writes MOBI),
// ODT -> DOC (only LibreOffice writes DOC) and, on Windows where Filee also installs H2Orestart and a JRE, HWPX -> DOCX.
// The sources are made by Pandoc and rhwp, which the steps before have installed.
func smokeHeavy(s *smoke, name string, windows bool, onPC map[string]bool, downloads func(string) bool, prepare func(string) error) error {
	failed := func(source, target, why string) error { return fmt.Errorf("%s -> %s: %s", source, target, why) }
	if downloads("calibre") {
		if err := prepare("calibre"); err != nil {
			return err
		}
		if _, err := s.convert("doc.md", "epub", "doc.epub"); err != nil {
			return err
		}
		mobi, err := s.convert("doc.epub", "mobi", "doc.mobi")
		if err != nil {
			return err
		}
		if len(mobi) < 68 || string(mobi[60:68]) != "BOOKMOBI" {
			return failed("doc.epub", "mobi", "not a MOBI file")
		}
		fmt.Println("EPUB -> MOBI (calibre, downloaded on demand) ok")
	}
	if downloads("libreoffice") {
		if err := prepare("libreoffice"); err != nil {
			return err
		}
		if _, err := s.convert("doc.md", "odt", "doc.odt"); err != nil {
			return err
		}
		doc, err := s.convert("doc.odt", "doc", "doc.doc")
		if err != nil {
			return err
		}
		if !bytes.HasPrefix(doc, []byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}) {
			return failed("doc.odt", "doc", "not an OLE2 Word file")
		}
		fmt.Println("ODT -> DOC (LibreOffice, downloaded on demand) ok")
		if windows {
			if _, err := s.convert("doc.md", "hwpx", "doc.hwpx"); err != nil {
				return err
			}
			docx, err := s.convert("doc.hwpx", "docx", "doc.docx")
			if err != nil {
				return err
			}
			if _, err := zipMember(docx, "word/document.xml"); err != nil {
				return failed("doc.hwpx", "docx", err.Error())
			}
			fmt.Println("HWPX -> DOCX (LibreOffice + H2Orestart + JRE) ok")
		}
	}
	return nil
}
