//go:build windows

package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Репозиторий, из релизов которого обновляется сам ZapretAuto.exe.
const appRepo = "DemonLibe/ZapretAuto-"

// appVersion подставляется при сборке: -ldflags "-X main.appVersion=v1.0.1".
var appVersion = "dev"

// latestAppVersion узнаёт тег последнего релиза по редиректу /releases/latest
// (без GitHub API и его лимитов).
func latestAppVersion() (string, error) {
	c := &http.Client{
		Timeout: 30 * time.Second,
		// Следуем редиректам (например, из-за регистра в имени репозитория), пока не дойдём до /tag/.
		CheckRedirect: func(r *http.Request, via []*http.Request) error {
			if strings.Contains(r.URL.Path, "/releases/tag/") || len(via) > 5 {
				return http.ErrUseLastResponse
			}
			return nil
		},
	}
	resp, err := c.Get("https://github.com/" + appRepo + "/releases/latest")
	if err != nil {
		return "", err
	}
	resp.Body.Close()
	loc := resp.Header.Get("Location")
	i := strings.LastIndex(loc, "/tag/")
	if i < 0 {
		return "", fmt.Errorf("релизов пока нет (HTTP %d)", resp.StatusCode)
	}
	return loc[i+len("/tag/"):], nil
}

// selfUpdate скачивает новый ZapretAuto.exe, подменяет установленный и
// перезапускает службу через отдельный процесс. Возвращает true, если обновление началось.
func selfUpdate() bool {
	if appVersion == "dev" {
		return false
	}
	tag, err := latestAppVersion()
	if err != nil {
		logf("самообновление: %v", err)
		return false
	}
	if compareVersions(strings.TrimPrefix(tag, "v"), strings.TrimPrefix(appVersion, "v")) <= 0 {
		return false
	}
	if bad, _ := os.ReadFile(skipVersionPath()); strings.TrimSpace(string(bad)) == tag {
		return false // эта версия уже не запустилась у нас — ждём следующую
	}
	logf("самообновление: вышел ZapretAuto %s (у нас %s)", tag, appVersion)

	exe := installedExe()
	newExe, oldExe := exe+".new", exe+".old"
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/ZapretAuto.exe", appRepo, tag)
	if err := downloadFile(url, newExe); err != nil {
		logf("самообновление: скачивание: %v", err)
		os.Remove(newExe)
		return false
	}
	if head, err := os.ReadFile(newExe); err != nil || len(head) < 1<<20 || !bytes.HasPrefix(head, []byte("MZ")) {
		logf("самообновление: скачанный файл не похож на exe")
		os.Remove(newExe)
		return false
	}
	// Запущенный exe нельзя удалить, но можно переименовать.
	os.Remove(oldExe)
	if err := os.Rename(exe, oldExe); err != nil {
		logf("самообновление: %v", err)
		return false
	}
	if err := os.Rename(newExe, exe); err != nil {
		_ = os.Rename(oldExe, exe)
		logf("самообновление: %v", err)
		return false
	}
	cmd := exec.Command(exe, "restart-service")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		logf("самообновление: перезапуск: %v", err)
		return false
	}
	logf("самообновление: перезапускаюсь в версии %s", tag)
	return true
}

// restartService вызывается новым exe после самообновления: перезапускает службу,
// а если новая версия не стартует — возвращает старый exe.
func restartService() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return err
	}
	defer s.Close()

	stopService(s)
	if startAndWait(s) {
		logf("самообновление: служба работает в версии %s", appVersion)
		return nil
	}
	logf("самообновление: новая версия не запустилась, возвращаю старую")
	stopService(s)
	_ = os.WriteFile(skipVersionPath(), []byte(appVersion), 0o644)
	exe := installedExe()
	if _, err := os.Stat(exe + ".old"); err == nil {
		os.Remove(exe + ".bad")
		_ = os.Rename(exe, exe+".bad")
		_ = os.Rename(exe+".old", exe)
	}
	if !startAndWait(s) {
		return errors.New("служба не запускается")
	}
	return nil
}

func skipVersionPath() string { return installedExe() + ".skip" }

func startAndWait(s *mgr.Service) bool {
	if err := s.Start(); err != nil {
		return false
	}
	// Проверяем, что служба не упала сразу после старта.
	time.Sleep(15 * time.Second)
	st, err := s.Query()
	return err == nil && st.State == svc.Running
}
