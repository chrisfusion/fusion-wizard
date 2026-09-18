# SPDX-License-Identifier: GPL-3.0-or-later

CONTROLLER_GEN ?= $(HOME)/go/bin/controller-gen
IMG ?= fusion-wizard:0.1.0
NAMESPACE ?= fusion
CHART_CRDS := deployment/fusion-wizard/crds

.PHONY: all build test generate sync-crds check-crds docker-build create-namespace install-crds

all: generate build

## Generate deepcopy methods and CRD manifests.
generate:
	$(CONTROLLER_GEN) object:headerFile="" paths="./api/..."
	$(CONTROLLER_GEN) crd paths="./api/..." output:crd:dir=config/crd/bases

## Copy generated CRDs into the Helm chart (Helm never updates crds/ itself; kubectl apply afterwards).
sync-crds: generate
	mkdir -p $(CHART_CRDS)
	cp config/crd/bases/*.yaml $(CHART_CRDS)/

## Fail when the chart's CRDs drifted from the generated ones.
check-crds: generate
	diff -r config/crd/bases $(CHART_CRDS)

## Build the operator and REST API binaries.
build: generate
	CGO_ENABLED=0 go build -o bin/manager ./cmd/
	CGO_ENABLED=0 go build -o bin/api-server ./cmd/api/

## Run unit tests (race detector on: the ledger and reconciler are concurrent).
test:
	go test ./... -race

## Build the Docker image inside minikube's daemon so pods can use it directly.
docker-build:
	eval $$(minikube docker-env) && docker build -t $(IMG) .

## Create the target namespace (idempotent).
create-namespace:
	kubectl create namespace $(NAMESPACE) --dry-run=client -o yaml | kubectl apply -f -

## Install CRDs into the cluster.
install-crds:
	kubectl apply -f config/crd/bases/
