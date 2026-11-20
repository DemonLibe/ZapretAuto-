package main

import (
	"testing"
	"time"
)

func TestUpdateDue(t *testing.T) {
	now := time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
	six := 6 * time.Hour
	cases := []struct {
		name string
		st   State
		want bool
	}{
		{"никогда не проверяли", State{}, true},
		{"успех час назад", State{LastCheck: now.Add(-time.Hour), CheckOK: true}, false},
		{"успех 7 часов назад", State{LastCheck: now.Add(-7 * time.Hour), CheckOK: true}, true},
		{"неудача 5 минут назад", State{LastCheck: now.Add(-5 * time.Minute)}, false},
		{"неудача 20 минут назад", State{LastCheck: now.Add(-20 * time.Minute)}, true},
		{"компьютер спал 3 дня", State{LastCheck: now.Add(-72 * time.Hour), CheckOK: true}, true},
	}
	for _, c := range cases {
		if got := updateDue(c.st, six, now); got != c.want {
			t.Errorf("%s: updateDue=%v, want %v", c.name, got, c.want)
		}
	}
}

func TestCompareVersionsNewer(t *testing.T) {
	if compareVersions("1.10.5", "1.10.4") <= 0 || compareVersions("1.11.0", "1.10.4") <= 0 || compareVersions("2.0", "1.10.4") <= 0 {
		t.Error("новая версия не распознана как более новая")
	}
}
