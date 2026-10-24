//go:build windows

package main

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/lxn/walk"
	. "github.com/lxn/walk/declarative"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"
)

const windowTitle = "ZapretAuto"

var (
	colorOK    = walk.RGB(22, 140, 64)
	colorOff   = walk.RGB(120, 120, 120)
	colorBusy  = walk.RGB(200, 120, 0)
	colorMuted = walk.RGB(110, 110, 110)
)

type appWindow struct {
	mw        *walk.MainWindow
	status    *walk.Label
	detail    *walk.TextLabel
	info      *walk.Label
	toggle    *walk.PushButton
	autostart *walk.CheckBox
	busy      bool

	tray       *walk.NotifyIcon
	trayToggle *walk.Action
	iconOn     *walk.Icon
	iconOff    *walk.Icon
	quitting   bool
	sawIdle    bool // видели «не работает» — значит, запуск идёт при открытом окне
	okTicks    int  // сколько обновлений подряд обход работает
	hintShown  bool
	bounds     walk.Rectangle
}

// runGUI показывает окно программы со значком в трее. hidden — запуск сразу в трей (автозагрузка).
// Второй экземпляр не запускается — показывается уже открытое окно.
func runGUI(hidden bool) {
	name, _ := windows.UTF16PtrFromString("Local\\ZapretAutoWindow")
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		if hidden {
			return
		}
		title, _ := windows.UTF16PtrFromString(windowTitle)
		if h := win.FindWindow(nil, title); h != 0 {
			win.ShowWindow(h, win.SW_SHOW)
			win.ShowWindow(h, win.SW_RESTORE)
			win.SetForegroundWindow(h)
		}
		return
	}

	a := &appWindow{}
	icon, _ := walk.NewIconFromResourceWithSize("APP", walk.Size{Width: 64, Height: 64})
	smallIcon, _ := walk.NewIconFromResource("APP")
	a.iconOn = smallIcon
	a.iconOff, _ = walk.NewIconFromResource("OFF")

	err := MainWindow{
		AssignTo: &a.mw,
		Title:    windowTitle,
		Icon:     smallIcon,
		Visible:  !hidden,
		Size:     Size{Width: 460, Height: 330},
		MinSize:  Size{Width: 460, Height: 330},
		MaxSize:  Size{Width: 460, Height: 330},
		Font:     Font{Family: "Segoe UI", PointSize: 10},
		Layout:   VBox{Margins: Margins{Left: 20, Top: 18, Right: 20, Bottom: 14}, Spacing: 8},
		Children: []Widget{
			Composite{
				Layout: HBox{MarginsZero: true, Spacing: 14},
				Children: []Widget{
					ImageView{Image: icon, MinSize: Size{Width: 64, Height: 64}, MaxSize: Size{Width: 64, Height: 64}, Mode: ImageViewModeZoom},
					Composite{
						Layout: VBox{MarginsZero: true, Spacing: 2},
						Children: []Widget{
							Label{Text: "ZapretAuto", Font: Font{Family: "Segoe UI", PointSize: 16, Bold: true}},
							Label{Text: "Discord и YouTube без блокировок", TextColor: colorMuted},
						},
					},
					HSpacer{},
				},
			},
			VSpacer{Size: 6},
			Label{AssignTo: &a.status, Font: Font{Family: "Segoe UI", PointSize: 13, Bold: true}},
			TextLabel{AssignTo: &a.detail, MinSize: Size{Height: 34}},
			Label{AssignTo: &a.info, TextColor: colorMuted},
			VSpacer{},
			PushButton{AssignTo: &a.toggle, MinSize: Size{Height: 38}, OnClicked: a.onToggle},
			CheckBox{AssignTo: &a.autostart, Alignment: AlignHNearVCenter, Text: "Запускать при включении компьютера", OnClicked: a.onAutostart},
			Composite{
				Layout: HBox{MarginsZero: true},
				Children: []Widget{
					PushButton{Text: "Журнал", OnClicked: openLog},
					HSpacer{},
					PushButton{Text: "Удалить…", OnClicked: a.onUninstall},
				},
			},
		},
	}.Create()
	if err != nil {
		messageBox("ZapretAuto", "Не удалось открыть окно: "+err.Error(), true)
		return
	}

	// Окно фиксированного размера: без «развернуть» и растягивания.
	hwnd := a.mw.Handle()
	style := win.GetWindowLong(hwnd, win.GWL_STYLE)
	win.SetWindowLong(hwnd, win.GWL_STYLE, style&^(win.WS_MAXIMIZEBOX|win.WS_THICKFRAME))

	a.setupTray()
	// Крестик не закрывает программу, а прячет её в трей: обход работает в службе независимо от окна.
	a.mw.Closing().Attach(func(canceled *bool, _ walk.CloseReason) {
		if a.quitting {
			return
		}
		*canceled = true
		a.hideToTray("ZapretAuto продолжает работать", "Нажмите на значок, чтобы открыть окно.")
	})

	a.refresh()
	stop := make(chan struct{})
	go func() {
		t := time.NewTicker(1500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case <-t.C:
				a.mw.Synchronize(a.refresh)
			}
		}
	}()
	a.mw.Run()
	close(stop)
	if a.tray != nil {
		a.tray.Dispose()
	}
}

func (a *appWindow) setupTray() {
	ni, err := walk.NewNotifyIcon(a.mw)
	if err != nil {
		return
	}
	a.tray = ni
	_ = ni.SetIcon(a.iconOn)
	_ = ni.SetToolTip(windowTitle)
	ni.MouseDown().Attach(func(_, _ int, button walk.MouseButton) {
		if button == walk.LeftButton {
			a.showWindow()
		}
	})

	open := walk.NewAction()
	_ = open.SetText("Открыть ZapretAuto")
	open.Triggered().Attach(a.showWindow)
	a.trayToggle = walk.NewAction()
	_ = a.trayToggle.SetText("Выключить")
	a.trayToggle.Triggered().Attach(a.onToggle)
	quit := walk.NewAction()
	_ = quit.SetText("Убрать значок (обход продолжит работать)")
	quit.Triggered().Attach(func() {
		a.quitting = true
		a.mw.Close()
	})
	acts := ni.ContextMenu().Actions()
	_ = acts.Add(open)
	_ = acts.Add(a.trayToggle)
	_ = acts.Add(walk.NewSeparatorAction())
	_ = acts.Add(quit)
	_ = ni.SetVisible(true)
}

func (a *appWindow) showWindow() {
	a.mw.Show()
	if a.bounds.Width > 0 {
		_ = a.mw.SetBounds(a.bounds)
	}
	h := a.mw.Handle()
	win.ShowWindow(h, win.SW_RESTORE)
	win.SetForegroundWindow(h)
}

// hideToTray прячет окно; подсказку у значка показываем один раз за запуск.
func (a *appWindow) hideToTray(title, text string) {
	if a.mw.Visible() {
		a.bounds = a.mw.Bounds()
	}
	a.mw.Hide()
	if a.tray != nil && !a.hintShown {
		a.hintShown = true
		_ = a.tray.ShowInfo(title, text)
	}
}

// updateTray: цветной значок — обход работает, серый — нет.
func (a *appWindow) updateTray(working, running bool, tip string) {
	if a.tray == nil {
		return
	}
	if working {
		_ = a.tray.SetIcon(a.iconOn)
	} else {
		_ = a.tray.SetIcon(a.iconOff)
	}
	_ = a.tray.SetToolTip("ZapretAuto — " + tip)
	if running {
		_ = a.trayToggle.SetText("Выключить")
	} else {
		_ = a.trayToggle.SetText("Включить")
	}
}

// refresh обновляет окно по состоянию службы и state.json.
func (a *appWindow) refresh() {
	if a.busy {
		return
	}
	info := queryService()
	st := loadState()

	a.autostart.SetEnabled(info.Installed)
	a.autostart.SetChecked(info.Autostart)

	switch {
	case !info.Installed:
		a.setStatus("○ Не установлен", colorOff, "Нажмите «Установить», чтобы включить обход блокировок.")
		a.toggle.SetText("Установить")
	case info.State == windows.SERVICE_RUNNING && st.Status == "":
		a.setStatus("● Работает", colorOK, "Discord и YouTube открываются без VPN. Окно можно закрыть — программа останется в трее.")
		a.toggle.SetText("Выключить")
	case info.State == windows.SERVICE_RUNNING || info.State == windows.SERVICE_START_PENDING:
		msg := st.Status
		if msg == "" || msg == "Выключен" || info.State == windows.SERVICE_START_PENDING {
			msg = "Запускается…"
		}
		detail := "Это займёт от нескольких секунд до пары минут."
		if strings.HasPrefix(msg, "Не ") {
			detail = "Выключите VPN и другие обходы блокировок, проверьте антивирус. Подробности — в журнале."
		}
		a.setStatus("◌ "+msg, colorBusy, detail)
		a.toggle.SetText("Выключить")
	case info.State == windows.SERVICE_STOP_PENDING:
		a.setStatus("◌ Выключается…", colorBusy, "")
		a.toggle.SetText("Включить")
	default:
		a.setStatus("○ Выключен", colorOff, "Обход блокировок не работает. Нажмите «Включить».")
		a.toggle.SetText("Включить")
	}

	working := info.State == windows.SERVICE_RUNNING && st.Status == ""
	running := info.State == windows.SERVICE_RUNNING || info.State == windows.SERVICE_START_PENDING
	tip := "выключен"
	switch {
	case working:
		tip = "работает"
	case running:
		tip = strings.TrimSuffix(st.Status, "…")
		if tip == "" || tip == "Выключен" {
			tip = "запускается"
		}
	}
	a.updateTray(working, running, tip)
	// После успешного запуска и проверки (и если обход держится несколько секунд) окно само уходит в трей.
	if working {
		a.okTicks++
	} else {
		a.okTicks, a.sawIdle = 0, true
	}
	if a.sawIdle && a.okTicks == 3 && a.mw.Visible() {
		a.sawIdle = false
		a.hideToTray("ZapretAuto работает ✔", "Discord и YouTube открываются. Программа свернулась сюда — нажмите на значок, чтобы открыть.")
	}

	var parts []string
	if st.Version != "" {
		parts = append(parts, "zapret "+st.Version)
	}
	if st.Strategy != "" {
		parts = append(parts, "настройка «"+st.Strategy+"»")
	}
	parts = append(parts, "программа "+strings.TrimPrefix(appVersion, "v"))
	a.info.SetText(strings.Join(parts, "  ·  "))
}

func (a *appWindow) setStatus(text string, color walk.Color, detail string) {
	a.status.SetText(text)
	a.status.SetTextColor(color)
	a.detail.SetText(detail)
}

// runBusy выполняет действие в фоне, блокируя кнопки, и затем обновляет окно.
func (a *appWindow) runBusy(label string, fn func() error) {
	a.busy = true
	a.toggle.SetEnabled(false)
	a.autostart.SetEnabled(false)
	a.setStatus("◌ "+label, colorBusy, "")
	go func() {
		err := fn()
		a.mw.Synchronize(func() {
			a.busy = false
			a.toggle.SetEnabled(true)
			a.refresh()
			if err != nil && err != errCancelled {
				walk.MsgBox(a.mw, "ZapretAuto", err.Error(), walk.MsgBoxIconError)
			}
		})
	}()
}

func (a *appWindow) onToggle() {
	info := queryService()
	switch {
	case !info.Installed:
		a.runBusy("Устанавливаю…", installFromUI)
	case info.State == windows.SERVICE_RUNNING || info.State == windows.SERVICE_START_PENDING:
		a.runBusy("Выключаю…", func() error {
			if err := stopServiceUser(); err != nil {
				return fmt.Errorf("не удалось выключить: %w", err)
			}
			waitState(windows.SERVICE_STOPPED, 30*time.Second)
			return nil
		})
	default:
		a.runBusy("Включаю…", func() error {
			if err := startServiceUser(); err != nil {
				return fmt.Errorf("не удалось включить: %w", err)
			}
			waitState(windows.SERVICE_RUNNING, 15*time.Second)
			return nil
		})
	}
}

func (a *appWindow) onAutostart() {
	on := a.autostart.Checked()
	a.runBusy("Сохраняю…", func() error { return setAutostartUser(on) })
}

func (a *appWindow) onUninstall() {
	if walk.MsgBox(a.mw, "Удалить ZapretAuto?",
		"Обход блокировок будет выключен, а программа и все её файлы — удалены.",
		walk.MsgBoxYesNo|walk.MsgBoxIconQuestion) != walk.DlgCmdYes {
		return
	}
	a.runBusy("Удаляю…", func() error {
		err := runElevatedWait("uninstall")
		if err == nil {
			a.mw.Synchronize(func() {
				a.quitting = true
				a.mw.Close()
			})
		}
		return err
	})
}

func waitState(want uint32, timeout time.Duration) {
	for end := time.Now().Add(timeout); time.Now().Before(end); time.Sleep(300 * time.Millisecond) {
		if queryService().State == want {
			return
		}
	}
}

func openLog() {
	_ = exec.Command("notepad.exe", logPath()).Start()
}
