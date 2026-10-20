# VLAN Analizer

Docker-приложение для проверки доступности сайтов через разные сетевые интерфейсы Linux-хоста. Дашборд показывает матрицу **сайт × интерфейс/VLAN**, подробности проверки, историю задержек и инвентаризацию сети. Интерфейс приложения — на английском языке.

**Статус: первая реализация, предназначенная для проверки на вашем Linux-хосте.** Локальные тесты не заменяют приёмку маршрутизации на реальном trunk-порту. IPv4 используется для проверок; IPv6 пока только отображается в инвентаризации.

## Возможности

- Обнаружение VLAN-интерфейсов 802.1Q/802.1ad, обычных интерфейсов и Linux bridges через `ip -j -d address`; MAC, MTU, IP, состояние, parent/master, маршруты всех таблиц.
- Отдельное отображение bridge VLAN membership через `bridge -j vlan show`.
- Ручной ввод сайтов, изменение, пауза и удаление; синхронизация с self-hosted Gatus API/YAML и БД Uptime Kuma MySQL/MariaDB каждые 5 минут.
- DNS A-запросы с ответами и временем; TCP connect; проверка сертификата и hostname; TLS version/cipher/handshake; subject/issuer/SAN, сроки и SHA-256 отпечаток leaf-сертификата.
- HTTP/HTTPS: код, конечный URL, redirects, TTFB, длительность загрузки тела, объём; Chromium: событие `load`, DOMContentLoaded, failed requests и HTTP-ошибки ресурсов.
- UDP traceroute через выбранный интерфейс, до 12 переходов. Отсутствие ответа hop само по себе не означает недоступность сайта.
- ClickHouse: сайты, снимки интерфейсов и временные ряды; история на дашборде за 24 часа.
- Microsoft Entra ID (single-tenant OIDC authorization code + PKCE, проверка подписи/issuer/audience/nonce/state) и локальные пользователи.
- Роли `admin` / `viewer`; локальные Argon2id-хеши и секреты приложения в OpenBao KV v2; AppRole с ограниченной политикой.
- HTTP JSON и LLD discovery для Zabbix, отдельный Bearer-токен.

## Как обеспечивается проверка из нужного VLAN

Контейнер приложения использует `network_mode: host` и **не создаёт/не изменяет** интерфейсы, адреса, маршруты и firewall хоста. Нужны Linux и обычный rootful Docker Engine; Docker Desktop и rootless Docker для этого режима не подходят.

DNS, TCP и TLS используют сокеты с `SO_BINDTODEVICE` и исходным IPv4-адресом выбранного интерфейса. HTTP-запросы и Chromium идут через временный локальный proxy, который также разрешает имена и открывает **каждое** upstream-соединение через этот интерфейс. Это относится к redirects, CSS, изображениям, скриптам и другим загружаемым ресурсам. Прямое разрешение имён Chromium отключено, QUIC отключён. Proxy работает только на loopback в течение одной проверки и не предоставляет общий сетевой сервис.

В отличие от выбора одного source IP, привязка к устройству не должна позволить проверке незаметно уйти через default-интерфейс. Однако рабочие адреса, шлюзы и policy routing **необходимо подготовить на хосте**. Для L2-порта, включённого в bridge, проверка выполняется через соответствующий L3 bridge/VLAN-интерфейс с адресом. L2 slave исключается из проверок.

Приложение видит настроенные VLAN и bridge membership. Оно не может определить все разрешённые VLAN на внешнем коммутаторе по одному trunk-интерфейсу и не выполняет перебор VLAN ID. Для untagged-интерфейса ID отображается как `Untagged / unknown`, а не предполагается VLAN 1. Для VLAN-aware bridge без отдельного адресованного L3-интерфейса на каждый VLAN индивидуальная проверка невозможна.

## Запуск

Требования: Linux x86_64/arm64 с поддержкой Docker/Chromium, Docker Compose v2, минимум 4 ГБ RAM для небольшой установки (потребление зависит от Chromium и числа параллельных проверок). Запускайте только доверенные сайты: браузер скачивает их ресурсы и выполняет JavaScript.

### 1. Получить код и настроить окружение

```bash
git clone https://github.com/sbrw-evc/VLAN-Analizer.git
cd VLAN-Analizer
cp .env.example .env
python3 deploy/prepare.py
docker compose build app
docker compose up -d clickhouse openbao
```

В `.env` задайте:

- `PUBLIC_URL`: точный origin приложения, например `https://vlan.example.com`, без завершающего `/`. Он используется и для callback Entra ID, и для защиты операций записи от CSRF.
- `INTERFACES`: явный список **L3** интерфейсов, например `eno1.10,eno1.20`. Пустое значение проверяет все подходящие интерфейсы, включая возможные Docker bridge/veth — предпочтительнее явный список.
- `VLAN_DNS`: JSON с доступными DNS для каждого интерфейса. Пример: `{"eno1.10":["192.168.10.1"],"eno1.20":["192.168.20.1"]}`. Использовать системный stub `127.0.0.53` для проверки DNS через удалённый VLAN нельзя. Для интерфейсов без записи используется IPv4 nameserver из `/etc/resolv.conf`; он должен быть достижим через этот интерфейс.

### 2. Инициализировать и разблокировать OpenBao

```bash
docker compose exec openbao bao operator init
docker compose exec openbao bao operator unseal
```

Сохраните recovery/unseal-материалы и initial root token в вашем защищённом хранилище. Выполните `unseal` с необходимым числом различных ключей, указанным при инициализации. При новом запуске OpenBao его требуется снова разблокировать; для автоматического unseal подключите ваш KMS согласно документации OpenBao.

Это **persistent server**, не dev mode. Данные OpenBao остаются в named volume `bao_data`. Не удаляйте его вместе с секретами. В комплекте односерверное file storage без HA; HTTP OpenBao и ClickHouse опубликован только на loopback хоста. Для внешних экземпляров используйте HTTPS и доверенные CA.

### 3. Создать AppRole, секреты приложения и первого администратора

```bash
docker run --rm -it --network host \
  -e SECRETS_DIR=/bootstrap-secrets \
  -v "$PWD/secrets:/bootstrap-secrets" \
  vlan-analizer:local python deploy/bootstrap.py
```

Скрипт интерактивно запросит provisioning token OpenBao, имя/пароль администратора и необязательный Entra client secret. Токены и пароли не выводятся. Runtime получает AppRole RoleID/SecretID из файлов Docker secrets; root token приложению не передаётся. После настройки отзовите временный provisioning/root token в рамках вашей политики OpenBao.

Файлы `secrets/bao_role_id` и `secrets/bao_secret_id` — минимальные bootstrap-credentials доступа к OpenBao. `secrets/clickhouse_password` требуется entrypoint ClickHouse; его копия хранится в KV приложения. Эти файлы исключены из git и создаются с правами `0600`; защищайте каталог и резервные копии. Все локальные пароли хранятся **только как Argon2id-хеши в OpenBao**, не в ClickHouse и не в `.env`.

Скрипт рассчитан на первоначальную настройку пустого приложения, не на повторную ротацию существующей установки.

### 4. Запустить приложение

```bash
docker compose up -d app
docker compose ps
docker compose logs --tail=100 app
```

Приложение слушает `127.0.0.1:8000`. Настройте ваш HTTPS reverse proxy; пример в [deploy/nginx.conf.example](deploy/nginx.conf.example). Для Nginx Proxy Manager в отдельном bridge-контейнере `127.0.0.1` обозначает сам NPM: настройте доступ к сервису хоста осознанно либо используйте host-network proxy. Не публикуйте ClickHouse/OpenBao наружу.

Для локальной приёмки допустимо временно задать `PUBLIC_URL=http://localhost:8000` и открыть именно этот origin на самом хосте. Для доступа через браузер другого компьютера используйте HTTPS reverse proxy с настроенным публичным origin.

Войдите локальным администратором и добавьте первый сайт. Проверьте раздел **Networks**, затем матрицу. Кнопка **Run checks** ставит новый полный цикл в очередь; цикл не запускается параллельно самому себе.

## Entra ID

1. Создайте single-tenant App Registration.
2. Добавьте Web redirect URI: `https://vlan.example.com/auth/entra/callback`.
3. Создайте app roles со значениями **`VLAN.Admin`** и **`VLAN.Viewer`**, доступные пользователям/группам.
4. Назначьте роли пользователям в Enterprise Applications. Одна принадлежность tenant не даёт доступ.
5. Укажите `ENTRA_TENANT_ID` (UUID) и `ENTRA_CLIENT_ID` в `.env`, client secret — в `secret/vlan-analizer/app` через `bao kv patch`. Не храните его в git или `.env`.
6. Пересоздайте контейнер приложения после изменения окружения; после изменения app-секретов перезапустите его.

Сессия — HttpOnly SameSite=Lax cookie, Secure при HTTPS, до 8 часов. OIDC flow cookie действует 10 минут. Обновление/отключение локального пользователя немедленно инвалидирует его сессии при следующем обращении. Изменение назначения роли Entra применяется при следующем входе или по истечении текущей сессии; для немедленного массового отзыва смените `session_key` и перезапустите приложение.

Локальные пользователи создаются и обновляются в **Administration**. Viewer может читать дашборд и историю; запись, запуск проверок, импорт Gatus и управление пользователями доступны admin. Endpoint для смены локального пользователя требует новый пароль минимум из 12 символов; отключение также выполняется с новым паролем.

## Gatus

### API self-hosted Gatus

```dotenv
GATUS_URL=https://status.example.com
```

Импортируется `/api/v1/endpoints/statuses`; Bearer при необходимости берётся из `gatus_token` в OpenBao. Набор импортируемых URL зависит от версии и настроек Gatus. Hosted Gatus API имеет другой контракт и в эту версию не включён.

### YAML Gatus

Если URL скрыты API, подключите исходный конфигурационный файл:

```yaml
# Добавьте в app.volumes в compose.yaml
- /opt/gatus/config.yaml:/config/gatus.yaml:ro
```

```dotenv
GATUS_CONFIG=/config/gatus.yaml
```

```yaml
endpoints:
  - name: Customer portal
    group: internal
    url: https://portal.example.com
    conditions:
      - "[STATUS] == 200"
```

YAML имеет приоритет над API. Импортируются только HTTP/HTTPS URL; шаблоны переменных окружения Gatus не подставляются. Условия, credentials и статус Gatus не копируются — доступность проверяется приложением самостоятельно из каждого интерфейса. Записи без URL и неподдерживаемые схемы отражаются как предупреждения.

Синхронизация сохраняет ручные сайты и состояние паузы импортированных сайтов. После полного импорта без предупреждений исчезнувшие из Gatus записи удаляются из активного списка; история сохраняется. Если часть URL не видна, автоматическое удаление пропущенных записей не выполняется. Импорт ограничен 1 000 endpoints за раз. Удалять импортированную запись следует в Gatus; в приложении можно поставить её на паузу.

## Uptime Kuma: импорт из MySQL/MariaDB

Добавлены кнопка **Sync Kuma**, endpoint `POST /api/kuma/sync` и автоматическая синхронизация источника. Читаются HTTP-мониторы из таблицы `monitor` Uptime Kuma 2.x; учитываются паузы мониторов/родительских групп, локальные паузы сохраняются. Доступ к БД — read-only; пароль хранится в OpenBao как `kuma_mysql_password`. История и результаты проверок Kuma не импортируются — приложение проверяет сайты самостоятельно из VLAN.

Подробная настройка подключения, TLS, SQL-права и правила обновления: [docs/kuma.md](docs/kuma.md).

## Статусы и измерения

| Статус | Условие |
|---|---|
| Healthy / `up` | TCP доступен, конечный HTTP 200–399, TLS проходит проверку; при включённом Chromium загрузка страницы и её ресурсов успешна |
| Degraded / `degraded` | HTTP доступен, но Chromium не завершил загрузку либо ресурс вернул ошибку/не загрузился |
| Down / `down` | Ошибка DNS/TCP/HTTP/TLS или конечный HTTP-код вне 200–399 |
| Error / `error` | Внутренняя ошибка или общий timeout проверки |
| Unknown / `unknown` | Нет результатов либо измерение старше `STALE_AFTER` |

При выключенном `BROWSER_ENABLED` статус `up` подтверждает HTTP, но не браузерный рендеринг. `load` отражает начальную загрузку документа и ресурсов; это не синтетический пользовательский сценарий для SPA, авторизованных страниц или бесконечной фоновой загрузки. Поздние запросы после события `load` могут не попасть в результат. HTTP-код 200 не гарантирует корректность содержимого или бизнес-функции сайта.

- `dns.ms` — A-resolution через DNS выбранного интерфейса; DNS over UDP с TCP fallback при truncation.
- `tcp_ms` и `tls.handshake_ms` — отдельное прямое соединение через интерфейс, без proxy. Проверяется доступный адрес из списка A records; downstream-запросы могут выбрать другой адрес при DNS round-robin.
- `http.ttfb_ms` и `http.total_ms` — время запроса curl через локальный bound proxy, включая DNS/proxy/redirect overhead; это не чистый TCP connect.
- `browser.load_ms`, `dom_ms`, `ttfb_ms` — Chromium Navigation Timing; отдельный запрос от curl.
- TLS-валидация никогда не отключается для HTTP/страницы. Для невалидного сертификата дополнительное отдельное соединение может собрать leaf metadata без доверия, сохраняя `valid=false`.
- Проверяются IPv4 A records, разрешены веб-порты 80/443/8080/8443; произвольные TCP-сервисы, AAAA/IPv6 и перебор TLS cipher suites пока не реализованы.

Настройте `CHECK_CONCURRENCY`, `CHECK_TIMEOUT`, `CHECK_INTERVAL` и `STALE_AFTER` под число пар «сайт × интерфейс». Интервал отсчитывается **после окончания** цикла; длинный цикл может превысить порог свежести, что будет честно показано на дашборде. Браузер запускается для каждой пары, поэтому большие установки требуют отдельного расчёта ресурсов.

## ClickHouse и эксплуатация

- `sites`: ReplacingMergeTree с версиями конфигурации, чтение актуального состояния с `FINAL`.
- `inventory`: снимки интерфейсов, TTL 30 дней.
- `checks`: измерения и подробные JSON payload, TTL 90 дней, месячные partition.
- Таблицы создаются идемпотентно при старте; поле `sites.source_enabled` добавляется через `ALTER ... ADD COLUMN IF NOT EXISTS` для существующих установок.
- `/healthz` — процесс жив; `/readyz` — доступны ClickHouse/OpenBao и нет последней ошибки цикла. Зависший/долгий сбор дополнительно виден по freshness результатов.
- Один app-контейнер, один Uvicorn worker на один хост. Несколько реплик сейчас будут дублировать проверки; distributed leader election не реализован.
- Для внутренних CA добавьте сертификаты в образ и обновите системное CA-хранилище; настройте trust store Chromium отдельно и проверьте его на вашей CA. Не отключайте проверку TLS для удобства.
- Секреты `app` кэшируются до перезапуска процесса. AppRole самостоятельно повторяет вход после истечения lease. Ротация `SecretID` требует обновления Docker secret и пересоздания контейнера.
- Резервируйте ClickHouse и OpenBao вместе с recovery-материалами. `docker compose down -v` удалит данные; для остановки используйте `docker compose stop`.

Подробная инструкция для Zabbix: [docs/zabbix.md](docs/zabbix.md). Проверки перед вводом в эксплуатацию: [docs/acceptance.md](docs/acceptance.md).

## Разработка и тесты

```bash
python3 -m venv .venv
. .venv/bin/activate
pip install -r requirements.lock
pip install -e '.[test]'
ruff check app deploy tests
pytest -q
```

Три runtime-теста выполняются отдельно: `RUN_BROWSER_TESTS=1` с установленным Chromium и `RUN_STORAGE_TESTS=1` с тестовыми ClickHouse/OpenBao, `RUN_KUMA_TESTS=1` с disposable MariaDB (адреса и disposable credentials описаны в `tests/test_runtime.py`). GitHub Actions устанавливает Chromium, проверяет UI с явно синтетическими API fixtures, реальные HTTP/browser-пробы, хранение в ClickHouse/OpenBao, read-only импорт из MariaDB и Docker build. Тесты **никогда не запускайте против рабочей БД/OpenBao**, они создают/изменяют тестовую конфигурацию.

При первоначальной локальной проверке в среде разработки: **36 passed, 3 runtime-теста skipped**. Docker Engine недоступен, netlink запрещён политикой среды, Chromium не удалось скачать. Полный Compose, UI в браузере и runtime-тесты требуют выполнения CI/приёмки на целевом Linux-хосте; успех этих проверок здесь не заявлен.

## Основные официальные источники

- [Linux SO_BINDTODEVICE](https://man7.org/linux/man-pages/man7/socket.7.html)
- [Docker host networking](https://docs.docker.com/engine/network/drivers/host/)
- [Gatus self-hosted](https://github.com/TwiN/gatus)
- [Playwright network / proxy](https://playwright.dev/python/docs/network)
- [Microsoft Entra claims validation](https://learn.microsoft.com/en-us/entra/identity-platform/claims-validation)
- [OpenBao KV v2](https://openbao.org/docs/secrets/kv/kv-v2/)
- [ClickHouse ReplacingMergeTree](https://clickhouse.com/docs/engines/table-engines/mergetree-family/replacingmergetree)
