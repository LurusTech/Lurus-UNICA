# STORY-030: Alert Rules + Webhook Notifications

**Epic:** EPIC-005 (Data Reports & Monitoring)
**Priority:** Must Have
**Story Points:** 5
**Status:** Completed
**Assigned To:** Unassigned
**Created:** 2026-03-06
**Sprint:** 6

---

## User Story

As a system admin,
I want alerts when channels fail or queues back up,
So that I can respond before customers are affected.

---

## Description

### Background
With all 5 channels operational and AI processing live, system failures directly impact customer experience. Admins need proactive alerting when error rates spike, queues back up, or AI response times degrade. This story configures Prometheus AlertManager with rules for critical conditions and routes notifications to team chat (DingTalk/WeCom/Feishu webhooks).

### Scope
**In scope:**
- Prometheus alert rules for critical system conditions
- AlertManager configuration with webhook notification routing
- Webhook adapter for DingTalk, WeCom, and Feishu
- Alert history and acknowledgment tracking
- Channel traffic anomaly detection
- Grafana alert panel showing active/resolved alerts

**Out of scope:**
- Email/SMS notifications (future enhancement)
- Auto-remediation actions
- Custom alert rule UI (admins edit YAML)

### User Flow
1. System detects channel error rate > 5% sustained for 5 minutes
2. Prometheus fires alert based on rule
3. AlertManager routes to configured webhook
4. Team receives DingTalk/WeCom message with alert details
5. Admin investigates via Grafana dashboard link in alert
6. Admin acknowledges alert in AlertManager UI
7. When condition resolves, "resolved" notification sent

---

## Acceptance Criteria

- [ ] Alert rule: channel error rate > 5% for 5 minutes (per channel)
- [ ] Alert rule: Redis stream queue depth > 1000 for 5 minutes
- [ ] Alert rule: Dify API P95 latency > 3 seconds for 5 minutes
- [ ] Alert rule: service down (target missing for 1 minute)
- [ ] Alert rule: dead-letter queue growing (> 10 messages in 1 hour)
- [ ] Alert rule: agent pool empty for a product line (0 available agents for 5 minutes)
- [ ] AlertManager routes alerts to webhook endpoint
- [ ] Webhook adapter supports DingTalk, WeCom, and Feishu formats
- [ ] Alert notifications include: severity, service, description, Grafana dashboard link
- [ ] Resolved notifications sent when condition clears
- [ ] Alert history visible in Grafana (annotations or alert panel)
- [ ] Channel traffic reports visible in Grafana (FR-023: message volume by channel over time)

---

## Technical Notes

### Alert Rules (Prometheus)

```yaml
groups:
  - name: unica-alerts
    rules:
      - alert: ChannelHighErrorRate
        expr: rate(gateway_outbound_errors_total[5m]) / rate(gateway_outbound_total[5m]) > 0.05
        for: 5m
        labels:
          severity: critical
        annotations:
          summary: "Channel error rate > 5%"
          dashboard: "/d/gateway-channels"

      - alert: QueueBacklog
        expr: gateway_stream_depth > 1000
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Redis stream queue depth > 1000"

      - alert: DifySlowResponse
        expr: histogram_quantile(0.95, rate(router_dify_call_duration_seconds_bucket[5m])) > 3
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "Dify API P95 > 3s"

      - alert: ServiceDown
        expr: up == 0
        for: 1m
        labels:
          severity: critical
        annotations:
          summary: "Service {{ $labels.job }} is down"

      - alert: DeadLetterGrowing
        expr: increase(gateway_dead_letter_total[1h]) > 10
        for: 0m
        labels:
          severity: warning
        annotations:
          summary: "Dead-letter queue received > 10 messages in last hour"

      - alert: NoAvailableAgents
        expr: agent_pool_available == 0
        for: 5m
        labels:
          severity: warning
        annotations:
          summary: "No available agents for product line {{ $labels.product_line }}"
```

### Webhook Adapter

Small Go service or script that receives AlertManager webhook POST and formats for target platform:

```
deploy/alertmanager/
  webhook-adapter/
    main.go            -- HTTP server receiving AM webhooks
    dingtalk.go        -- DingTalk markdown message format
    wecom.go           -- WeCom markdown message format
    feishu.go          -- Feishu card message format
    config.yaml        -- Target webhook URLs per severity
```

### AlertManager Config
```yaml
route:
  receiver: 'webhook-adapter'
  group_by: ['alertname', 'severity']
  group_wait: 30s
  group_interval: 5m
  repeat_interval: 4h
  routes:
    - match:
        severity: critical
      repeat_interval: 1h

receivers:
  - name: 'webhook-adapter'
    webhook_configs:
      - url: 'http://webhook-adapter:8080/alert'
        send_resolved: true
```

### Channel Traffic Dashboard (FR-023)
- Add Grafana dashboard panel: message volume by channel type over time
- Use existing `gateway_inbound_total` and `gateway_outbound_total` metrics with channel label
- Daily/weekly aggregation views

---

## Dependencies

**Prerequisite Stories:**
- STORY-028: Prometheus + Grafana Dashboards (Prometheus must be deployed)

**External Dependencies:**
- DingTalk/WeCom/Feishu webhook URLs configured by admin
- AlertManager included in kube-prometheus-stack Helm chart

---

## Definition of Done

- [ ] Alert rules deployed and firing correctly in Prometheus
- [ ] AlertManager routing alerts to webhook adapter
- [ ] Webhook adapter deployed, sending formatted messages to at least one platform
- [ ] Test: simulate high error rate, verify alert fires and notification received
- [ ] Test: simulate queue backlog, verify warning sent
- [ ] Resolved notifications confirmed working
- [ ] Alert history visible in Grafana
- [ ] Channel traffic dashboard created (FR-023)
- [ ] All config files committed to `deploy/alertmanager/`
- [ ] Webhook adapter Dockerfile and K3s deployment manifest created

---

## Story Points Breakdown

- **Alert rules definition:** 1 point
- **AlertManager configuration:** 0.5 points
- **Webhook adapter (3 platforms):** 2 points
- **Channel traffic dashboard:** 0.5 points
- **Testing + validation:** 1 point
- **Total:** 5 points

**Rationale:** Webhook adapter for 3 platforms is the main development effort. Alert rules are configuration-heavy but well-defined.

---

## Progress Tracking

**Status History:**
- 2026-03-06: Created
- 2026-03-06: Implementation complete - all files created, Go tests passing (16/16)

**Actual Effort:** 5 points (matched estimate)

**Implementation Notes:**
- 6 Prometheus alert rules defined as PrometheusRule CRD
- AlertManager config added to prometheus-values.yaml with severity-based routing
- Webhook adapter Go service with DingTalk, WeCom, Feishu formatters
- HMAC-SHA256 signing for DingTalk and Feishu
- Channel traffic Grafana dashboard with 7 panels (FR-023)
- Active alerts table with annotation overlay for alert history
- K3s deployment with ConfigMap, Secret, Deployment, and Service
- 16 Go tests covering all handlers, formatters, and config loading
