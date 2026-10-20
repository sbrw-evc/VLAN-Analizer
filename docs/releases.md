# Релизы и Docker-пакеты в GitHub

`.github/workflows/release.yml` выполняет полный CI, собирает `linux/amd64` + `linux/arm64`, публикует образ в GHCR и создаёт GitHub Release с автоматически сформированным списком изменений, адресом/digest образа, установочным архивом Compose и SHA-256 checksum.

## Запуск

- После изменения версии в `pyproject.toml` и слияния в `main` создаётся стабильный релиз `v<version>`.
- Можно запустить workflow вручную через **Actions → Release and publish Docker package → Run workflow**, указав версию и текущую ветку `main`.
- Поддерживается push тега `vX.Y.Z` / `vX.Y.Z-rc.N` (также alpha/beta). Версия без суффикса должна совпадать с `pyproject.toml`.
- Уже существующий релиз пропускается: его файлы не заменяются. Для новой публикации измените версию. Повторный запуск после неуспешной публикации до создания Release может повторно отправить образ той же версии.

Публикация использует встроенный `GITHUB_TOKEN` с `packages:write` и `contents:write` только в publish job. Отдельные Docker Hub credentials или сторонний registry не нужны. Job проверки имеет только чтение репозитория. Для запуска необходимы включённые GitHub Actions и разрешение публикации packages в настройках владельца/организации.

## Теги образа

Для версии `v0.2.0`:

```text
ghcr.io/sbrw-evc/vlan-analizer:v0.2.0
ghcr.io/sbrw-evc/vlan-analizer:0.2.0
ghcr.io/sbrw-evc/vlan-analizer:0.2
ghcr.io/sbrw-evc/vlan-analizer:latest
```

Prerelease публикуется отдельным тегом и не обновляет `latest`/стабильный minor. Рекомендуется фиксировать точную версию в `.env`, а для строгой воспроизводимости — digest в собственном Compose override.

Новый GHCR package может по умолчанию иметь private visibility, даже в публичном репозитории. Для анонимного `docker compose pull` владелец должен включить **Public** в Package settings; иначе выполните `docker login ghcr.io` с аккаунтом, имеющим доступ и токеном `read:packages`. Это настройка доступа пакета GitHub, а не самого приложения. Не сохраняйте registry token в репозитории или `.env` приложения.

## Установка из релиза

Скачайте `vlan-analizer-install.tar.gz` и `SHA256SUMS` из GitHub Release:

```bash
sha256sum -c SHA256SUMS
tar -xzf vlan-analizer-install.tar.gz
cp .env.example .env
docker compose pull app
docker compose up -d app
```

В архиве `.env.example` уже фиксирует версию этого релиза. Далее используйте браузерный мастер из `docs/setup.md`.

## Обновление

Сделайте резервные копии persistent volumes, поменяйте `VLAN_ANALIZER_VERSION` в `.env`, затем:

```bash
docker compose pull app
docker compose up -d app
```

Настройки и секреты сохраняются в persistent volume/OpenBao, а история — в ClickHouse. `docker compose down -v` удаляет volumes и для обновления не нужен. Обратный переход версии допускайте только при совместимости схем/формата настроек.

## Локальная разработка

Production `compose.yaml` не содержит `build`. Для сборки из исходников:

```bash
docker compose -f compose.yaml -f compose.dev.yaml up -d --build app
```
