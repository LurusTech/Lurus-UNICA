module github.com/kefu/unica/admin

go 1.23.0

require (
	github.com/golang-jwt/jwt/v5 v5.2.1
	github.com/kefu/unica/pkg v0.0.0
	github.com/lib/pq v1.10.9
	github.com/redis/go-redis/v9 v9.7.3
	golang.org/x/crypto v0.31.0
)

require (
	github.com/alicebob/miniredis/v2 v2.37.0 // indirect
	github.com/beorn7/perks v1.0.1 // indirect
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	github.com/munnerz/goautoneg v0.0.0-20191010083416-a7dc8b61c822 // indirect
	github.com/prometheus/client_golang v1.23.2 // indirect
	github.com/prometheus/client_model v0.6.2 // indirect
	github.com/prometheus/common v0.66.1 // indirect
	github.com/prometheus/procfs v0.16.1 // indirect
	github.com/yuin/gopher-lua v1.1.1 // indirect
	go.yaml.in/yaml/v2 v2.4.2 // indirect
	golang.org/x/sys v0.35.0 // indirect
	google.golang.org/protobuf v1.36.8 // indirect
)

replace github.com/kefu/unica/pkg => ../pkg
