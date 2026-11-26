SERVICES := gateway router admin reporter judge
# The delivery stack images. judge has its own Dockerfile and `make judge-dist`;
# it is not part of this build (f7c8f8f).
IMAGES := gateway router admin reporter
# Extra docker build flags, e.g. DOCKER_BUILD_ARGS="--build-arg GOPROXY=https://goproxy.cn,direct"
DOCKER_BUILD_ARGS ?=
MODULES := github.com/kefu/unica/pkg/... github.com/kefu/unica/gateway/... github.com/kefu/unica/router/... github.com/kefu/unica/admin/... github.com/kefu/unica/reporter/... github.com/kefu/unica/judge/...

.PHONY: build test lint docker-build judge-dist clean

build:
	@for svc in $(SERVICES); do \
		echo "Building $$svc..."; \
		cd $$svc && go build ./... && cd ..; \
	done

test:
	go test $(MODULES)

lint:
	go vet $(MODULES)

docker-build:
	@for svc in $(IMAGES); do \
		echo "Building Docker image for $$svc..."; \
		docker build $(DOCKER_BUILD_ARGS) -t unica-$$svc:latest -f $$svc/Dockerfile . || exit 1; \
	done

# Release archives for the standalone gate (judge/dist, one per platform).
judge-dist:
	bash judge/scripts/dist.sh

clean:
	@for svc in $(SERVICES); do \
		rm -f $$svc/$$svc; \
	done
