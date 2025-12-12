package util

import (
	"os"
	"strconv"

	"k8s.io/client-go/rest"
)

// SetClientQPS sets the QPS and Burst for the client from environment variables or defaults.
func SetClientQPS(cfg *rest.Config) {
	qps := 50.0
	burst := 100

	if qpsStr := os.Getenv("E2E_CLIENT_QPS"); qpsStr != "" {
		if q, err := strconv.ParseFloat(qpsStr, 32); err == nil {
			qps = q
		}
	}
	if burstStr := os.Getenv("E2E_CLIENT_BURST"); burstStr != "" {
		if b, err := strconv.Atoi(burstStr); err == nil {
			burst = b
		}
	}

	cfg.QPS = float32(qps)
	cfg.Burst = burst
}
