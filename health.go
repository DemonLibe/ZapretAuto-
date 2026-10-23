package main

import (
	"net/http"
	"sync"
	"time"
)

// probe делает HEAD-запрос, как «Standard tests» в utils\test zapret.ps1:
// любой HTTP-ответ означает, что TLS-соединение не было заблокировано.
func probe(url string, timeout time.Duration) bool {
	c := &http.Client{
		Timeout: timeout,
		// Редиректы не нужны — достаточно, что сервер ответил.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
		Transport:     &http.Transport{DisableKeepAlives: true, Proxy: http.ProxyFromEnvironment},
	}
	req, err := http.NewRequest("HEAD", url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64)")
	resp, err := c.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

// score — сколько целей из списка открываются.
func score(targets []string) int {
	var wg sync.WaitGroup
	var mu sync.Mutex
	n := 0
	for _, t := range targets {
		wg.Add(1)
		go func(u string) {
			defer wg.Done()
			if probe(u, 6*time.Second) {
				mu.Lock()
				n++
				mu.Unlock()
			}
		}(t)
	}
	wg.Wait()
	return n
}

func internetUp(control string) bool {
	if control == "" {
		return true
	}
	for i := 0; i < 2; i++ {
		if probe(control, 6*time.Second) {
			return true
		}
	}
	return false
}
