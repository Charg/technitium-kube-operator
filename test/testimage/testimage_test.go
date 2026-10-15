package testimage

import (
	"os"
	"path/filepath"
	"testing"

	"sigs.k8s.io/yaml"
)

func TestMatchesCRDDefault(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "config", "crd", "bases", "dns.packet.fail_technitiumclusters.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var crd struct {
		Spec struct {
			Versions []struct {
				Schema struct {
					OpenAPIV3Schema struct {
						Properties struct {
							Spec struct {
								Properties struct {
									Image struct {
										Default string `json:"default"`
									} `json:"image"`
								} `json:"properties"`
							} `json:"spec"`
						} `json:"properties"`
					} `json:"openAPIV3Schema"`
				} `json:"schema"`
			} `json:"versions"`
		} `json:"spec"`
	}
	if err := yaml.Unmarshal(raw, &crd); err != nil {
		t.Fatal(err)
	}
	if len(crd.Spec.Versions) == 0 {
		t.Fatal("CRD has no versions")
	}
	got := crd.Spec.Versions[0].Schema.OpenAPIV3Schema.Properties.Spec.Properties.Image.Default
	if got != TechnitiumImage {
		t.Fatalf("CRD default image %q != TechnitiumImage %q", got, TechnitiumImage)
	}
}
