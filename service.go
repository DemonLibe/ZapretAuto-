//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/windows"
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
	if err := s.Start(); err != nil {
		return fmt.Errorf("запуск службы: %w", err)
	}
	logf("служба установлена и запущена")
	return nil
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
	_ = os.RemoveAll(releasesDir())
	_ = os.Remove(statePath())
	// Сам exe может быть запущен прямо сейчас — удаляем его при перезагрузке.
	if p, err := windows.UTF16PtrFromString(installedExe()); err == nil {
		if os.Remove(installedExe()) != nil {
			_ = windows.MoveFileEx(p, nil, windows.MOVEFILE_DELAY_UNTIL_REBOOT)
		}
	}
	return nil
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
	fmt.Printf("\nСлужба ZapretAuto: %s\nВерсия zapret:     %s\nСтратегия:         %s\nПоследняя проверка обновлений: %s\nПапка:             %s\n",
		state, st.Version, strategy, st.LastCheck.Format("2006-01-02 15:04"), dataDir())
}
