//go:build windows

// ZapretAuto — тихая обёртка над zapret-discord-youtube (Flowseal).
//
// Двойной клик по exe: один запрос UAC, затем программа ставит себя как
// службу Windows и дальше работает молча: сама скачивает последнюю версию
// zapret, подбирает рабочую стратегию, следит за winws.exe и обновляется.
package main

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/sys/windows/svc"
)

const (
	serviceName = "ZapretAuto"
	displayName = "Zapret Auto (DPI bypass)"
	description = "Автоматически обновляемый zapret-discord-youtube: обход DPI для Discord и YouTube"
)

func main() {
	isService, err := svc.IsWindowsService()
	if err == nil && isService {
		runService()
		return
	}

	cmd := ""
	if len(os.Args) > 1 {
		cmd = strings.ToLower(strings.TrimLeft(os.Args[1], "-/"))
	}

	switch cmd {
	case "", "install":
		if !isAdmin() {
			relaunchElevated(cmd)
			return
		}
		if err := install(); err != nil {
			messageBox("ZapretAuto", "Не удалось установить:\n\n"+err.Error(), true)
			os.Exit(1)
		}
		if cmd == "" {
			messageBox("ZapretAuto", "Готово. Zapret установлен как служба и будет работать и обновляться сам.\n\n"+
				"Удалить: запустите ZapretAuto.exe uninstall\nЛог: "+logPath(), false)
		}
	case "uninstall", "remove":
		if !isAdmin() {
			relaunchElevated(cmd)
			return
		}
		if err := uninstall(); err != nil {
			messageBox("ZapretAuto", "Ошибка удаления:\n\n"+err.Error(), true)
			os.Exit(1)
		}
		messageBox("ZapretAuto", "ZapretAuto удалён.", false)
	case "run", "debug":
		// Запуск в консоли без службы — для отладки.
		runConsole()
	case "update":
		requireAdminConsole()
		forceUpdateCheck()
	case "select", "autoselect":
		requireAdminConsole()
		restartWithAutoselect()
	case "status":
		printStatus()
	default:
		fmt.Println(`ZapretAuto — использование:
  ZapretAuto.exe             установить и запустить (двойной клик)
  ZapretAuto.exe uninstall   остановить и удалить службу
  ZapretAuto.exe status      показать версию, стратегию и состояние
  ZapretAuto.exe update      проверить обновление сейчас
  ZapretAuto.exe select      заново подобрать стратегию
  ZapretAuto.exe run         запустить в консоли (отладка)`)
	}
}

func requireAdminConsole() {
	if !isAdmin() {
		fmt.Println("Нужны права администратора: запустите из консоли от имени администратора.")
		os.Exit(1)
	}
}
