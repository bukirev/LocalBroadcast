# Local Broadcast — протокол и порты (schema 2)

## Discovery / Control

- UDP `39721`: объявление главного координатора в локальной сети.
- HTTP `39780/TCP` на главном OBS; обязательный заголовок `X-LB-Key`, общий ключ минимум 16 символов. Это **не TLS**; используйте только доверенную LAN.
- `POST /v1/heartbeat`: JSON `{nodeId,name,mode,sendScreen,sendCamera,watchPresenter,mediaStatus,version,rttMs}`. IP берётся из TCP-соединения, а не из тела запроса.
- `GET /v1/plan?id=<nodeId>`: `Plan` с направлениями и выбранным главным ПК `transport`, `srtLatencyMs`, `srtAdaptive`, `generation`.
- Только главный ПК определяет транспорт; клиентская локальная конфигурация для передачи может быть переопределена планом.

## Стабильные входные порты

| Назначение | UDP | SRT Listener |
|---|---:|---:|
| Презентер | 5000 | 11000 |
| Камера 1 | 5101 | 11101 |
| Камера N | 5100+N | 11100+N |

SRT stream on wire — MPEG-TS with encoded H.264/AAC. FFmpeg `libsrt`, `mode=caller/listener`, `latency` in microseconds, `passphrase`, `pbkeylen=16`. FFmpeg remux SRT listener -> UDP localhost target. OBS Media Source remains `udp://0.0.0.0:port` for both modes.

## Local UI/API

`http://127.0.0.1:39888` локальная панель, HTTP endpoints `/api/state`, `/api/config`, `/api/route`, `/api/sync`, `/api/devices`, `/api/action`. POST требует `X-LB-UI: 1` и `Content-Type: application/json`; Host / Origin проверяются. Панель не предназначена для удалённого браузера.

`POST /api/sync` на главном ПК: `{sourceId,videoMs,audioMs}` — сохраняет калибровку только существующего источника; изменение применяется при очередной синхронизации OBS. Ограничения: video [-2000;10000] ms, audio [-950;10000] ms. Фактически применённый video delay ограничен >=0, audio offset -950...20000 ms.

## Совместимость

В 0.2.0 миграция конфигурации schema 1 -> 2 сохраняет ID, ключ, маршруты, порты и роли. Для SRT на **всех ПК** требуется приложение v0.2 и FFmpeg с libsrt. Смешанные версии в одной системе пока не поддерживаются явным negotiation; обновите все узлы одновременно.
