package main

import "testing"

func TestReferenceProbeRejectsRemoteTargets(t *testing.T) {
	if !localReferenceTargets("https://auth-service:8080/api/v1/auth/service-token", "https://catalog-service:8081/api/v1/catalog/stock-references/batch") {
		t.Fatal("local Compose targets rejected")
	}
	for _, pair := range [][2]string{{"https://production.example.com/token", "https://catalog-service:8081/batch"}, {"https://auth-service:8080/token", "https://production.example.com/batch"}, {"http://auth-service:8080/token", "https://catalog-service:8081/batch"}} {
		if localReferenceTargets(pair[0], pair[1]) {
			t.Fatal("remote/plaintext target accepted")
		}
	}
}
