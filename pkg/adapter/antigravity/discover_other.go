//go:build !linux

package antigravity

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// discoverLS provides a fallback discovery for non-Linux systems using ps and lsof.
func discoverLS() (string, string, error) {
	out, err := exec.Command("ps", "-ef").Output()
	if err != nil {
		return "", "", fmt.Errorf("failed to run ps: %w", err)
	}

	lines := strings.Split(string(out), "\n")
	for _, line := range lines {
		if !strings.Contains(line, "language_server") || !strings.Contains(line, "--csrf_token") || !strings.Contains(line, "antigravity") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid := fields[1]

		// Extract csrf token
		csrfToken := ""
		for i, field := range fields {
			if field == "--csrf_token" && i+1 < len(fields) {
				csrfToken = fields[i+1]
				break
			}
			if strings.HasPrefix(field, "--csrf_token=") {
				csrfToken = strings.TrimPrefix(field, "--csrf_token=")
				break
			}
		}

		if csrfToken == "" {
			continue
		}

		// Use lsof to find listening ports for this pid
		lsofOut, err := exec.Command("lsof", "-nP", "-p", pid, "-iTCP", "-sTCP:LISTEN").Output()
		if err != nil {
			continue
		}

		client := &http.Client{Timeout: 100 * time.Millisecond}
		for _, lsofLine := range strings.Split(string(lsofOut), "\n") {
			fields := strings.Fields(lsofLine)
			if len(fields) < 9 {
				continue
			}
			addr := fields[8] // e.g. 127.0.0.1:44987 or *:44987
			parts := strings.Split(addr, ":")
			if len(parts) < 2 {
				continue
			}
			port, err := strconv.Atoi(parts[len(parts)-1])
			if err != nil {
				continue
			}

			url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
			resp, err := client.Get(url)
			if err != nil {
				continue
			}
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
			resp.Body.Close()

			if resp.StatusCode == http.StatusOK && bytes.Contains(body, []byte("ok")) {
				return fmt.Sprintf("localhost:%d", port), csrfToken, nil
			}
		}
	}

	return "", "", fmt.Errorf("antigravity language_server process not found or unreachable")
}
