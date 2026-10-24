package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Config — настройки пользователя (config.json). Создаётся при первом запуске,
// править руками не обязательно.
type Config struct {
	// Имя bat-стратегии без расширения, например "general" или "general (ALT3)".
	// Пусто — подобрать автоматически.
	Strategy string `json:"strategy"`
	// Автоматически подбирать стратегию, если текущая перестала работать.
	AutoSelect bool `json:"auto_select"`
	// Как часто проверять обновления zapret (часы).
	UpdateIntervalHours int `json:"update_interval_hours"`
	// Как часто проверять, что Discord/YouTube открываются (минуты, 0 — не проверять).
	HealthIntervalMinutes int `json:"health_interval_minutes"`
	// Игровой фильтр: disabled | all | tcp | udp (как пункт 4 в service.bat).
	GameFilter    string `json:"game_filter"`
	GameFilterTCP string `json:"game_filter_tcp"`
	GameFilterUDP string `json:"game_filter_udp"`
	// Сайты, по которым проверяется работоспособность стратегии.
	CheckTargets []string `json:"check_targets"`
	// Сайт без блокировок: если недоступен и он, значит нет интернета, а не стратегия плохая.
	ControlTarget string `json:"control_target"`
	// Репозиторий, откуда брать релизы.
	Repo string `json:"repo"`
}

func defaultConfig() Config {
	return Config{
		AutoSelect:            true,
		UpdateIntervalHours:   6,
		HealthIntervalMinutes: 30,
		GameFilter:            "disabled",
		GameFilterTCP:         "1024-65535",
		GameFilterUDP:         "1024-65535",
		CheckTargets: []string{
			"https://discord.com",
			"https://gateway.discord.gg",
			"https://cdn.discordapp.com",
			"https://www.youtube.com",
			"https://i.ytimg.com",
			"https://redirector.googlevideo.com",
		},
		ControlTarget: "https://ya.ru",
		Repo:          "Flowseal/zapret-discord-youtube",
	}
}

// State — служебное состояние, которое программа ведёт сама.
type State struct {
	Version     string    `json:"version"`      // установленная версия zapret
	Strategy    string    `json:"strategy"`     // текущая стратегия (результат автоподбора)
	LastCheck   time.Time `json:"last_check"`   // последняя проверка обновлений
	LastSelect  time.Time `json:"last_select"`  // последний автоподбор
	PrevVersion string    `json:"prev_version"` // предыдущая версия — для отката
	Status      string    `json:"status"`       // что служба делает сейчас (для окна программы); "" — работает
}

func dataDir() string {
	pd := os.Getenv("ProgramData")
	if pd == "" {
		pd = `C:\ProgramData`
	}
	return filepath.Join(pd, "ZapretAuto")
}

func releasesDir() string        { return filepath.Join(dataDir(), "releases") }
func releaseDir(v string) string { return filepath.Join(releasesDir(), v) }
func configPath() string         { return filepath.Join(dataDir(), "config.json") }
func statePath() string          { return filepath.Join(dataDir(), "state.json") }
func logPath() string            { return filepath.Join(dataDir(), "zapretauto.log") }
func installedExe() string       { return filepath.Join(dataDir(), "ZapretAuto.exe") }

func loadConfig() Config {
	cfg := defaultConfig()
	b, err := os.ReadFile(configPath())
	if err != nil {
		saveJSON(configPath(), cfg)
		return cfg
	}
	if err := json.Unmarshal(b, &cfg); err != nil {
		logf("config.json повреждён (%v), использую настройки по умолчанию", err)
		return defaultConfig()
	}
	if cfg.UpdateIntervalHours <= 0 {
		cfg.UpdateIntervalHours = 6
	}
	if len(cfg.CheckTargets) == 0 {
		cfg.CheckTargets = defaultConfig().CheckTargets
	}
	if cfg.Repo == "" {
		cfg.Repo = defaultConfig().Repo
	}
	return cfg
}

func loadState() State {
	var st State
	if b, err := os.ReadFile(statePath()); err == nil {
		_ = json.Unmarshal(b, &st)
	}
	return st
}

func saveState(st State) { saveJSON(statePath(), st) }

func saveJSON(path string, v any) {
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	b, _ := json.MarshalIndent(v, "", "  ")
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err == nil {
		_ = os.Rename(tmp, path)
	}
}
