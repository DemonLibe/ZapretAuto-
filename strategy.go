package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// GameFilter — порты игрового фильтра, подставляемые в %GameFilterTCP%/%GameFilterUDP%.
// "12" — заглушка, которую использует service.bat, когда фильтр выключен.
type GameFilter struct{ TCP, UDP string }

func gameFilterFor(mode, tcp, udp string) GameFilter {
	switch strings.ToLower(mode) {
	case "all":
		return GameFilter{tcp, udp}
	case "tcp":
		return GameFilter{tcp, "12"}
	case "udp":
		return GameFilter{"12", udp}
	}
	return GameFilter{"12", "12"}
}

// listStrategies возвращает имена стратегий (bat-файлы, кроме service*.bat)
// в том же порядке, что и меню service.bat: числа сравниваются как числа.
func listStrategies(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.EqualFold(filepath.Ext(n), ".bat") || strings.HasPrefix(strings.ToLower(n), "service") {
			continue
		}
		names = append(names, strings.TrimSuffix(n, filepath.Ext(n)))
	}
	pad := regexp.MustCompile(`\d+`)
	key := func(s string) string {
		return pad.ReplaceAllStringFunc(s, func(d string) string { return fmt.Sprintf("%08s", d) })
	}
	sort.Slice(names, func(i, j int) bool { return key(names[i]) < key(names[j]) })
	return names, nil
}

// parseStrategy читает bat-файл стратегии и возвращает аргументы winws.exe
// с уже подставленными путями — так же, как это делает service.bat при установке службы.
func parseStrategy(root, name string, gf GameFilter) ([]string, error) {
	raw, err := os.ReadFile(filepath.Join(root, name+".bat"))
	if err != nil {
		return nil, err
	}
	text := strings.ReplaceAll(string(raw), "\r\n", "\n")

	// Собираем команду winws.exe вместе со строками-продолжениями (оканчиваются на ^).
	var cmd strings.Builder
	capture := false
	for _, line := range strings.Split(text, "\n") {
		if !capture {
			i := strings.Index(strings.ToLower(line), `winws.exe"`)
			if i < 0 {
				continue
			}
			capture = true
			line = line[i+len(`winws.exe"`):]
		}
		trimmed := strings.TrimRight(line, " \t")
		cont := strings.HasSuffix(trimmed, "^")
		cmd.WriteString(strings.TrimSuffix(trimmed, "^"))
		cmd.WriteByte(' ')
		if !cont {
			break
		}
	}
	if !capture {
		return nil, fmt.Errorf("в %s.bat не найден запуск winws.exe", name)
	}

	sep := string(os.PathSeparator)
	vars := map[string]string{
		"BIN":           filepath.Join(root, "bin") + sep,
		"LISTS":         filepath.Join(root, "lists") + sep,
		"GAMEFILTER":    gf.TCP,
		"GAMEFILTERTCP": gf.TCP,
		"GAMEFILTERUDP": gf.UDP,
	}
	expanded := regexp.MustCompile(`%([A-Za-z_][A-Za-z0-9_]*)%`).ReplaceAllStringFunc(cmd.String(), func(m string) string {
		if v, ok := vars[strings.ToUpper(m[1:len(m)-1])]; ok {
			return v
		}
		return m
	})

	args := splitArgs(expanded)
	if len(args) == 0 {
		return nil, fmt.Errorf("в %s.bat пустая команда winws.exe", name)
	}
	for _, a := range args {
		if strings.Contains(a, "%") {
			return nil, fmt.Errorf("в %s.bat неизвестная переменная в аргументе %q", name, a)
		}
	}
	return args, nil
}

// splitArgs делит командную строку cmd по пробелам, учитывая кавычки
// (кавычки убираются: --hostlist="C:\a b\x.txt" -> --hostlist=C:\a b\x.txt)
// и экранирование ^ вне кавычек, как это делает cmd.exe (^! -> !).
func splitArgs(s string) []string {
	var args []string
	var cur strings.Builder
	inQuote, has, escaped := false, false, false
	for _, r := range s {
		switch {
		case escaped:
			cur.WriteRune(r)
			escaped, has = false, true
		case r == '^' && !inQuote:
			escaped = true
		case r == '"':
			inQuote = !inQuote
			has = true
		case (r == ' ' || r == '\t') && !inQuote:
			if has {
				args = append(args, cur.String())
				cur.Reset()
				has = false
			}
		default:
			cur.WriteRune(r)
			has = true
		}
	}
	if has {
		args = append(args, cur.String())
	}
	return args
}

// ensureUserLists создаёт пользовательские списки, на которые ссылаются стратегии
// (без них winws.exe не запустится) — аналог :load_user_lists в service.bat.
func ensureUserLists(root string) {
	lists := filepath.Join(root, "lists")
	defaults := map[string]string{
		"ipset-exclude-user.txt": "203.0.113.113/32\r\n",
		"list-general-user.txt":  "# Never leave this file empty\r\ndomain.example.abc\r\n",
		"list-exclude-user.txt":  "domain.example.abc\r\n",
	}
	for name, content := range defaults {
		p := filepath.Join(lists, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			_ = os.WriteFile(p, []byte(content), 0o644)
		}
	}
}
