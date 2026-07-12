package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"

	"gopkg.in/yaml.v3"
)

// AlertManagerPayload represents the webhook POST body from AlertManager.
type AlertManagerPayload struct {
	Version           string            `json:"version"`
	GroupKey           string            `json:"groupKey"`
	TruncatedAlerts   int               `json:"truncatedAlerts"`
	Status            string            `json:"status"`
	Receiver          string            `json:"receiver"`
	GroupLabels       map[string]string  `json:"groupLabels"`
	CommonLabels      map[string]string  `json:"commonLabels"`
	CommonAnnotations map[string]string  `json:"commonAnnotations"`
	ExternalURL       string            `json:"externalURL"`
	Alerts            []Alert           `json:"alerts"`
}

// Alert represents a single alert within the AlertManager payload.
type Alert struct {
	Status       string            `json:"status"`
	Labels       map[string]string `json:"labels"`
	Annotations  map[string]string `json:"annotations"`
	StartsAt     string            `json:"startsAt"`
	EndsAt       string            `json:"endsAt"`
	GeneratorURL string            `json:"generatorURL"`
	Fingerprint  string            `json:"fingerprint"`
}

// Config holds the webhook adapter configuration.
type Config struct {
	Server   ServerConfig              `yaml:"server"`
	Targets  map[string][]TargetConfig `yaml:"targets"`
}

// ServerConfig holds the HTTP server settings.
type ServerConfig struct {
	Port int `yaml:"port"`
}

// TargetConfig defines a single webhook target.
type TargetConfig struct {
	Platform string `yaml:"platform"`
	URL      string `yaml:"url"`
	Secret   string `yaml:"secret,omitempty"`
}

// Notifier sends formatted alerts to a webhook target.
type Notifier interface {
	Send(url string, secret string, payload AlertManagerPayload) error
}

var notifiers = map[string]Notifier{
	"dingtalk": &DingTalkNotifier{},
	"wecom":    &WeComNotifier{},
	"feishu":   &FeishuNotifier{},
}

func loadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8080
	}

	return &cfg, nil
}

func alertHandler(cfg *Config) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var payload AlertManagerPayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			log.Printf("ERROR: decode payload: %v", err)
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}

		// Determine severity from common labels
		severity := payload.CommonLabels["severity"]
		if severity == "" {
			severity = "default"
		}

		log.Printf("INFO: received alert group status=%s severity=%s alerts=%d",
			payload.Status, severity, len(payload.Alerts))

		// Look up targets: try severity-specific first, fall back to "default"
		targets := cfg.Targets[severity]
		if len(targets) == 0 {
			targets = cfg.Targets["default"]
		}

		if len(targets) == 0 {
			log.Printf("WARN: no targets configured for severity=%s", severity)
			w.WriteHeader(http.StatusOK)
			return
		}

		// Send to all configured targets
		delivered := 0
		for _, target := range targets {
			notifier, ok := notifiers[target.Platform]
			if !ok {
				log.Printf("WARN: unknown platform %q, skipping", target.Platform)
				continue
			}

			if err := notifier.Send(target.URL, target.Secret, payload); err != nil {
				log.Printf("ERROR: send to %s: %v", target.Platform, err)
			} else {
				delivered++
				log.Printf("INFO: sent to %s successfully", target.Platform)
			}
		}

		// If nothing was actually delivered (all sends failed and/or every target
		// was skipped as an unknown platform), report failure so AlertManager
		// retries instead of silently dropping the alert.
		if delivered == 0 {
			http.Error(w, "no targets delivered", http.StatusBadGateway)
			return
		}

		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
	}
}

func healthHandler(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(`{"status":"healthy"}`))
}

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		log.Fatalf("FATAL: load config: %v", err)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/alert", alertHandler(cfg))
	mux.HandleFunc("/health", healthHandler)

	addr := fmt.Sprintf(":%d", cfg.Server.Port)
	log.Printf("INFO: webhook-adapter starting on %s", addr)
	if err := http.ListenAndServe(addr, mux); err != nil {
		log.Fatalf("FATAL: server: %v", err)
	}
}
