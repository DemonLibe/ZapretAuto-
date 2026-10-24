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
	detail    *walk.Label
	info      *walk.Label
	toggle    *walk.PushButton
	autostart *walk.CheckBox
	busy      bool
}

// runGUI показывает окно программы. Второе окно не открывается — поднимается уже открытое.
func runGUI() {
	name, _ := windows.UTF16PtrFromString("Local\\ZapretAutoWindow")
	if _, err := windows.CreateMutex(nil, false, name); err == windows.ERROR_ALREADY_EXISTS {
		title, _ := windows.UTF16PtrFromString(windowTitle)
		if h := win.FindWindow(nil, title); h != 0 {
			win.ShowWindow(h, win.SW_RESTORE)
			win.SetForegroundWindow(h)
		}
		return
	}

	a := &appWindow{}
	icon, _ := walk.NewIconFromResourceWithSize("APP", walk.Size{Width: 64, Height: 64})
	smallIcon, _ := walk.NewIconFromResource("APP")

	err := MainWindow{
		AssignTo: &a.mw,
		Title:    windowTitle,
		Icon:     smallIcon,
		Size:     Size{Width: 460, Height: 330},
		MinSize:  Size{Width: 460, Height: 330},
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
			Label{AssignTo: &a.detail},
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
		a.setStatus("● Работает", colorOK, "Discord и YouTube открываются без VPN. Окно можно закрыть.")
		a.toggle.SetText("Выключить")
	case info.State == windows.SERVICE_RUNNING || info.State == windows.SERVICE_START_PENDING:
		msg := st.Status
		if msg == "" || info.State == windows.SERVICE_START_PENDING {
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
			a.mw.Synchronize(func() { a.mw.Close() })
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
