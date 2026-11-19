# Проверка декодирования OBS на главном ПК

`Test-OBSDecoder.ps1` работает с установленной Local Broadcast 0.3.6+ и OBS WebSocket 5. Не требует Python, модулей PowerShell или изменения настроек WebSocket. Параметры локального OBS и пароль читает из работающего Local Broadcast; пароль не выводится и в backup не сохраняется.

Запускать **на главном ПК**, где OBS принимает `LB | Presenter`. Презентер продолжает работать. Сам скрипт не измеряет сквозную задержку: для каждого варианта нужен одновременный снимок секундомеров источника и OBS, желательно 3–5 повторов после 10 секунд установившейся работы.

```powershell
# Только собрать сведения. Изменений и backup нет.
powershell -NoProfile -ExecutionPolicy Bypass -File .\Test-OBSDecoder.ps1 -Mode Inspect

# Сохранить прежнее значение и включить аппаратное декодирование.
# Источник кратко перезапустится. При записи/эфире скрипт откажется менять настройки.
powershell -NoProfile -ExecutionPolicy Bypass -File .\Test-OBSDecoder.ps1 -Mode HardwareOn

# После повторного замера вернуть прежнее значение.
powershell -NoProfile -ExecutionPolicy Bypass -File .\Test-OBSDecoder.ps1 -Mode Restore
```

`Inspect` выводит версии, CPU, состояние SRT, `hardwareDecode`, буфер Media Source и статистику рендера OBS. Наличие `hardwareDecode=true` означает включённую настройку, а не доказательство, что конкретный GPU успешно использован: нужна проверка изображения и результата измерения. Если настройка уже true, HardwareOn не создаёт другого варианта декодирования; такой A/B-тест неинформативен.

Резервная копия сохраняется в `%LOCALAPPDATA%\LocalBroadcast\Diagnostics\obs-decoder-backup.json`. До успешного Restore повторный HardwareOn не перезапишет её. Restore проверяет ПК, URL WebSocket и UUID источника и восстанавливает только изменённое поле `is_hw_decoding`, сохраняя другие настройки. Не удаляйте backup до восстановления. При сбое подключения настройка не меняется; проверьте, что OBS и Local Broadcast запущены на главном ПК.

Отдельные параметры `-ApiPort`, `-InputName`, `-BackupPath` предназначены для изолированных проверок. Число CPU/affinity, кодировщик, SRT и звук скрипт не меняет.

Проверка инструмента: Windows PowerShell 5.1, сервер-имитатор с настоящим WebSocket handshake и проверкой SHA256-аутентификации; Inspect без записи; backup до изменения; применение только одного поля через overlay; Restore; запрет повторного теста поверх backup; отказ при активной записи/эфире. На новом физическом главном ПК инструмент ещё не запускался.
