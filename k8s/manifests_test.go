package k8s

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

type manifestDoc struct {
	file       string
	apiVersion string
	kind       string
	name       string
	raw        string
}

func splitYAMLDocs(t *testing.T, filename string) []manifestDoc {
	t.Helper()
	raw, err := os.ReadFile(filename)
	if err != nil {
		t.Fatalf("read %s: %v", filename, err)
	}
	parts := regexp.MustCompile(`(?m)^---\s*$`).Split(string(raw), -1)
	var docs []manifestDoc
	apiRe := regexp.MustCompile(`(?m)^apiVersion:\s*(\S+)`)
	kindRe := regexp.MustCompile(`(?m)^kind:\s*(\S+)`)
	nameRe := regexp.MustCompile(`(?m)^metadata:\s*\n(?:\s+[^\n]+\n)*?\s+name:\s*(\S+)`)

	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		doc := manifestDoc{file: filepath.Base(filename), raw: trimmed}
		if m := apiRe.FindStringSubmatch(trimmed); len(m) == 2 {
			doc.apiVersion = m[1]
		}
		if m := kindRe.FindStringSubmatch(trimmed); len(m) == 2 {
			doc.kind = m[1]
		}
		if m := nameRe.FindStringSubmatch(trimmed); len(m) == 2 {
			doc.name = m[1]
		}
		docs = append(docs, doc)
	}
	return docs
}

var expectedServices = map[string]string{
	"api-gateway":                   "8080",
	"catalog-pricing-service":       "8081",
	"inventory-warehouse-service":   "8082",
	"order-checkout-service":        "8083",
	"payment-service":               "8084",
	"fulfillment-shipping-service":  "8085",
	"ledger-reconciliation-service": "8086",
	"iam-auth-service":              "8087",
	"backoffice-support-service":    "8088",
	"notification-service":          "8089",
}

func TestAllTenMicroservicesHaveHardenedDeploymentsAndServices(t *testing.T) {
	deployDocs := splitYAMLDocs(t, "deployments.yaml")
	svcDocs := splitYAMLDocs(t, "services.yaml")

	deployments := make(map[string]manifestDoc)
	for _, doc := range deployDocs {
		if doc.kind == "Deployment" {
			deployments[doc.name] = doc
		}
	}
	services := make(map[string]manifestDoc)
	for _, doc := range svcDocs {
		if doc.kind == "Service" {
			services[doc.name] = doc
		}
	}

	if len(deployments) != len(expectedServices) {
		t.Fatalf("deployments count = %d, want %d", len(deployments), len(expectedServices))
	}
	if len(services) != len(expectedServices) {
		t.Fatalf("services count = %d, want %d", len(services), len(expectedServices))
	}

	for svc, port := range expectedServices {
		d, ok := deployments[svc]
		if !ok {
			t.Fatalf("missing Deployment for %s", svc)
		}
		requiredSnippets := []string{
			"livenessProbe:",
			"readinessProbe:",
			"path: /health",
			"requests:",
			"limits:",
			"runAsNonRoot: true",
			"runAsUser: 10001",
			"allowPrivilegeEscalation: false",
			"readOnlyRootFilesystem: true",
			"terminationGracePeriodSeconds: 30",
			`prometheus.io/scrape: "true"`,
			`prometheus.io/path: "/metrics"`,
			`prometheus.io/port: "` + port + `"`,
		}
		for _, want := range requiredSnippets {
			if !strings.Contains(d.raw, want) {
				t.Errorf("Deployment %s missing %q", svc, want)
			}
		}

		s, ok := services[svc]
		if !ok {
			t.Fatalf("missing Service for %s", svc)
		}
		if !strings.Contains(s.raw, "port: "+port) || !strings.Contains(s.raw, "targetPort: "+port) {
			t.Errorf("Service %s missing port %s", svc, port)
		}
	}
}

func TestCorePlatformManifestsAndKustomization(t *testing.T) {
	kustRaw, err := os.ReadFile("kustomization.yaml")
	if err != nil {
		t.Fatalf("read kustomization.yaml: %v", err)
	}
	kust := string(kustRaw)
	requiredResources := []string{
		"namespace.yaml",
		"resource-quota.yaml",
		"configmap.yaml",
		"network-policies.yaml",
		"deployments.yaml",
		"services.yaml",
		"hpa.yaml",
	}
	for _, res := range requiredResources {
		if !strings.Contains(kust, "- "+res) {
			t.Errorf("kustomization.yaml missing resource %s", res)
		}
	}
	if strings.Contains(kust, "secrets.example.yaml") {
		t.Errorf("kustomization.yaml must not include secrets.example.yaml")
	}

	var (
		hasNamespace     bool
		hasResourceQuota bool
		hasLimitRange    bool
		hasConfigMap     bool
		hasDefaultDeny   bool
		hasInfraEgress   bool
		hpaTargets       = make(map[string]bool)
	)

	for _, file := range requiredResources {
		for _, doc := range splitYAMLDocs(t, file) {
			switch doc.kind {
			case "Namespace":
				if doc.name == "commercelab-retail" && strings.Contains(doc.raw, "pod-security.kubernetes.io/enforce:") {
					hasNamespace = true
				}
			case "ResourceQuota":
				hasResourceQuota = true
			case "LimitRange":
				hasLimitRange = true
			case "ConfigMap":
				if strings.Contains(doc.raw, "OTEL_EXPORTER_OTLP_ENDPOINT:") {
					hasConfigMap = true
				}
			case "NetworkPolicy":
				if doc.name == "default-deny-ingress" && strings.Contains(doc.raw, "podSelector: {}") && strings.Contains(doc.raw, "- Ingress") {
					hasDefaultDeny = true
				}
				if doc.name == "allow-infra-and-observability-egress" && strings.Contains(doc.raw, "commercelab-infra") && strings.Contains(doc.raw, "port: 5432") {
					hasInfraEgress = true
				}
			case "HorizontalPodAutoscaler":
				if doc.apiVersion != "autoscaling/v2" {
					t.Errorf("HPA %s apiVersion = %q, want autoscaling/v2", doc.name, doc.apiVersion)
				}
				for _, target := range []string{"api-gateway", "catalog-pricing-service", "order-checkout-service"} {
					if strings.Contains(doc.raw, "name: "+target) {
						hpaTargets[target] = true
					}
				}
			}
		}
	}

	if !hasNamespace {
		t.Errorf("missing hardened Namespace commercelab-retail")
	}
	if !hasResourceQuota || !hasLimitRange {
		t.Errorf("missing ResourceQuota (%v) or LimitRange (%v)", hasResourceQuota, hasLimitRange)
	}
	if !hasConfigMap {
		t.Errorf("missing ConfigMap with OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if !hasDefaultDeny {
		t.Errorf("missing default-deny-ingress NetworkPolicy")
	}
	if !hasInfraEgress {
		t.Errorf("missing allow-infra-and-observability-egress NetworkPolicy")
	}
	for _, target := range []string{"api-gateway", "catalog-pricing-service", "order-checkout-service"} {
		if !hpaTargets[target] {
			t.Errorf("missing autoscaling/v2 HPA targeting %s", target)
		}
	}
}

func TestNoSecretsCommittedInConfigMapOrDeployments(t *testing.T) {
	forbiddenPatterns := []*regexp.Regexp{
		regexp.MustCompile(`(?i)postgres://[^@\s]+:[^@\s]+@`),
		regexp.MustCompile(`(?i)mongodb://[^@\s]+:[^@\s]+@`),
		regexp.MustCompile(`(?i)password\s*:\s*["']?[^"'\s]+`),
		regexp.MustCompile(`(?i)(iam_secret|order_secret|payment_secret|pricing_secret|inventory_secret|fulfillment_secret|ledger_secret|support_secret|notify_secret|mongo_password|postgres_password)`),
		regexp.MustCompile(`REPLACE_WITH_`),
	}

	for _, file := range []string{"configmap.yaml", "deployments.yaml"} {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("read %s: %v", file, err)
		}
		text := string(raw)
		for _, re := range forbiddenPatterns {
			if match := re.FindString(text); match != "" {
				t.Errorf("%s contains forbidden secret-like literal %q", file, match)
			}
		}
	}
}
