#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="livekit"

# Как импортировать образ в containerd кластера.
# k3s:              k3s ctr images import -
# kubeadm/rke2:     ctr -n k8s.io images import -
# microk8s:         microk8s ctr images import -
IMPORT_CMD="k3s ctr images import -"

# ─── Аргумент: способ загрузки ────────────────────────────────────────────────
#   ./deploy.sh simple   — один POST на файл (по умолчанию)
#   ./deploy.sh chunked  — чанки 4 МБ с ретраями и resume для файлов >10 МБ
UPLOAD_MODE="${1:-}"
if [[ "$UPLOAD_MODE" != "simple" && "$UPLOAD_MODE" != "chunked" ]]; then
  echo "Использование: $0 <simple|chunked>" >&2
  echo "  simple  — каждый файл одним запросом" >&2
  echo "  chunked — чанки с ретраями и resume для больших файлов" >&2
  exit 1
fi

echo "==> Режим загрузки: UPLOAD_MODE=$UPLOAD_MODE"

# ─── Сборка образов ───────────────────────────────────────────────────────────

echo "==> Сборка backend..."
docker build --build-arg UPLOAD_MODE="$UPLOAD_MODE" -t wedding-backend:latest ./backend

echo "==> Сборка frontend..."
docker build --build-arg UPLOAD_MODE="$UPLOAD_MODE" -t wedding-frontend:latest ./frontend

# ─── Загрузка в containerd ────────────────────────────────────────────────────

echo "==> Загрузка wedding-backend в кластер..."
docker save wedding-backend:latest | sudo $IMPORT_CMD

echo "==> Загрузка wedding-frontend в кластер..."
docker save wedding-frontend:latest | sudo $IMPORT_CMD

# ─── Применение манифестов ────────────────────────────────────────────────────
# В backend.yaml UPLOAD_MODE захардкожен как simple — подменяем значение на
# лету через sed, чтобы не плодить грязный diff в репозитории.

echo "==> Применение манифестов..."
kubectl apply -f k8s/secret.yaml
sed "/name: UPLOAD_MODE/{n;s|value: .*|value: $UPLOAD_MODE|;}" k8s/backend.yaml | kubectl apply -f -
kubectl apply -f k8s/frontend.yaml

# ─── Рестарт деплойментов (форсируем подхват нового образа) ──────────────────

echo "==> Рестарт деплойментов..."
kubectl rollout restart deployment/wedding-backend  -n "$NAMESPACE"
kubectl rollout restart deployment/wedding-frontend -n "$NAMESPACE"

# ─── Ожидание готовности ──────────────────────────────────────────────────────

echo "==> Ожидание rollout backend..."
kubectl rollout status deployment/wedding-backend  -n "$NAMESPACE" --timeout=120s

echo "==> Ожидание rollout frontend..."
kubectl rollout status deployment/wedding-frontend -n "$NAMESPACE" --timeout=120s

echo ""
echo "✓ Готово (UPLOAD_MODE=$UPLOAD_MODE). Поды:"
kubectl get pods -n "$NAMESPACE" | grep wedding
