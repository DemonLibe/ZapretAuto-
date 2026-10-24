//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc/mgr"
)

const createNoWindow = 0x08000000

type request int

const (
	reqUpdate request = iota
	reqSelect
)

// Engine держит winws.exe запущенным, обновляет zapret и подбирает стратегию.
type Engine struct {
	cfg     Config
	st      State
	cmd     *exec.Cmd
	job     windows.Handle
	exited  chan error
	reqs    chan request
	stop    <-chan struct{}
	started time.Time
}

func newEngine() *Engine {
	return &Engine{reqs: make(chan request, 4)}
}

// Run работает, пока не закрыт канал stop.
func (e *Engine) Run(stop <-chan struct{}) {
	e.stop = stop
	e.cfg = loadConfig()
	e.st = loadState()
	logf("ZapretAuto запущен (версия zapret: %q, стратегия: %q)", e.st.Version, e.st.Strategy)
	defer unloadWinDivert() // выполняется последним — когда winws.exe уже закрыт
	defer e.stopWinws()
	defer e.setStatus("Выключен")

	enableTCPTimestamps()
	e.setStatus("Запускается…")

	// 1. Убедиться, что zapret скачан.
	for wait := time.Minute; !e.ensureRelease(); wait = min(wait*2, 30*time.Minute) {
		e.setStatus("Не удалось скачать zapret")
		logf("zapret ещё не скачан (нет интернета или антивирус удалил winws.exe — добавьте %s в исключения), повтор через %v", dataDir(), wait)
		select {
		case <-stop:
			return
		case <-time.After(wait):
		}
	}

	// 2. Обновиться, если пора, затем запустить обход.
	if time.Since(e.st.LastCheck) > time.Duration(e.cfg.UpdateIntervalHours)*time.Hour {
		e.checkUpdate()
	}
	if e.cmd == nil {
		if selected := e.startBest(); !selected && e.cmd != nil {
			e.verify()
		}
	}

	updateTick := time.NewTicker(time.Duration(e.cfg.UpdateIntervalHours) * time.Hour)
	defer updateTick.Stop()
	healthEvery := time.Duration(e.cfg.HealthIntervalMinutes) * time.Minute
	if healthEvery <= 0 {
		healthEvery = 24 * 365 * time.Hour
	}
	healthTick := time.NewTicker(healthEvery)
	defer healthTick.Stop()

	retryTick := time.NewTicker(time.Minute)
	defer retryTick.Stop()

	crashes, badChecks := 0, 0
	for {
		select {
		case <-stop:
			logf("остановка")
			return

		case err := <-e.exited:
			e.cmd, e.exited = nil, nil
			if time.Since(e.started) > 5*time.Minute {
				crashes = 0
			}
			logf("winws.exe завершился: %v", err)
			crashes = e.restartAfterFailure(crashes, stop)

		case <-retryTick.C:
			// Стратегия не запустилась (например, конфликт WinDivert) — пробуем снова.
			if e.cmd == nil {
				crashes = e.restartAfterFailure(crashes, stop)
			}

		case <-updateTick.C:
			e.checkUpdate()

		case <-healthTick.C:
			if e.cmd == nil {
				continue
			}
			ok := score(e.cfg.CheckTargets)
			if ok*2 >= len(e.cfg.CheckTargets) {
				badChecks = 0
				continue
			}
			if !internetUp(e.cfg.ControlTarget) {
				logf("проверка: нет интернета, стратегию не трогаю")
				continue
			}
			badChecks++
			logf("проверка: открылось %d из %d сайтов (плохих проверок подряд: %d)", ok, len(e.cfg.CheckTargets), badChecks)
			if badChecks >= 2 && e.cfg.AutoSelect && e.cfg.Strategy == "" {
				badChecks = 0
				e.autoselect()
			}

		case r := <-e.reqs:
			switch r {
			case reqUpdate:
				e.checkUpdate()
			case reqSelect:
				e.autoselect()
			}
		}
	}
}

// restartAfterFailure перезапускает winws с нарастающей паузой; после 3 неудач подряд
// на только что обновлённой версии откатывается на предыдущую.
func (e *Engine) restartAfterFailure(crashes int, stop <-chan struct{}) int {
	crashes++
	if crashes == 3 && e.st.PrevVersion != "" && isValidRelease(releaseDir(e.st.PrevVersion)) {
		logf("версия %s не запускается — откат на %s", e.st.Version, e.st.PrevVersion)
		e.st.Version, e.st.PrevVersion = e.st.PrevVersion, ""
		saveState(e.st)
	}
	wait := time.Duration(crashes*crashes) * 5 * time.Second
	if wait > 10*time.Minute {
		wait = 10 * time.Minute
	}
	logf("перезапуск #%d через %v", crashes, wait)
	if crashes >= 3 {
		e.setStatus("Не получается запустить обход")
	} else {
		e.setStatus("Перезапуск после сбоя…")
	}
	select {
	case <-stop:
		return crashes
	case <-time.After(wait):
	}
	e.startCurrent()
	return crashes
}

// alive — работает ли winws.exe прямо сейчас.
func (e *Engine) alive() bool {
	if e.cmd == nil {
		return false
	}
	select {
	case err := <-e.exited:
		e.exited <- err // вернуть, чтобы основной цикл тоже увидел завершение
		return false
	default:
		return true
	}
}

// setStatus сообщает окну программы, чем служба занята ("" — обход работает).
func (e *Engine) setStatus(format string, a ...any) {
	e.st.Status = fmt.Sprintf(format, a...)
	saveState(e.st)
}

func (e *Engine) root() string { return releaseDir(e.st.Version) }

// ensureRelease: есть ли рабочая копия zapret; если нет — импорт из соседней папки или скачивание.
func (e *Engine) ensureRelease() bool {
	if e.st.Version != "" && isValidRelease(e.root()) {
		return true
	}
	// Возможно, установщик уже положил релиз в releases\.
	if entries, err := os.ReadDir(releasesDir()); err == nil {
		for _, en := range entries {
			if en.IsDir() && isValidRelease(releaseDir(en.Name())) &&
				(e.st.Version == "" || compareVersions(en.Name(), e.st.Version) > 0 || !isValidRelease(e.root())) {
				e.st.Version = en.Name()
			}
		}
		if e.st.Version != "" && isValidRelease(e.root()) {
			saveState(e.st)
			return true
		}
	}
	e.setStatus("Скачиваю zapret…")
	ver, err := latestVersion(e.cfg.Repo)
	if err == nil {
		err = downloadRelease(e.cfg.Repo, ver)
	}
	if err != nil {
		logf("скачивание zapret: %v", err)
		return false
	}
	logf("скачан zapret %s", ver)
	e.st.Version = ver
	e.st.LastCheck = time.Now()
	saveState(e.st)
	return true
}

// checkUpdate — тихое обновление: скачать новую версию рядом, переключиться, старую оставить для отката.
func (e *Engine) checkUpdate() {
	e.st.LastCheck = time.Now()
	defer saveState(e.st)

	// Сначала — обновление самого ZapretAuto.exe (служба перезапустится сама).
	if selfUpdate() {
		return
	}

	latest, err := latestVersion(e.cfg.Repo)
	if err != nil {
		logf("проверка обновлений: %v", err)
		return
	}
	if compareVersions(latest, e.st.Version) <= 0 {
		logf("проверка обновлений: установлена последняя версия %s", e.st.Version)
		return
	}
	logf("найдена новая версия %s (текущая %s), скачиваю", latest, e.st.Version)
	if e.cmd == nil {
		e.setStatus("Обновляю zapret до %s…", latest)
	}
	if err := downloadRelease(e.cfg.Repo, latest); err != nil {
		logf("обновление не удалось: %v", err)
		return
	}
	migrateUserFiles(e.root(), releaseDir(latest))

	e.stopWinws()
	e.st.PrevVersion, e.st.Version = e.st.Version, latest
	saveState(e.st)
	cleanupReleases(e.st.Version, e.st.PrevVersion)

	// Набор и параметры стратегий в новой версии могли поменяться — проверяем заново.
	if e.cfg.AutoSelect && e.cfg.Strategy == "" {
		e.autoselect()
	} else {
		e.startCurrent()
	}
	logf("обновлено до %s", latest)
}

func (e *Engine) currentStrategy() string {
	if e.cfg.Strategy != "" {
		return e.cfg.Strategy
	}
	return e.st.Strategy
}

// startBest запускает сохранённую стратегию, а если её нет — подбирает.
// Возвращает true, если был автоподбор (он сам проверяет сайты).
func (e *Engine) startBest() bool {
	if e.currentStrategy() == "" && e.cfg.AutoSelect {
		e.autoselect()
		return true
	}
	e.startCurrent()
	return false
}

// verify — проверка после запуска: открываются ли Discord и YouTube. Если нет, а интернет есть, —
// подбираем стратегию заново. Окно программы прячется в трей, только когда проверка пройдена.
func (e *Engine) verify() {
	e.setStatus("Проверяю Discord и YouTube…")
	ok := score(e.cfg.CheckTargets)
	logf("проверка после запуска: %d из %d", ok, len(e.cfg.CheckTargets))
	if ok*2 < len(e.cfg.CheckTargets) && e.cfg.AutoSelect && e.cfg.Strategy == "" && internetUp(e.cfg.ControlTarget) {
		e.autoselect()
		return
	}
	e.setStatus("")
}

func (e *Engine) startCurrent() {
	defer saveState(e.st)
	name := e.currentStrategy()
	if name == "" {
		name = "general"
	}
	if err := e.startWinws(name); err != nil {
		logf("запуск стратегии %q: %v", name, err)
		if name != "general" {
			if err := e.startWinws("general"); err != nil {
				logf("запуск general: %v", err)
			}
		}
	}
	if e.cmd == nil {
		e.st.Status = "Не получается запустить обход"
	}
}

// autoselect по очереди пробует все стратегии и оставляет ту, при которой открывается больше всего сайтов.
func (e *Engine) autoselect() {
	e.stopWinws()
	if !internetUp(e.cfg.ControlTarget) {
		logf("автоподбор отложен: нет интернета")
		e.setStatus("")
		e.startCurrent()
		return
	}
	names, err := listStrategies(e.root())
	if err != nil || len(names) == 0 {
		logf("автоподбор: нет стратегий: %v", err)
		return
	}
	// Начинаем с текущей: если она работает идеально, остальные не перебираем.
	if cur := e.currentStrategy(); cur != "" {
		for i, n := range names {
			select {
			case <-e.stop:
				return // служба останавливается — не тянем с выключением
			default:
			}
			if n == cur {
				names = append([]string{cur}, append(names[:i:i], names[i+1:]...)...)
				break
			}
		}
	}

	logf("автоподбор стратегии среди %d вариантов", len(names))
	best, bestScore := "", -1
	for i, n := range names {
		e.setStatus("Подбираю настройки: %d из %d…", i+1, len(names))
		if err := e.startWinws(n); err != nil {
			logf("  %s: не запускается: %v", n, err)
			continue
		}
		time.Sleep(3 * time.Second)
		s := score(e.cfg.CheckTargets)
		if !e.alive() {
			s = -1 // упал во время проверки
		}
		logf("  %s: %d/%d", n, s, len(e.cfg.CheckTargets))
		e.stopWinws()
		if s > bestScore {
			best, bestScore = n, s
		}
		if s == len(e.cfg.CheckTargets) {
			break
		}
	}
	if best == "" {
		best = "general"
	}
	logf("выбрана стратегия %q", best)
	e.st.Strategy = best
	e.st.LastSelect = time.Now()
	saveState(e.st)
	e.startCurrent()
}

func (e *Engine) startWinws(name string) error {
	e.stopWinws()
	root := e.root()
	ensureUserLists(root)
	gf := gameFilterFor(e.cfg.GameFilter, e.cfg.GameFilterTCP, e.cfg.GameFilterUDP)
	args, err := parseStrategy(root, name, gf)
	if err != nil {
		return err
	}
	killStrayWinws()

	bin := filepath.Join(root, "bin")
	cmd := exec.Command(filepath.Join(bin, "winws.exe"), args...)
	cmd.Dir = bin
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	if err := cmd.Start(); err != nil {
		return err
	}
	e.attachJob(cmd.Process.Pid)

	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()

	// Если winws сразу падает (ошибка в аргументах, конфликт WinDivert) — это видно за пару секунд.
	select {
	case err := <-exited:
		if err == nil {
			err = errors.New("winws.exe сразу завершился")
		}
		return err
	case <-time.After(2 * time.Second):
	}
	e.cmd, e.exited, e.started = cmd, exited, time.Now()
	e.st.Status = "" // сохранится вызывающим кодом или при следующем saveState
	logf("запущен winws.exe: zapret %s, стратегия %q", e.st.Version, name)
	return nil
}

func (e *Engine) stopWinws() {
	if e.cmd == nil {
		return
	}
	_ = e.cmd.Process.Kill()
	select {
	case <-e.exited:
	case <-time.After(5 * time.Second):
	}
	e.cmd, e.exited = nil, nil
}

// attachJob кладёт winws.exe в Job Object: если служба аварийно умрёт, Windows сама убьёт winws.
func (e *Engine) attachJob(pid int) {
	if e.job == 0 {
		job, err := windows.CreateJobObject(nil, nil)
		if err != nil {
			return
		}
		info := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
		info.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
		if _, err := windows.SetInformationJobObject(job, windows.JobObjectExtendedLimitInformation,
			uintptr(unsafe.Pointer(&info)), uint32(unsafe.Sizeof(info))); err != nil {
			windows.CloseHandle(job)
			return
		}
		e.job = job
	}
	h, err := windows.OpenProcess(windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE, false, uint32(pid))
	if err != nil {
		return
	}
	_ = windows.AssignProcessToJobObject(e.job, h)
	windows.CloseHandle(h)
}

func runHidden(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true, CreationFlags: createNoWindow}
	return cmd.Run()
}

// unloadWinDivert выгружает драйвер WinDivert после остановки обхода,
// чтобы после выключения от программы ничего не оставалось в системе.
func unloadWinDivert() {
	m, err := mgr.Connect()
	if err != nil {
		return
	}
	defer m.Disconnect()
	for _, name := range []string{"WinDivert", "WinDivert14"} {
		if s, err := m.OpenService(name); err == nil {
			stopService(s)
			s.Close()
		}
	}
}

func killStrayWinws() { _ = runHidden("taskkill", "/IM", "winws.exe", "/F") }

// enableTCPTimestamps — то же, что :tcp_enable в service.bat (нужно для --dpi-desync-fooling=ts).
func enableTCPTimestamps() {
	_ = runHidden("netsh", "interface", "tcp", "set", "global", "timestamps=enabled")
}
