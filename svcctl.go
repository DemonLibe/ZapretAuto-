//go:build windows

package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Пользовательские коды управления: включить/выключить автозапуск, пока служба работает.
const (
	ctlAutostartOn  svc.Cmd = 130
	ctlAutostartOff svc.Cmd = 131
)

// Права службы: как у Windows по умолчанию, плюс пользователям за компьютером (IU)
// разрешено запускать и останавливать её (RP, WP) — чтобы кнопка «Включить/Выключить»
// работала без запроса UAC. Менять настройки службы (путь к exe и т.п.) им по-прежнему нельзя.
const serviceSDDL = "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)" +
	"(A;;CCLCSWRPWPDTLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)"

func allowUsersStartStop(s *mgr.Service) {
	sd, err := windows.SecurityDescriptorFromString(serviceSDDL)
	if err != nil {
		return
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return
	}
	if err := windows.SetSecurityInfo(s.Handle, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION,
		nil, nil, dacl, nil); err != nil {
		logf("права службы: %v", err)
	}
}

// ServiceInfo — что видно о службе без прав администратора.
type ServiceInfo struct {
	Installed bool
	State     uint32 // windows.SERVICE_RUNNING и т.п.
	Autostart bool
}

func queryService() ServiceInfo {
	var info ServiceInfo
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return info
	}
	defer windows.CloseServiceHandle(scm)
	name, _ := windows.UTF16PtrFromString(serviceName)
	h, err := windows.OpenService(scm, name, windows.SERVICE_QUERY_STATUS|windows.SERVICE_QUERY_CONFIG)
	if err != nil {
		return info
	}
	defer windows.CloseServiceHandle(h)
	info.Installed = true

	var st windows.SERVICE_STATUS
	if windows.QueryServiceStatus(h, &st) == nil {
		info.State = st.CurrentState
	}
	var need uint32
	_ = windows.QueryServiceConfig(h, nil, 0, &need)
	if need > 0 {
		buf := make([]byte, need)
		cfg := (*windows.QUERY_SERVICE_CONFIG)(unsafe.Pointer(&buf[0]))
		if windows.QueryServiceConfig(h, cfg, need, &need) == nil {
			info.Autostart = cfg.StartType == windows.SERVICE_AUTO_START
		}
	}
	return info
}

func serviceRunning() bool { return queryService().State == windows.SERVICE_RUNNING }

// openServiceAs открывает службу с нужными правами без mgr.Connect (тот требует прав администратора).
func openServiceAs(access uint32) (windows.Handle, func(), error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return 0, nil, err
	}
	name, _ := windows.UTF16PtrFromString(serviceName)
	h, err := windows.OpenService(scm, name, access)
	if err != nil {
		windows.CloseServiceHandle(scm)
		return 0, nil, err
	}
	return h, func() { windows.CloseServiceHandle(h); windows.CloseServiceHandle(scm) }, nil
}

// startServiceUser/stopServiceUser работают из обычного окна; при отказе в доступе — через UAC.
func startServiceUser() error {
	h, done, err := openServiceAs(windows.SERVICE_START)
	if err == nil {
		defer done()
		err = windows.StartService(h, 0, nil)
		if errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
			return nil
		}
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return runElevatedWait("start")
	}
	return err
}

func stopServiceUser() error {
	h, done, err := openServiceAs(windows.SERVICE_STOP)
	if err == nil {
		defer done()
		var st windows.SERVICE_STATUS
		err = windows.ControlService(h, windows.SERVICE_CONTROL_STOP, &st)
		if errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) {
			return nil
		}
	}
	if errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		return runElevatedWait("stop")
	}
	return err
}

// setAutostartUser: если служба работает — просит её саму сменить тип запуска (без UAC),
// иначе меняет через UAC.
func setAutostartUser(on bool) error {
	code := uint32(ctlAutostartOff)
	arg := "off"
	if on {
		code, arg = uint32(ctlAutostartOn), "on"
	}
	if serviceRunning() {
		h, done, err := openServiceAs(windows.SERVICE_USER_DEFINED_CONTROL)
		if err == nil {
			defer done()
			var st windows.SERVICE_STATUS
			if err = windows.ControlService(h, code, &st); err == nil {
				for i := 0; i < 20 && queryService().Autostart != on; i++ {
					time.Sleep(100 * time.Millisecond)
				}
				return nil
			}
		}
	}
	return runElevatedWait("autostart " + arg)
}

// setAutostart меняет только тип запуска службы (нужны права администратора или SYSTEM).
func setAutostart(on bool) error {
	h, done, err := openServiceAs(windows.SERVICE_CHANGE_CONFIG)
	if err != nil {
		return err
	}
	defer done()
	startType := uint32(windows.SERVICE_DEMAND_START)
	if on {
		startType = windows.SERVICE_AUTO_START
	}
	const noChange = windows.SERVICE_NO_CHANGE
	if err := windows.ChangeServiceConfig(h, noChange, startType, noChange, nil, nil, nil, nil, nil, nil, nil); err != nil {
		return err
	}
	setTrayAutorun(on)
	logf("автозапуск: %v", on)
	return nil
}

const runKey = `Software\Microsoft\Windows\CurrentVersion\Run`

// setTrayAutorun: при включённом автозапуске значок ZapretAuto появляется в трее при входе в Windows.
func setTrayAutorun(on bool) {
	k, _, err := registry.CreateKey(registry.LOCAL_MACHINE, runKey, registry.SET_VALUE)
	if err != nil {
		logf("автозагрузка значка: %v", err)
		return
	}
	defer k.Close()
	if on {
		_ = k.SetStringValue("ZapretAuto", `"`+installedExe()+`" tray`)
	} else {
		_ = k.DeleteValue("ZapretAuto")
	}
}

// Ярлыки в меню «Пуск» и на рабочем столе — чтобы программу было где найти после установки.
func shortcutPaths() []string {
	pd := os.Getenv("ProgramData")
	pub := os.Getenv("PUBLIC")
	var paths []string
	if pd != "" {
		paths = append(paths, filepath.Join(pd, `Microsoft\Windows\Start Menu\Programs`, "ZapretAuto.lnk"))
	}
	if pub != "" {
		paths = append(paths, filepath.Join(pub, "Desktop", "ZapretAuto.lnk"))
	}
	return paths
}

func createShortcuts() {
	for _, p := range shortcutPaths() {
		script := "$s=(New-Object -ComObject WScript.Shell).CreateShortcut('" + p + "');" +
			"$s.TargetPath='" + installedExe() + "';$s.WorkingDirectory='" + os.Getenv("SystemRoot") + "';" +
			"$s.IconLocation='" + installedExe() + ",0';$s.Description='Discord и YouTube без блокировок';$s.Save()"
		if err := runHidden("powershell", "-NoProfile", "-NonInteractive", "-Command", script); err != nil {
			logf("ярлык %s: %v", p, err)
		}
	}
}

func removeShortcuts() {
	for _, p := range shortcutPaths() {
		_ = os.Remove(p)
	}
}

// runElevatedWait запускает этот же exe с аргументами через UAC и ждёт завершения.
func runElevatedWait(args string) error {
	exe, _ := os.Executable()
	verb, _ := windows.UTF16PtrFromString("runas")
	file, _ := windows.UTF16PtrFromString(exe)
	params, _ := windows.UTF16PtrFromString(args)
	info := &shellExecuteInfo{
		mask:       seeMaskNoCloseProcess,
		verb:       verb,
		file:       file,
		parameters: params,
		show:       windows.SW_HIDE,
	}
	info.size = uint32(unsafe.Sizeof(*info))
	if r, _, _ := procShellExecuteEx.Call(uintptr(unsafe.Pointer(info))); r == 0 || info.process == 0 {
		return errCancelled // пользователь нажал «Нет» в окне UAC
	}
	defer windows.CloseHandle(info.process)
	_, _ = windows.WaitForSingleObject(info.process, windows.INFINITE)
	var code uint32
	if windows.GetExitCodeProcess(info.process, &code) == nil && code != 0 {
		return errors.New("операция не выполнена — подробности в журнале")
	}
	return nil
}

// SHELLEXECUTEINFOW (в golang.org/x/sys его нет).
type shellExecuteInfo struct {
	size         uint32
	mask         uint32
	hwnd         uintptr
	verb         *uint16
	file         *uint16
	parameters   *uint16
	directory    *uint16
	show         int32
	instApp      uintptr
	idList       uintptr
	class        *uint16
	keyClass     uintptr
	hotKey       uint32
	iconOrMonitr uintptr
	process      windows.Handle
}

const seeMaskNoCloseProcess = 0x00000040

var procShellExecuteEx = windows.NewLazySystemDLL("shell32.dll").NewProc("ShellExecuteExW")

var errCancelled = errors.New("отменено")

// killInstalledInstances закрывает запущенные копии установленного exe (значок в трее, окно),
// кроме текущего процесса — чтобы при обновлении и удалении файл не был занят.
func killInstalledInstances() {
	snap, err := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if err != nil {
		return
	}
	defer windows.CloseHandle(snap)
	target := strings.ToLower(filepath.Clean(installedExe()))
	self := uint32(os.Getpid())
	var pe windows.ProcessEntry32
	pe.Size = uint32(unsafe.Sizeof(pe))
	for err = windows.Process32First(snap, &pe); err == nil; err = windows.Process32Next(snap, &pe) {
		if pe.ProcessID == self || !strings.EqualFold(windows.UTF16ToString(pe.ExeFile[:]), "ZapretAuto.exe") {
			continue
		}
		h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.PROCESS_TERMINATE|windows.SYNCHRONIZE, false, pe.ProcessID)
		if err != nil {
			continue
		}
		buf := make([]uint16, windows.MAX_PATH)
		n := uint32(len(buf))
		if windows.QueryFullProcessImageName(h, 0, &buf[0], &n) == nil &&
			strings.ToLower(filepath.Clean(windows.UTF16ToString(buf[:n]))) == target {
			_ = windows.TerminateProcess(h, 0)
			_, _ = windows.WaitForSingleObject(h, 5000)
		}
		windows.CloseHandle(h)
	}
}
