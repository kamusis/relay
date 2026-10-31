//go:build linux

package antigravity

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// discoverLS scans /proc on Linux to detect a running Antigravity language_server process,
// extract its CSRF token from command line flags, and probe its listening TCP ports.
func discoverLS() (string, string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return "", "", fmt.Errorf("failed to read /proc: %w", err)
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pidStr := entry.Name()
		if _, err := strconv.Atoi(pidStr); err != nil {
			continue
		}

		cmdlineBytes, err := os.ReadFile(filepath.Join("/proc", pidStr, "cmdline"))
		if err != nil {
			continue
		}

		args := bytes.Split(cmdlineBytes, []byte{0})
		isLS := false
		csrfToken := ""

		for i, arg := range args {
			argStr := string(arg)
			if strings.Contains(argStr, "language_server") || strings.Contains(argStr, "antigravity") {
				isLS = true
			}
			if argStr == "--csrf_token" && i+1 < len(args) {
				csrfToken = string(args[i+1])
			} else if strings.HasPrefix(argStr, "--csrf_token=") {
				csrfToken = strings.TrimPrefix(argStr, "--csrf_token=")
			}
		}

		if !isLS || csrfToken == "" {
			continue
		}

		// Collect socket inodes owned by this process
		socketInodes := make(map[string]bool)
		fdDir := filepath.Join("/proc", pidStr, "fd")
		fdEntries, err := os.ReadDir(fdDir)
		if err == nil {
			for _, fe := range fdEntries {
				target, err := os.Readlink(filepath.Join(fdDir, fe.Name()))
				if err == nil && strings.HasPrefix(target, "socket:[") && strings.HasSuffix(target, "]") {
					inode := target[8 : len(target)-1]
					socketInodes[inode] = true
				}
			}
		}

		// Find candidate listening ports from /proc/<pid>/net/tcp and /proc/<pid>/net/tcp6
		ports := findListeningPorts(filepath.Join("/proc", pidStr, "net", "tcp"), socketInodes)
		ports = append(ports, findListeningPorts(filepath.Join("/proc", pidStr, "net", "tcp6"), socketInodes)...)

		// Also check global /proc/net/tcp as fallback if process net/tcp had no matches
		if len(ports) == 0 {
			ports = findListeningPorts("/proc/net/tcp", socketInodes)
		}

		// Probe candidate ports for /healthz endpoint
		client := &http.Client{
			Timeout: 100 * time.Millisecond,
		}

		for _, port := range ports {
			url := fmt.Sprintf("http://127.0.0.1:%d/healthz", port)
			req, err := http.NewRequest(http.MethodGet, url, nil)
			if err != nil {
				continue
			}
			resp, err := client.Do(req)
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

	return "", "", fmt.Errorf("no active antigravity language_server process found with responsive healthz port")
}

func findListeningPorts(tcpPath string, socketInodes map[string]bool) []int {
	f, err := os.Open(tcpPath)
	if err != nil {
		return nil
	}
	defer f.Close()

	var ports []int
	scanner := bufio.NewScanner(f)
	// Skip header line
	if !scanner.Scan() {
		return nil
	}

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		fields := strings.Fields(line)
		if len(fields) <= 9 {
			continue
		}

		state := fields[3]
		inode := fields[9]

		// State 0A is TCP_LISTEN
		if state != "0A" {
			continue
		}

		// If socketInodes is non-empty, only accept sockets owned by the target PID
		if len(socketInodes) > 0 && !socketInodes[inode] {
			continue
		}

		localAddr := fields[1]
		parts := strings.Split(localAddr, ":")
		if len(parts) != 2 {
			continue
		}

		port64, err := strconv.ParseInt(parts[1], 16, 64)
		if err == nil && port64 > 0 && port64 < 65536 {
			ports = append(ports, int(port64))
		}
	}

	return ports
}
