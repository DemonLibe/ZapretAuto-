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
	case "":
		// Повторный двойной клик: если служба уже работает и этот exe не новее установленного,
		// ничего не трогаем (и не откатываем самообновившуюся версию на старый скачанный файл).
		if serviceRunning() && compareVersions(strings.TrimPrefix(appVersion, "v"), installedVersion()) <= 0 {
			notify("Zapret уже работает ✔\n\nНичего делать не нужно — он запускается вместе с Windows и обновляется сам.")
			return
		}
		fallthrough
	case "install":
		if !isAdmin() {
			relaunchElevated(cmd)
			return
		}
		if err := install(); err != nil {
			messageBox("ZapretAuto", "Не удалось включить:\n\n"+err.Error()+"\n\nПодробности: "+logPath(), true)
			os.Exit(1)
		}
		if cmd == "" {
			notify("Zapret включён ✔\n\nЧерез минуту-две Discord и YouTube заработают. Программа запускается вместе с Windows " +
				"и обновляется сама — про неё можно забыть.\n\nУдалить: Параметры → Приложения → ZapretAuto.\n" +
				"Скачанный файл можно удалить.")
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
		notify("ZapretAuto удалён полностью.\n\nЕсли вы добавляли папку в исключения Касперского или другого антивируса, " +
			"эту запись можно оттуда убрать.")
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
