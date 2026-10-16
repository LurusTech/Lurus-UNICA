SERVICES := gateway router admin reporter judge
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
	@for svc in $(SERVICES); do \
		echo "Building Docker image for $$svc..."; \
		docker build -t unica-$$svc:latest -f $$svc/Dockerfile .; \
	done

# Release archives for the standalone gate (judge/dist, one per platform).
judge-dist:
	bash judge/scripts/dist.sh

clean:
	@for svc in $(SERVICES); do \
		rm -f $$svc/$$svc; \
	done
