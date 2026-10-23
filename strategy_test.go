package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ZAPRET_DIR — распакованный релиз zapret-discord-youtube для проверки разбора.
func releaseForTest(t *testing.T) string {
	d := os.Getenv("ZAPRET_DIR")
	if d == "" {
		t.Skip("ZAPRET_DIR не задан")
	}
	return d
}

func TestParseAllStrategies(t *testing.T) {
	root := releaseForTest(t)
	names, err := listStrategies(root)
	if err != nil || len(names) == 0 {
		t.Fatalf("listStrategies: %v %v", names, err)
	}
	if names[0] != "general" || names[1] != "general (ALT)" || names[2] != "general (ALT2)" {
		t.Errorf("порядок как в service.bat нарушен: %v", names[:3])
	}
	for _, n := range names {
		args, err := parseStrategy(root, n, gameFilterFor("disabled", "", ""))
		if err != nil {
			t.Errorf("%s: %v", n, err)
			continue
		}
		if !strings.HasPrefix(args[0], "--wf-") {
			t.Errorf("%s: первый аргумент %q", n, args[0])
		}
		for _, a := range args {
			if strings.ContainsAny(a, `"^%`) || a == "" {
				t.Errorf("%s: плохой аргумент %q", n, a)
			}
			// Все упомянутые файлы должны существовать (кроме *-user.txt, их создаёт ensureUserLists).
			if i := strings.Index(a, "="); i > 0 && strings.HasPrefix(a[i+1:], root) && !strings.Contains(a, "-user.txt") {
				if _, err := os.Stat(a[i+1:]); err != nil {
					t.Errorf("%s: нет файла %s", n, a[i+1:])
				}
			}
		}
	}
	t.Logf("разобрано стратегий: %d", len(names))
}

func TestParseGeneralExact(t *testing.T) {
	root := releaseForTest(t)
	args, err := parseStrategy(root, "general", gameFilterFor("all", "1024-65535", "1024-65535"))
	if err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(root, "bin") + "/"
	want := []string{
		"--wf-tcp=80,443,2053,2083,2087,2096,8443,1024-65535",
		"--wf-udp=443,19294-19344,50000-50100,1024-65535",
		"--filter-udp=443",
		"--hostlist=" + filepath.Join(root, "lists") + "/list-general.txt",
	}
	for i, w := range want {
		if args[i] != w {
			t.Errorf("arg %d = %q, want %q", i, args[i], w)
		}
	}
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "--dpi-desync-fake-quic="+bin+"quic_initial_www_google_com.bin") {
		t.Error("нет пути к quic-фейку")
	}
	if strings.Count(joined, "--new") != 8 {
		t.Errorf("ожидалось 8 --new, есть %d", strings.Count(joined, "--new"))
	}
	if !strings.HasSuffix(joined, "--dpi-desync-cutoff=n2") {
		t.Errorf("конец команды обрезан: ...%s", joined[len(joined)-40:])
	}
}

func TestSplitArgs(t *testing.T) {
	got := splitArgs(` --a="C:\x y\z.txt"  --b=1 "--c" --fake-tls=^! --d="^x"`)
	want := []string{`--a=C:\x y\z.txt`, "--b=1", "--c", "--fake-tls=!", "--d=^x"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%q", got)
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{{"1.10.4", "1.9.7", 1}, {"1.10.4", "1.10.4", 0}, {"1.10", "1.10.1", -1}, {"1.11.0", "", 1}, {"1.9.9b", "1.9.9", 1}}
	for _, c := range cases {
		if g := compareVersions(c.a, c.b); g != c.want {
			t.Errorf("compare(%q,%q)=%d", c.a, c.b, g)
		}
	}
}

func TestUnzipRelease(t *testing.T) {
	z := os.Getenv("ZAPRET_ZIP")
	if z == "" {
		t.Skip("ZAPRET_ZIP не задан")
	}
	dir := t.TempDir()
	if err := unzip(z, dir); err != nil {
		t.Fatal(err)
	}
	root, err := findReleaseRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	if v := localVersion(root); v != "1.10.4" {
		t.Errorf("localVersion=%q", v)
	}
}

func TestLatestVersionLive(t *testing.T) {
	if os.Getenv("ZAPRET_LIVE") == "" {
		t.Skip()
	}
	v, err := latestVersion("Flowseal/zapret-discord-youtube")
	t.Logf("latest=%q err=%v", v, err)
	if err != nil {
		t.Fatal(err)
	}
}
