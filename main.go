//go:build windows

// ZapretAuto — тихая обёртка над zapret-discord-youtube (Flowseal).
//
// При первом запуске программа один раз запрашивает права (UAC), ставит себя
// службой Windows и спрашивает, запускаться ли при включении компьютера.
// Дальше служба работает молча: сама скачивает последнюю версию zapret,
// подбирает рабочую стратегию, следит за winws.exe и обновляется.
// Окно программы показывает состояние и позволяет включить/выключить обход.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

const (
	serviceName = "ZapretAuto"
	displayName = "Zapret Auto (DPI bypass)"
	description = "Автоматически обновляемый zapret-discord-youtube: обход DPI для Discord и YouTube"
)

func main() {
	cmd := ""
	if len(os.Args) > 1 {
		cmd = strings.ToLower(strings.TrimLeft(os.Args[1], "-/"))
	}

	// Служба запускается с аргументом "service"; проверка IsWindowsService — для старых установок без него.
	if cmd == "service" {
		runService()
		return
	}
	if isService, err := svc.IsWindowsService(); err == nil && isService {
		runService()
		return
	}

	switch cmd {
	case "":
		// Обычный запуск (двойной клик, ярлык): при необходимости установить/обновить, затем открыть окно.
		needInstall := !queryService().Installed ||
			compareVersions(strings.TrimPrefix(appVersion, "v"), installedVersion()) > 0
		if needInstall {
			if err := installFromUI(); err != nil {
				if err != errCancelled {
					messageBox("ZapretAuto", "Не удалось установить:\n\n"+err.Error()+"\n\nПодробности: "+logPath(), true)
				}
				os.Exit(1)
			}
			// Окно открываем уже из установленной копии — скачанный файл можно сразу удалить.
			if !isInstalledCopy() {
				if err := startDetached(installedExe()); err == nil {
					return
				}
			}
		}
		runGUI(false)
	case "tray":
		// Автозагрузка при входе в Windows: только значок в трее, без окна.
		if queryService().Installed {
			runGUI(true)
		}
	case "install":
		requireAdminOrExit()
		if err := install(); err != nil {
			messageBox("ZapretAuto", "Не удалось установить:\n\n"+err.Error()+"\n\nПодробности: "+logPath(), true)
			os.Exit(1)
		}
	case "uninstall", "remove":
		if !isAdmin() {
			if err := runElevatedWait("uninstall"); err != nil && err != errCancelled {
				messageBox("ZapretAuto", "Ошибка удаления:\n\n"+err.Error(), true)
			}
			return
		}
		if err := uninstall(); err != nil {
			messageBox("ZapretAuto", "Ошибка удаления:\n\n"+err.Error(), true)
			os.Exit(1)
		}
		notify("ZapretAuto удалён полностью.\n\nЕсли вы добавляли папку в исключения Касперского или другого антивируса, " +
			"эту запись можно оттуда убрать.")
	case "start", "stop":
		// Запасной путь для кнопки «Включить/Выключить», если у пользователя нет прав на службу.
		requireAdminOrExit()
		m, err := mgr.Connect()
		if err != nil {
			os.Exit(1)
		}
		defer m.Disconnect()
		s, err := m.OpenService(serviceName)
		if err != nil {
			os.Exit(1)
		}
		defer s.Close()
		if cmd == "start" {
			err = s.Start()
		} else {
			stopService(s)
		}
		if err != nil {
			logf("%s: %v", cmd, err)
			os.Exit(1)
		}
	case "autostart":
		requireAdminOrExit()
		on := len(os.Args) > 2 && strings.EqualFold(os.Args[2], "on")
		if err := setAutostart(on); err != nil {
			logf("автозапуск: %v", err)
			os.Exit(1)
		}
	case "run", "debug":
		// Запуск в консоли без службы — для отладки.
		runConsole()
	case "update":
		requireAdminConsole()
		forceUpdateCheck()
	case "select", "autoselect":
		requireAdminConsole()
		restartWithAutoselect()
	case "restart-service":
		// Служебная команда: перезапуск после самообновления.
		if err := restartService(); err != nil {
			logf("перезапуск службы: %v", err)
		}
	case "status":
		printStatus()
	default:
		attachConsole()
		fmt.Println(`ZapretAuto — использование:
  ZapretAuto.exe             открыть программу (при первом запуске — установить)
  ZapretAuto.exe uninstall   остановить и удалить службу
  ZapretAuto.exe status      показать версию, стратегию и состояние
  ZapretAuto.exe update      проверить обновление сейчас
  ZapretAuto.exe select      заново подобрать стратегию
  ZapretAuto.exe run         запустить в консоли (отладка)`)
	}
}

// installFromUI ставит программу: сразу, если уже есть права администратора, иначе через UAC.
func installFromUI() error {
	if isAdmin() {
		return install()
	}
	return runElevatedWait("install")
}

func isInstalledCopy() bool {
	self, _ := os.Executable()
	return strings.EqualFold(filepath.Clean(self), filepath.Clean(installedExe()))
}

func startDetached(exe string) error {
	cmd := exec.Command(exe)
	cmd.Dir = os.Getenv("SystemRoot") // не держим папку программы рабочей — её можно будет удалить
	return cmd.Start()
}

func requireAdminOrExit() {
	if !isAdmin() {
		os.Exit(1)
	}
}

func requireAdminConsole() {
	if !isAdmin() {
		fmt.Println("Нужны права администратора: запустите из консоли от имени администратора.")
		os.Exit(1)
	}
}
