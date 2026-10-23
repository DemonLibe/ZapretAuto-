package main

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	logMu      sync.Mutex
	logConsole bool
)

// logf пишет в %ProgramData%\ZapretAuto\zapretauto.log (лог ротируется при 1 МБ).
func logf(format string, a ...any) {
	logMu.Lock()
	defer logMu.Unlock()
	line := time.Now().Format("2006-01-02 15:04:05 ") + fmt.Sprintf(format, a...) + "\r\n"
	if logConsole {
		fmt.Print(line)
	}
	p := logPath()
	_ = os.MkdirAll(filepath.Dir(p), 0o755)
	if fi, err := os.Stat(p); err == nil && fi.Size() > 1<<20 {
		_ = os.Rename(p, p+".old")
	}
	if f, err := os.OpenFile(p, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644); err == nil {
		f.WriteString(line)
		f.Close()
	}
}
