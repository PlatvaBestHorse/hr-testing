.PHONY: init install up down db migrate api web test integration runtime-check build check admin admin-dev

init:
	python3 scripts/init_env.py

install:
	cd backend && go mod download
	cd frontend && npm ci

up: init
	docker compose up -d --build --wait

down:
	docker compose down

db: init
	docker compose up -d --wait db

migrate:
	python3 scripts/run_backend.py migrate

api:
	python3 scripts/run_backend.py api

admin:
	python3 scripts/create_admin.py

admin-dev:
	python3 scripts/create_admin.py --dev

web:
	cd frontend && npm run dev

test:
	cd backend && go test ./...
	cd frontend && npm test

integration:
	python3 scripts/run_backend.py integration

runtime-check:
	node scripts/check_attempt_runtime.mjs

build:
	cd backend && go build ./cmd/api ./cmd/migrate ./cmd/bootstrap ./cmd/set-password
	cd frontend && npm run build

check: test build
	cd backend && go vet ./...
