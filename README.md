# ZapretAuto

Тихая обёртка над [zapret-discord-youtube (Flowseal)](https://github.com/Flowseal/zapret-discord-youtube)

**Скачать:** [последний релиз](https://github.com/DemonLibe/ZapretAuto-/releases/latest) → `ZapretAuto.exe` для Windows 10/11 x64.

## Как пользоваться

1. Запустите `ZapretAuto.exe` двойным кликом и нажмите «Да» в окне UAC.
2. Всё. Появится одно окно «Готово», дальше программа работает как служба Windows
   и пользователя больше не трогает: без консолей, без всплывающих окон и без открытия браузера.

Если положить exe в распакованную папку zapret (рядом с `service.bat`), он возьмёт её как стартовую
версию и сможет запуститься даже без доступа к GitHub. Иначе он скачает последнюю версию сам.

## Что делает служба

- Скачивает последний релиз zapret-discord-youtube с GitHub в `C:\ProgramData\ZapretAuto\releases\<версия>`.
- Подбирает стратегию: по очереди запускает все `general*.bat` и оставляет ту, при которой открывается
  больше всего сайтов (Discord, YouTube, googlevideo). Запросы те же, что в «Standard tests» из `utils\test zapret.ps1`.
- Запускает `winws.exe` скрыто, с теми же аргументами, что и выбранный bat (разбирает bat так же, как `service.bat`).
- Перезапускает `winws.exe`, если он упал.
- Каждые 6 часов проверяет `.service/version.txt` в репозитории. Если вышла новая версия, скачивает её рядом,
  переносит пользовательские списки (`*-user.txt`), переключается и заново подбирает стратегию.
  Предыдущая версия остаётся: если новая трижды не запустится, служба откатится на неё.
- Каждые 30 минут проверяет, что сайты открываются. Если интернет есть, а сайты не открываются две проверки подряд,
  она заново подбирает стратегию.
- Включает TCP timestamps (как `service.bat`).
- При установке удаляет старую службу `zapret` из `service.bat`: две службы на WinDivert конфликтуют.

## Команды (из консоли администратора)

```
ZapretAuto.exe status      состояние, версия, стратегия
ZapretAuto.exe update      проверить обновление сейчас
ZapretAuto.exe select      подобрать стратегию заново
ZapretAuto.exe uninstall   удалить службу, WinDivert и скачанные файлы
ZapretAuto.exe run         запустить в консоли без службы (отладка)
```

## Настройки — `C:\ProgramData\ZapretAuto\config.json`

| Поле | По умолчанию | Смысл |
|---|---|---|
| `strategy` | `""` | Зафиксировать стратегию, например `"general (ALT3)"`. Пусто — автоподбор |
| `auto_select` | `true` | Подбирать стратегию автоматически |
| `update_interval_hours` | `6` | Как часто проверять обновления |
| `health_interval_minutes` | `30` | Как часто проверять доступность сайтов (0 — не проверять) |
| `game_filter` | `"disabled"` | `disabled` / `all` / `tcp` / `udp` (пункт 4 в меню service.bat) |
| `check_targets` | Discord, YouTube… | Сайты для проверки стратегии |
| `control_target` | `https://ya.ru` | Незаблокированный сайт: по нему служба понимает, есть ли вообще интернет |

После изменения перезапустите службу: `sc stop ZapretAuto && sc start ZapretAuto`.

Лог: `C:\ProgramData\ZapretAuto\zapretauto.log`.

## Сборка

```
./build.sh       # нужен Go 1.22+
go test ./...    # ZAPRET_DIR=<распакованный релиз> — проверить разбор всех стратегий
```
