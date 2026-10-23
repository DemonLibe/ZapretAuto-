package main

import (
	"archive/zip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 60 * time.Second}

func httpGet(url string) (*http.Response, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "ZapretAuto")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != 200 {
		resp.Body.Close()
		return nil, fmt.Errorf("%s: HTTP %d", url, resp.StatusCode)
	}
	return resp, nil
}

// latestVersion берёт номер последней версии из того же файла, что и service.bat.
func latestVersion(repo string) (string, error) {
	resp, err := httpGet("https://raw.githubusercontent.com/" + repo + "/main/.service/version.txt")
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 64))
	if err != nil {
		return "", err
	}
	v := strings.TrimSpace(string(b))
	if !regexp.MustCompile(`^[0-9][0-9A-Za-z.\-]*$`).MatchString(v) {
		return "", fmt.Errorf("странный ответ version.txt: %q", v)
	}
	return v, nil
}

// compareVersions: -1 если a<b, 0 если равны, 1 если a>b ("1.10.4" vs "1.9.7").
func compareVersions(a, b string) int {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) || i < len(pb); i++ {
		var x, y string
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		nx, ex := strconv.Atoi(x)
		ny, ey := strconv.Atoi(y)
		switch {
		case ex == nil && ey == nil && nx != ny:
			if nx < ny {
				return -1
			}
			return 1
		case (ex != nil || ey != nil) && x != y:
			if x < y {
				return -1
			}
			return 1
		}
	}
	return 0
}

// releaseZipURLs — откуда можно скачать архив версии: сначала из API (точное имя файла),
// затем по стандартному имени файла релиза.
func releaseZipURLs(repo, ver string) []string {
	var urls []string
	if resp, err := httpGet("https://api.github.com/repos/" + repo + "/releases/tags/" + ver); err == nil {
		var rel struct {
			Assets []struct {
				Name string `json:"name"`
				URL  string `json:"browser_download_url"`
			} `json:"assets"`
		}
		if json.NewDecoder(resp.Body).Decode(&rel) == nil {
			for _, a := range rel.Assets {
				if strings.HasSuffix(strings.ToLower(a.Name), ".zip") {
					urls = append(urls, a.URL)
				}
			}
		}
		resp.Body.Close()
	}
	return append(urls, fmt.Sprintf("https://github.com/%s/releases/download/%s/zapret-discord-youtube-%s.zip", repo, ver, ver))
}

// downloadRelease скачивает и распаковывает версию ver в releases\<ver>.
func downloadRelease(repo, ver string) error {
	dest := releaseDir(ver)
	if isValidRelease(dest) {
		return nil
	}
	_ = os.MkdirAll(releasesDir(), 0o755)
	tmpZip := filepath.Join(releasesDir(), ver+".zip.part")
	defer os.Remove(tmpZip)

	var lastErr error
	for _, u := range releaseZipURLs(repo, ver) {
		if lastErr = downloadFile(u, tmpZip); lastErr == nil {
			break
		}
		logf("не удалось скачать %s: %v", u, lastErr)
	}
	if lastErr != nil {
		return lastErr
	}

	tmpDir := dest + ".part"
	_ = os.RemoveAll(tmpDir)
	defer os.RemoveAll(tmpDir)
	if err := unzip(tmpZip, tmpDir); err != nil {
		return fmt.Errorf("распаковка: %w", err)
	}
	root, err := findReleaseRoot(tmpDir)
	if err != nil {
		return err
	}
	_ = os.RemoveAll(dest)
	return os.Rename(root, dest)
}

func downloadFile(url, path string) error {
	resp, err := httpGet(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	// Ограничение размера на случай подмены ответа (релиз весит ~2 МБ).
	n, err := io.Copy(f, io.LimitReader(resp.Body, 200<<20))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil && n < 1024 {
		err = errors.New("слишком маленький файл")
	}
	return err
}

func unzip(src, dst string) error {
	r, err := zip.OpenReader(src)
	if err != nil {
		return err
	}
	defer r.Close()
	dstAbs, _ := filepath.Abs(dst)
	for _, f := range r.File {
		p := filepath.Join(dstAbs, filepath.FromSlash(f.Name))
		if p != dstAbs && !strings.HasPrefix(p, dstAbs+string(os.PathSeparator)) {
			return fmt.Errorf("недопустимый путь в архиве: %s", f.Name)
		}
		if f.FileInfo().IsDir() {
			if err := os.MkdirAll(p, 0o755); err != nil {
				return err
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		rc, err := f.Open()
		if err != nil {
			return err
		}
		out, err := os.Create(p)
		if err == nil {
			_, err = io.Copy(out, rc)
			if cerr := out.Close(); err == nil {
				err = cerr
			}
		}
		rc.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// findReleaseRoot ищет папку с bin\winws.exe (в архиве она обычно вложена в
// zapret-discord-youtube-X.Y.Z\).
func findReleaseRoot(dir string) (string, error) {
	var found string
	_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
		if err != nil || found != "" {
			return nil
		}
		if !d.IsDir() && strings.EqualFold(d.Name(), "winws.exe") && strings.EqualFold(filepath.Base(filepath.Dir(p)), "bin") {
			found = filepath.Dir(filepath.Dir(p))
		}
		return nil
	})
	if found == "" || !isValidRelease(found) {
		return "", errors.New("в архиве нет bin\\winws.exe или стратегий")
	}
	return found, nil
}

func isValidRelease(dir string) bool {
	if _, err := os.Stat(filepath.Join(dir, "bin", "winws.exe")); err != nil {
		return false
	}
	names, err := listStrategies(dir)
	return err == nil && len(names) > 0
}

// localVersion читает LOCAL_VERSION из service.bat распакованного релиза.
func localVersion(dir string) string {
	b, err := os.ReadFile(filepath.Join(dir, "service.bat"))
	if err != nil {
		return ""
	}
	m := regexp.MustCompile(`set "LOCAL_VERSION=([^"]+)"`).FindSubmatch(b)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(string(m[1]))
}

// importLocalRelease копирует уже распакованный zapret (например, папку, рядом
// с которой лежит exe) в releases\<ver> — чтобы работать даже без доступа к GitHub.
func importLocalRelease(src string) (string, error) {
	ver := localVersion(src)
	if ver == "" || !isValidRelease(src) {
		return "", errors.New("рядом нет распакованного zapret")
	}
	dest := releaseDir(ver)
	if isValidRelease(dest) {
		return ver, nil
	}
	tmp := dest + ".part"
	_ = os.RemoveAll(tmp)
	if err := copyDir(src, tmp); err != nil {
		os.RemoveAll(tmp)
		return "", err
	}
	_ = os.RemoveAll(dest)
	return ver, os.Rename(tmp, dest)
}

func copyDir(src, dst string) error {
	return filepath.WalkDir(src, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		t := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(t, 0o755)
		}
		if strings.EqualFold(filepath.Ext(p), ".exe") && !strings.EqualFold(d.Name(), "winws.exe") {
			return nil // не тащим сам ZapretAuto.exe и прочие exe
		}
		return copyFile(p, t)
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}

// migrateUserFiles переносит пользовательские списки и выбранные «фейки»
// из старой версии в новую, чтобы обновление ничего не сбрасывало.
func migrateUserFiles(oldDir, newDir string) {
	if oldDir == "" || oldDir == newDir {
		return
	}
	for _, rel := range []string{
		filepath.Join("lists", "ipset-exclude-user.txt"),
		filepath.Join("lists", "list-general-user.txt"),
		filepath.Join("lists", "list-exclude-user.txt"),
	} {
		if _, err := os.Stat(filepath.Join(oldDir, rel)); err == nil {
			_ = copyFile(filepath.Join(oldDir, rel), filepath.Join(newDir, rel))
		}
	}
}

// cleanupReleases удаляет всё, кроме текущей и предыдущей версий.
func cleanupReleases(keep ...string) {
	entries, err := os.ReadDir(releasesDir())
	if err != nil {
		return
	}
	for _, e := range entries {
		k := false
		for _, v := range keep {
			if v != "" && e.Name() == v {
				k = true
			}
		}
		if !k {
			_ = os.RemoveAll(filepath.Join(releasesDir(), e.Name()))
		}
	}
}
