#!/usr/bin/env bash
set -euo pipefail

NAMESPACE="livekit"

# Как импортировать образ в containerd кластера.
# k3s:              k3s ctr images import -
# kubeadm/rke2:     ctr -n k8s.io images import -
# microk8s:         microk8s ctr images import -
IMPORT_CMD="k3s ctr images import -"

# ─── Сборка образов ───────────────────────────────────────────────────────────

echo "==> Сборка backend..."
docker build -t wedding-backend:latest ./backend

echo "==> Сборка frontend..."
docker build -t wedding-frontend:latest ./frontend

# ─── Загрузка в containerd ────────────────────────────────────────────────────

echo "==> Загрузка wedding-backend в кластер..."
docker save wedding-backend:latest | sudo $IMPORT_CMD

echo "==> Загрузка wedding-frontend в кластер..."
docker save wedding-frontend:latest | sudo $IMPORT_CMD

# ─── Применение манифестов ────────────────────────────────────────────────────

echo "==> Применение манифестов..."
kubectl apply -f k8s/secret.yaml
kubectl apply -f k8s/backend.yaml
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
echo "✓ Готово. Поды:"
kubectl get pods -n "$NAMESPACE" | grep wedding
