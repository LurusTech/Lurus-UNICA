SERVICES := gateway router admin reporter
MODULES := github.com/kefu/unica/pkg/... github.com/kefu/unica/gateway/... github.com/kefu/unica/router/... github.com/kefu/unica/admin/... github.com/kefu/unica/reporter/...

.PHONY: build test lint docker-build clean

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

clean:
	@for svc in $(SERVICES); do \
		rm -f $$svc/$$svc; \
	done
