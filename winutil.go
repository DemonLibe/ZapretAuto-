//go:build windows

package main

import (
	"os"

	"golang.org/x/sys/windows"
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
