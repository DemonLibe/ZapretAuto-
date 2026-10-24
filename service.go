//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Пользовательские коды управления службой (128–255).
const (
	ctlUpdate svc.Cmd = 128
	ctlSelect svc.Cmd = 129
)

type handler struct{}

func (handler) Execute(_ []string, r <-chan svc.ChangeRequest, s chan<- svc.Status) (bool, uint32) {
	s <- svc.Status{State: svc.StartPending}
	eng := newEngine()
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() { eng.Run(stop); close(done) }()
	s <- svc.Status{State: svc.Running, Accepts: svc.AcceptStop | svc.AcceptShutdown}

	for c := range r {
		switch c.Cmd {
		case svc.Interrogate:
			s <- c.CurrentStatus
		case svc.Stop, svc.Shutdown:
			s <- svc.Status{State: svc.StopPending}
			close(stop)
			select {
			case <-done:
			case <-time.After(20 * time.Second):
			}
			return false, 0
		case ctlUpdate:
			eng.reqs <- reqUpdate
		case ctlSelect:
			eng.reqs <- reqSelect
		}
	}
	return false, 0
}

func runService() {
	if err := svc.Run(serviceName, handler{}); err != nil {
		logf("служба: %v", err)
	}
}

func runConsole() {
	attachConsole()
	if !isAdmin() {
		fmt.Println("Нужны права администратора.")
		return
	}
	if serviceRunning() {
		fmt.Println("Служба ZapretAuto уже работает. Сначала: ZapretAuto.exe uninstall")
		return
	}
	logConsole = true
	stop := make(chan struct{})
	ch := make(chan os.Signal, 1)
	signal.Notify(ch, os.Interrupt)
	go func() { <-ch; close(stop) }()
	newEngine().Run(stop)
}

func install() error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dataDir(), 0o755); err != nil {
		return err
	}
	logf("установка из %s", self)

	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()

	// Остановить старую копию, чтобы можно было перезаписать exe.
	if s, err := m.OpenService(serviceName); err == nil {
		stopService(s)
		_ = s.Delete()
		s.Close()
		waitServiceGone(m, serviceName)
	}

	// Оригинальная служба "zapret" из service.bat конфликтует (обе захватывают трафик через WinDivert).
	if s, err := m.OpenService("zapret"); err == nil {
		logf("удаляю старую службу zapret (из service.bat), чтобы не было конфликта")
		stopService(s)
		_ = s.Delete()
		s.Close()
	}
	killStrayWinws()

	if !strings.EqualFold(filepath.Clean(self), filepath.Clean(installedExe())) {
		if err := copyFile(self, installedExe()); err != nil {
			return fmt.Errorf("копирование exe: %w", err)
		}
	}

	// Если exe лежит в распакованной папке zapret — берём её как стартовую версию
	// (работает даже без доступа к GitHub). Иначе служба скачает всё сама.
	importNearby(filepath.Dir(self))
	loadConfig() // создаёт config.json с настройками по умолчанию

	s, err := m.CreateService(serviceName, installedExe(), mgr.Config{
		DisplayName:  displayName,
		Description:  description,
		StartType:    mgr.StartAutomatic,
		ErrorControl: mgr.ErrorNormal,
	})
	if err != nil {
		return fmt.Errorf("создание службы: %w", err)
	}
	defer s.Close()
	_ = s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: 10 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 2 * time.Minute},
	}, 24*60*60)
	_ = s.SetRecoveryActionsOnNonCrashFailures(true)

	recordTCPTimestamps()
	addDefenderExclusion()
	registerUninstallEntry()
	askThirdPartyAVExclusion()

	if err := s.Start(); err != nil {
		return fmt.Errorf("запуск службы: %w", err)
	}
	logf("служба ZapretAuto %s установлена и запущена", appVersion)
	return nil
}

// addDefenderExclusion: Защитник Windows часто удаляет winws.exe/WinDivert как «хакерский инструмент»,
// из-за чего обход молча перестаёт работать. Исключение — только для папки ZapretAuto.
func addDefenderExclusion() {
	if err := runHidden("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Add-MpPreference -ExclusionPath '"+dataDir()+"'"); err != nil {
		logf("исключение Защитника не добавлено (возможно, стоит другой антивирус): %v", err)
	}
}

func removeDefenderExclusion() {
	_ = runHidden("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Remove-MpPreference -ExclusionPath '"+dataDir()+"'")
}

// thirdPartyAV — установленные антивирусы, кроме Защитника Windows (по данным Центра безопасности).
func thirdPartyAV() []string {
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"Get-CimInstance -Namespace root/SecurityCenter2 -ClassName AntiVirusProduct | ForEach-Object { $_.displayName }")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	var names []string
	for _, l := range strings.Split(string(out), "\n") {
		l = strings.TrimSpace(l)
		if l != "" && !strings.Contains(strings.ToLower(l), "defender") {
			names = append(names, l)
		}
	}
	return names
}

// askThirdPartyAVExclusion: сторонние антивирусы (Касперский, ESET, Dr.Web…) защищают свои настройки
// от изменения программами, поэтому исключение добавить за пользователя нельзя. Показываем
// короткую инструкцию один раз при установке — до того, как zapret будет скачан, — и кладём путь в буфер обмена.
func askThirdPartyAVExclusion() {
	avs := thirdPartyAV()
	if len(avs) == 0 {
		return
	}
	logf("найден антивирус: %s", strings.Join(avs, ", "))
	_ = runHidden("powershell", "-NoProfile", "-NonInteractive", "-Command", "Set-Clipboard -Value '"+dataDir()+"'")

	steps := "Откройте антивирус → Настройки → Исключения → Добавить папку\n" +
		"и вставьте путь (Ctrl+V)."
	for _, a := range avs {
		if strings.Contains(strings.ToLower(a), "kaspersky") || strings.Contains(strings.ToLower(a), "касперск") {
			steps = "1. Откройте Касперский → ⚙ Настройки → Настройки безопасности.\n" +
				"2. «Исключения и действия при обнаружении объектов» → «Управлять исключениями».\n" +
				"3. «Добавить» → в поле «Файл или папка» нажмите Ctrl+V.\n" +
				"4. Отметьте все компоненты защиты → «Добавить»."
			break
		}
	}
	messageBox("ZapretAuto — один шаг",
		"У вас установлен "+strings.Join(avs, ", ")+".\n"+
			"Он может удалить файлы обхода блокировок, и тогда ничего не заработает.\n\n"+
			"Добавьте папку в исключения (путь уже скопирован):\n"+dataDir()+"\n\n"+
			steps+"\n\nПотом нажмите OK.", false)
}

const uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\ZapretAuto`

// registerUninstallEntry добавляет ZapretAuto в «Параметры → Приложения»,
// чтобы удалять его обычной кнопкой «Удалить».
func registerUninstallEntry() {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallKey, registry.SET_VALUE)
	if err != nil {
		logf("запись в «Приложения»: %v", err)
		return
	}
	defer k.Close()
	exe := installedExe()
	_ = k.SetStringValue("DisplayName", "ZapretAuto — Discord и YouTube без блокировок")
	_ = k.SetStringValue("DisplayVersion", strings.TrimPrefix(appVersion, "v"))
	_ = k.SetStringValue("Publisher", "DemonLibe")
	_ = k.SetStringValue("DisplayIcon", exe)
	_ = k.SetStringValue("InstallLocation", dataDir())
	_ = k.SetStringValue("UninstallString", `"`+exe+`" uninstall`)
	_ = k.SetStringValue("URLInfoAbout", "https://github.com/"+appRepo)
	_ = k.SetDWordValue("NoModify", 1)
	_ = k.SetDWordValue("NoRepair", 1)
	_ = k.SetDWordValue("EstimatedSize", 12*1024)
}

func importNearby(dir string) {
	candidates := []string{dir, filepath.Dir(dir)}
	if matches, _ := filepath.Glob(filepath.Join(dir, "zapret-discord-youtube*")); matches != nil {
		candidates = append(candidates, matches...)
	}
	for _, c := range candidates {
		if ver, err := importLocalRelease(c); err == nil {
			logf("взял локальную копию zapret %s из %s", ver, c)
			return
		}
	}
}

func uninstall() error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	if s, err := m.OpenService(serviceName); err == nil {
		stopService(s)
		if err := s.Delete(); err != nil {
			s.Close()
			return err
		}
		s.Close()
	}
	killStrayWinws()
	// Драйвер WinDivert выгружаем так же, как "Remove Services" в service.bat.
	for _, name := range []string{"WinDivert", "WinDivert14"} {
		if s, err := m.OpenService(name); err == nil {
			stopService(s)
			_ = s.Delete()
			s.Close()
		}
	}
	time.Sleep(time.Second)
	removeDefenderExclusion()
	restoreTCPTimestamps()
	_ = registry.DeleteKey(registry.LOCAL_MACHINE, uninstallKey)

	// Удаляем всю папку: версии zapret, настройки, логи и сам exe.
	_ = os.RemoveAll(dataDir())
	if _, err := os.Stat(dataDir()); err == nil {
		// Exe, из которого идёт удаление, сейчас запущен и сам себя стереть не может —
		// дочищает скрытый cmd после выхода программы (и на всякий случай — при перезагрузке).
		deleteAfterExit(dataDir())
		for _, p := range []string{installedExe(), dataDir()} {
			if u, err := windows.UTF16PtrFromString(p); err == nil {
				_ = windows.MoveFileEx(u, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
			}
		}
	}
	return nil
}

// deleteAfterExit запускает скрытый cmd, который до минуты пытается удалить папку,
// пока не завершится процесс, держащий в ней файлы.
func deleteAfterExit(dir string) {
	script := `/d /c for /l %i in (1,1,60) do @(if exist "` + dir + `" (ping -n 2 127.0.0.1 >nul & rmdir /s /q "` + dir + `" 2>nul))`
	cmd := exec.Command(os.Getenv("ComSpec"))
	if cmd.Path == "" {
		cmd = exec.Command("cmd.exe")
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: createNoWindow | 0x00000008, // DETACHED_PROCESS
		CmdLine:       `cmd.exe ` + script,
	}
	cmd.Dir = os.Getenv("SystemRoot")
	_ = cmd.Start()
}

// recordTCPTimestamps запоминает (один раз, до первого запуска службы), были ли включены
// TCP timestamps, чтобы при удалении вернуть как было.
func recordTCPTimestamps() {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, uninstallKey, registry.QUERY_VALUE|registry.SET_VALUE)
	if err != nil {
		return
	}
	defer k.Close()
	if _, _, err := k.GetStringValue("TcpTimestampsBefore"); err == nil {
		return
	}
	cmd := exec.Command("powershell", "-NoProfile", "-NonInteractive", "-Command",
		"(Get-NetTCPSetting -SettingName Internet -ErrorAction SilentlyContinue).Timestamps")
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	out, err := cmd.Output()
	v := strings.TrimSpace(string(out))
	if err != nil || (v != "Enabled" && v != "Disabled") {
		v = "Unknown"
	}
	_ = k.SetStringValue("TcpTimestampsBefore", v)
}

func restoreTCPTimestamps() {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return
	}
	v, _, _ := k.GetStringValue("TcpTimestampsBefore")
	k.Close()
	if v == "Disabled" {
		_ = runHidden("netsh", "interface", "tcp", "set", "global", "timestamps=disabled")
	}
}

func stopService(s *mgr.Service) {
	st, err := s.Control(svc.Stop)
	if err != nil {
		return
	}
	for i := 0; i < 60 && st.State != svc.Stopped; i++ {
		time.Sleep(500 * time.Millisecond)
		if st, err = s.Query(); err != nil {
			return
		}
	}
}

func waitServiceGone(m *mgr.Mgr, name string) {
	for i := 0; i < 20; i++ {
		s, err := m.OpenService(name)
		if err != nil {
			return
		}
		s.Close()
		time.Sleep(500 * time.Millisecond)
	}
}

func sendControl(c svc.Cmd) error {
	m, err := mgr.Connect()
	if err != nil {
		return err
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err != nil {
		return errors.New("служба ZapretAuto не установлена")
	}
	defer s.Close()
	_, err = s.Control(c)
	return err
}

func forceUpdateCheck() {
	attachConsole()
	if err := sendControl(ctlUpdate); err != nil {
		fmt.Println("Ошибка:", err)
		return
	}
	fmt.Println("Проверка обновлений запущена, результат — в", logPath())
}

func restartWithAutoselect() {
	attachConsole()
	if err := sendControl(ctlSelect); err != nil {
		fmt.Println("Ошибка:", err)
		return
	}
	fmt.Println("Автоподбор стратегии запущен (до пары минут), результат — в", logPath())
}

func serviceRunning() bool {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString(serviceName)
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS)
	if err != nil {
		return false
	}
	defer windows.CloseServiceHandle(h)
	var st windows.SERVICE_STATUS
	if windows.QueryServiceStatus(h, &st) != nil {
		return false
	}
	return st.CurrentState == windows.SERVICE_RUNNING
}

func printStatus() {
	attachConsole()
	st := loadState()
	cfg := loadConfig()
	state := "не работает / не установлена"
	if serviceRunning() {
		state = "работает"
	}
	strategy := st.Strategy
	if cfg.Strategy != "" {
		strategy = cfg.Strategy + " (задана в config.json)"
	}
	fmt.Printf("\nZapretAuto %s\nСлужба: %s\nВерсия zapret:     %s\nСтратегия:         %s\nПоследняя проверка обновлений: %s\nПапка:             %s\n",
		appVersion, state, st.Version, strategy, st.LastCheck.Format("2006-01-02 15:04"), dataDir())
}
