//go:build windows

package main

import (
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

func isAdmin() bool {
	return windows.GetCurrentProcessToken().IsElevated()
}

// relaunchElevated перезапускает exe с тем же аргументом через UAC.
func relaunchElevated(arg string) {
	exe, _ := os.Executable()
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	args, _ := windows.UTF16PtrFromString(arg)
	if err := windows.ShellExecute(0, verb, file, args, nil, windows.SW_HIDE); err != nil {
		// Пользователь нажал «Нет» в окне UAC — молча выходим.
		os.Exit(1)
	}
}

func messageBox(title, text string, isError bool) {
	t, _ := windows.UTF16PtrFromString(title)
	m, _ := windows.UTF16PtrFromString(text)
	flags := uint32(windows.MB_OK | windows.MB_ICONINFORMATION)
	if isError {
		flags = windows.MB_OK | windows.MB_ICONERROR
	}
	windows.MessageBox(0, m, t, flags)
}

var (
	kernel32          = windows.NewLazySystemDLL("kernel32.dll")
	procAttachConsole = kernel32.NewProc("AttachConsole")
)

// attachConsole: exe собран как GUI (без мигающего окна консоли при двойном клике),
// поэтому для консольных команд подключаемся к консоли, из которой нас запустили.
func attachConsole() {
	const attachParentProcess = ^uintptr(0) // (DWORD)-1
	if r, _, _ := procAttachConsole.Call(attachParentProcess); r == 0 {
		return
	}
	if f, err := os.OpenFile("CONOUT$", os.O_WRONLY, 0); err == nil {
		os.Stdout, os.Stderr = f, f
	}
	_ = windows.SetConsoleOutputCP(65001)
}

var procMessageBoxTimeout = windows.NewLazySystemDLL("user32.dll").NewProc("MessageBoxTimeoutW")

// notify показывает информационное окно, которое само закрывается через 8 секунд.
func notify(text string) {
	t, _ := windows.UTF16PtrFromString("ZapretAuto")
	m, _ := windows.UTF16PtrFromString(text)
	const flags = windows.MB_OK | windows.MB_ICONINFORMATION | windows.MB_SETFOREGROUND | windows.MB_TOPMOST
	if procMessageBoxTimeout.Find() != nil {
		windows.MessageBox(0, m, t, flags)
		return
	}
	procMessageBoxTimeout.Call(0, uintptr(unsafe.Pointer(m)), uintptr(unsafe.Pointer(t)), flags, 0, 8000)
}

// installedVersion — версия установленного ZapretAuto (из «Приложений»), "" если не установлен.
func installedVersion() string {
	k, err := registry.OpenKey(registry.LOCAL_MACHINE, uninstallKey, registry.QUERY_VALUE)
	if err != nil {
		return ""
	}
	defer k.Close()
	v, _, _ := k.GetStringValue("DisplayVersion")
	return v
}
