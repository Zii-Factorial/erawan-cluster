package haproxy

import (
	"archive/zip"
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	minPort = 1
	maxPort = 65535
)

type Service struct {
	tenantsDir      string
	reloadCmd       []string
	reloadTimeout   time.Duration
	mainConfigFiles []string // optional; enables pre-validation before every reload
}

func NewService(tenantsDir string, reloadCmd []string, reloadTimeout time.Duration) (*Service, error) {
	if strings.TrimSpace(tenantsDir) == "" {
		return nil, fmt.Errorf("tenants directory is required")
	}
	if len(reloadCmd) == 0 {
		return nil, fmt.Errorf("reload command is required")
	}
	if reloadTimeout <= 0 {
		reloadTimeout = 15 * time.Second
	}
	if err := os.MkdirAll(tenantsDir, 0o775); err != nil {
		return nil, fmt.Errorf("create tenants directory: %w", err)
	}
	return &Service{tenantsDir: tenantsDir, reloadCmd: reloadCmd, reloadTimeout: reloadTimeout}, nil
}

// SetMainConfigs sets the main HAProxy config file paths used for pre-validation.
// When set, every applyConfig call validates the new tenant config against the full
// config set (main files + all existing tenant files + new file) before writing,
// so a bad config for one cluster never blocks reloads for other clusters.
func (s *Service) SetMainConfigs(paths []string) {
	filtered := make([]string, 0, len(paths))
	for _, p := range paths {
		if strings.TrimSpace(p) != "" {
			filtered = append(filtered, p)
		}
	}
	s.mainConfigFiles = filtered
}

// minimalGlobalCfg is a syntactically valid HAProxy global+defaults stub used when
// no mainConfigFiles are provided. It satisfies haproxy -c requirements so tenant
// listen blocks can be validated in isolation.
const minimalGlobalCfg = `global
    log /dev/null local0
    maxconn 50000

defaults
    mode tcp
    log global
    option dontlognull
    timeout connect 5s
    timeout client 30s
    timeout server 30s
`

// validateConfigSyntax runs haproxy -c against the new content combined with all
// existing tenant configs. Returns an error if haproxy reports a config problem,
// preventing the broken config from ever reaching disk or causing a reload failure.
func (s *Service) validateConfigSyntax(content string) error {
	stubFile, err := os.CreateTemp("", "haproxy-stub-*.cfg")
	if err != nil {
		return fmt.Errorf("create validation stub: %w", err)
	}
	defer os.Remove(stubFile.Name())
	if _, err := stubFile.WriteString(minimalGlobalCfg); err != nil {
		stubFile.Close()
		return fmt.Errorf("write validation stub: %w", err)
	}
	stubFile.Close()

	newFile, err := os.CreateTemp("", "haproxy-candidate-*.cfg")
	if err != nil {
		return fmt.Errorf("create candidate config: %w", err)
	}
	defer os.Remove(newFile.Name())
	if _, err := newFile.WriteString(content); err != nil {
		newFile.Close()
		return fmt.Errorf("write candidate config: %w", err)
	}
	newFile.Close()

	args := []string{"-c"}
	if len(s.mainConfigFiles) > 0 {
		for _, f := range s.mainConfigFiles {
			args = append(args, "-f", f)
		}
	} else {
		args = append(args, "-f", stubFile.Name())
	}

	// Include all existing tenant configs so port-conflict errors are caught upfront.
	entries, _ := os.ReadDir(s.tenantsDir)
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".cfg") && !strings.HasSuffix(e.Name(), ".bak") {
			args = append(args, "-f", filepath.Join(s.tenantsDir, e.Name()))
		}
	}
	args = append(args, "-f", newFile.Name())

	var out bytes.Buffer
	cmd := exec.Command("haproxy", args...)
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("config validation failed: %s", strings.TrimSpace(out.String()))
	}
	return nil
}

const defaultPatroniPort = 8008

// CreateMySQLConfigInput is the input for creating an HAProxy config for a MySQL Router cluster.
type CreateMySQLConfigInput struct {
	Port    int      `json:"port"`
	NodeIPs []string `json:"node_ips"`
	DBPort  int      `json:"db_port"`
}

// CreatePGSQLConfigInput is the input for creating an HAProxy config for a PostgreSQL/Patroni cluster.
type CreatePGSQLConfigInput struct {
	Port        int      `json:"port"`
	NodeIPs     []string `json:"node_ips"`
	DBPort      int      `json:"db_port"`
	PatroniPort int      `json:"patroni_port"` // Patroni REST API port; defaults to 8008 when omitted
}

type DeleteConfigInput struct {
	Port int `json:"port"`
}

func ValidatePort(port int, field string) error {
	if port < minPort || port > maxPort {
		return fmt.Errorf("%s must be between %d and %d", field, minPort, maxPort)
	}
	return nil
}

func NormalizeNodeIPs(raw []string) ([]string, error) {
	if len(raw) == 0 {
		return nil, fmt.Errorf("node_ips must contain at least one IP address or hostname")
	}

	normalized := make([]string, 0, len(raw))
	seen := make(map[string]struct{}, len(raw))
	for i, host := range raw {
		host = strings.TrimSpace(host)
		if host == "" {
			return nil, fmt.Errorf("node_ips[%d] cannot be empty", i+1)
		}
		if strings.ContainsAny(host, " \n\r\t") {
			return nil, fmt.Errorf("node_ips[%d] contains invalid whitespace", i+1)
		}
		if !isValidBackendHost(host) {
			return nil, fmt.Errorf("node_ips[%d] must be a valid IP address or hostname", i+1)
		}
		if _, ok := seen[host]; ok {
			continue
		}
		seen[host] = struct{}{}
		normalized = append(normalized, host)
	}
	if len(normalized) == 0 {
		return nil, fmt.Errorf("node_ips must contain at least one IP address or hostname")
	}
	return normalized, nil
}

func isValidBackendHost(host string) bool {
	if net.ParseIP(host) != nil {
		return true
	}

	labels := strings.Split(host, ".")
	if len(labels) == 0 {
		return false
	}
	for _, label := range labels {
		if label == "" {
			return false
		}
		if label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' {
				continue
			}
			return false
		}
	}
	return true
}

func (s *Service) CreateMySQLConfig(ctx context.Context, in CreateMySQLConfigInput) error {
	if err := ValidatePort(in.Port, "port"); err != nil {
		return err
	}
	if err := ValidatePort(in.DBPort, "db_port"); err != nil {
		return err
	}
	nodes, err := NormalizeNodeIPs(in.NodeIPs)
	if err != nil {
		return err
	}
	return s.applyConfig(ctx, in.Port, buildMySQLConfig(in.Port, nodes, in.DBPort))
}

func (s *Service) CreatePGSQLConfig(ctx context.Context, in CreatePGSQLConfigInput) error {
	if err := ValidatePort(in.Port, "port"); err != nil {
		return err
	}
	if err := ValidatePort(in.DBPort, "db_port"); err != nil {
		return err
	}
	nodes, err := NormalizeNodeIPs(in.NodeIPs)
	if err != nil {
		return err
	}
	patroniPort := in.PatroniPort
	if patroniPort == 0 {
		patroniPort = defaultPatroniPort
	}
	return s.applyConfig(ctx, in.Port, buildPGSQLConfig(in.Port, nodes, in.DBPort, patroniPort))
}

func (s *Service) applyConfig(ctx context.Context, port int, content string) error {
	if err := s.validateConfigSyntax(content); err != nil {
		return err
	}

	filename := s.filename(port)
	backup := ""

	if _, err := os.Stat(filename); err == nil {
		backup = filename + ".bak"
		if err := copyFile(filename, backup); err != nil {
			return fmt.Errorf("backup existing config: %w", err)
		}
	}

	if err := os.WriteFile(filename, []byte(content), 0o664); err != nil {
		return fmt.Errorf("write config: %w", err)
	}

	if err := s.Reload(ctx); err != nil {
		if backup != "" {
			_ = os.Rename(backup, filename)
		} else {
			_ = os.Remove(filename)
		}
		return fmt.Errorf("haproxy reload failed, rolled back: %w", err)
	}
	if err := s.verifyRuntimeAfterCreate(port); err != nil {
		if backup != "" {
			_ = os.Rename(backup, filename)
		} else {
			_ = os.Remove(filename)
		}
		_ = s.Reload(ctx)
		return fmt.Errorf("haproxy reload verification failed, rolled back: %w", err)
	}

	if backup != "" {
		_ = os.Remove(backup)
	}
	return nil
}

func (s *Service) DeleteConfig(ctx context.Context, in DeleteConfigInput) (bool, error) {
	if err := ValidatePort(in.Port, "port"); err != nil {
		return false, err
	}

	filename := s.filename(in.Port)
	if _, err := os.Stat(filename); err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat config: %w", err)
	}

	backup := filename + ".bak"
	if err := copyFile(filename, backup); err != nil {
		return false, fmt.Errorf("backup config before delete: %w", err)
	}
	if err := os.Remove(filename); err != nil {
		return false, fmt.Errorf("remove config: %w", err)
	}

	if err := s.Reload(ctx); err != nil {
		_ = os.Rename(backup, filename)
		return false, fmt.Errorf("haproxy reload failed, rolled back deletion: %w", err)
	}
	if err := s.verifyRuntimeAfterDelete(in.Port); err != nil {
		_ = os.Rename(backup, filename)
		_ = s.Reload(ctx)
		return false, fmt.Errorf("haproxy reload verification failed, rolled back deletion: %w", err)
	}
	_ = os.Remove(backup)

	return true, nil
}

func (s *Service) ZipTenantsDir() ([]byte, error) {
	entries, err := os.ReadDir(s.tenantsDir)
	if err != nil {
		return nil, fmt.Errorf("read tenants directory: %w", err)
	}

	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".cfg") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.tenantsDir, entry.Name()))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", entry.Name(), err)
		}
		f, err := zw.Create(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("zip entry %s: %w", entry.Name(), err)
		}
		if _, err := f.Write(data); err != nil {
			return nil, fmt.Errorf("write %s: %w", entry.Name(), err)
		}
	}
	if err := zw.Close(); err != nil {
		return nil, fmt.Errorf("finalize zip: %w", err)
	}
	return buf.Bytes(), nil
}

func (s *Service) ListConfigs() ([]string, error) {
	entries, err := os.ReadDir(s.tenantsDir)
	if err != nil {
		return nil, fmt.Errorf("read tenants directory: %w", err)
	}
	files := make([]string, 0)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".cfg") {
			files = append(files, e.Name())
		}
	}
	sort.Strings(files)
	return files, nil
}

func (s *Service) Reload(ctx context.Context) error {
	timeoutCtx, cancel := context.WithTimeout(ctx, s.reloadTimeout)
	defer cancel()

	cmd := exec.CommandContext(timeoutCtx, s.reloadCmd[0], s.reloadCmd[1:]...)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("reload failed: %s", msg)
	}
	return nil
}

func (s *Service) filename(port int) string {
	return filepath.Join(s.tenantsDir, fmt.Sprintf("%d.cfg", port))
}

func (s *Service) verifyRuntimeAfterCreate(port int) error {
	if err := s.ensureHaproxyLoadsTenantsDir(); err != nil {
		return err
	}
	if err := waitForPortState(port, true, 5*time.Second); err != nil {
		return err
	}
	return nil
}

func (s *Service) verifyRuntimeAfterDelete(port int) error {
	if err := s.ensureHaproxyLoadsTenantsDir(); err != nil {
		return err
	}
	if err := waitForPortState(port, false, 5*time.Second); err != nil {
		return err
	}
	return nil
}

func (s *Service) ensureHaproxyLoadsTenantsDir() error {
	cmdlines, err := haproxyCmdlines()
	if err != nil {
		return err
	}
	for _, cmdline := range cmdlines {
		if strings.Contains(cmdline, s.tenantsDir) {
			return nil
		}
	}
	return fmt.Errorf("running haproxy is not loading tenants dir: %s", s.tenantsDir)
}

func haproxyCmdlines() ([]string, error) {
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, fmt.Errorf("read /proc: %w", err)
	}

	cmdlines := make([]string, 0, 2)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if _, err := strconv.Atoi(entry.Name()); err != nil {
			continue
		}

		commPath := filepath.Join("/proc", entry.Name(), "comm")
		commRaw, err := os.ReadFile(commPath)
		if err != nil {
			continue
		}
		if strings.TrimSpace(string(commRaw)) != "haproxy" {
			continue
		}

		cmdPath := filepath.Join("/proc", entry.Name(), "cmdline")
		cmdRaw, err := os.ReadFile(cmdPath)
		if err != nil {
			continue
		}
		cmd := strings.TrimSpace(strings.ReplaceAll(string(cmdRaw), "\x00", " "))
		if cmd != "" {
			cmdlines = append(cmdlines, cmd)
		}
	}
	if len(cmdlines) == 0 {
		return nil, fmt.Errorf("no running haproxy process found")
	}
	return cmdlines, nil
}

func waitForPortState(port int, shouldListen bool, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 300*time.Millisecond)
		isListening := err == nil
		if conn != nil {
			_ = conn.Close()
		}

		if shouldListen && isListening {
			return nil
		}
		if !shouldListen && !isListening {
			return nil
		}

		if time.Now().After(deadline) {
			if shouldListen {
				return fmt.Errorf("port %d is not listening after reload", port)
			}
			return fmt.Errorf("port %d is still listening after reload", port)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func buildMySQLConfig(port int, nodeIPs []string, dbPort int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("listen node_%d\n", port))
	b.WriteString(fmt.Sprintf("    bind *:%d\n", port))
	b.WriteString("    mode tcp\n")
	b.WriteString("\n")
	b.WriteString("    # Always use first available healthy server = deterministic primary routing\n")
	b.WriteString("    balance first\n")
	b.WriteString("\n")
	b.WriteString("    # TCP keepalive — keeps idle connections alive through NAT/firewalls\n")
	b.WriteString("    option clitcpka\n")
	b.WriteString("    option srvtcpka\n")
	b.WriteString("\n")
	b.WriteString("    # TCP connect health check — MySQL Router handles its own primary detection;\n")
	b.WriteString("    # a raw mysql-check bypasses Router and marks backends DOWN incorrectly.\n")
	b.WriteString("    option tcp-check\n")
	b.WriteString("\n")
	b.WriteString("    # Proper timeouts\n")
	b.WriteString("    timeout connect  1s\n")
	b.WriteString("    timeout check    500ms\n")
	b.WriteString("    timeout queue    10s\n")
	b.WriteString("    timeout client   10m\n")
	b.WriteString("    timeout server   10m\n")
	b.WriteString("    timeout client-fin  5s\n")
	b.WriteString("    timeout server-fin  5s\n")
	b.WriteString("\n")
	b.WriteString("    # Redispatch to backup on the first retry — don't waste retries on a dead router\n")
	b.WriteString("    option redispatch 1\n")
	b.WriteString("    retries 3\n")
	b.WriteString("\n")
	b.WriteString("    # Health check tuning\n")
	b.WriteString("    # fall 3      = mark DOWN after 3 consecutive failures — avoids false DOWN on brief Router hiccup\n")
	b.WriteString("    # rise 3      = mark UP after 3 consecutive successes — avoids premature recovery after rejoin\n")
	b.WriteString("    # inter       = check every 2s when healthy — reduces check storm and flapping\n")
	b.WriteString("    # fastinter   = check every 500ms when just failed (faster failure detection)\n")
	b.WriteString("    # downinter   = check every 500ms when DOWN (consistent with fastinter)\n")
	b.WriteString("    # on-marked-down shutdown-sessions = kill connections immediately when server goes down\n")
	b.WriteString("    # slowstart 10s = ramp up traffic to rejoining server gradually — prevents connection flood\n")
	b.WriteString("    default-server inter 2s fastinter 500ms downinter 500ms fall 3 rise 3 on-marked-down shutdown-sessions slowstart 10s\n")
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("    # Backend port %d (%s)\n", dbPort, mysqlPortDesc(dbPort)))
	b.WriteString("    # Use first server as primary, others as backup\n")
	for i, ip := range nodeIPs {
		b.WriteString(fmt.Sprintf("    server db%d %s:%d check", i+1, ip, dbPort))
		if i > 0 {
			b.WriteString(" backup")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func buildPGSQLConfig(port int, nodeIPs []string, dbPort int, patroniPort int) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("listen node_%d\n", port))
	b.WriteString(fmt.Sprintf("    bind *:%d\n", port))
	b.WriteString("    mode tcp\n")
	b.WriteString("\n")
	b.WriteString("    # Always use first available healthy server = deterministic primary routing\n")
	b.WriteString("    balance first\n")
	b.WriteString("\n")
	b.WriteString("    # TCP keepalive — keeps idle connections alive through NAT/firewalls\n")
	b.WriteString("    option clitcpka\n")
	b.WriteString("    option srvtcpka\n")
	b.WriteString("\n")
	b.WriteString("    # Patroni REST API health check: only route to the leader (primary).\n")
	b.WriteString("    # GET /leader → 200 means this node is the current Patroni primary.\n")
	b.WriteString("    option httpchk GET /leader\n")
	b.WriteString("    http-check expect status 200\n")
	b.WriteString("\n")
	b.WriteString("    # Proper timeouts\n")
	b.WriteString("    timeout connect  500ms\n")
	b.WriteString("    timeout check    200ms\n")
	b.WriteString("    timeout queue    5s\n")
	b.WriteString("    timeout client   10m\n")
	b.WriteString("    timeout server   10m\n")
	b.WriteString("    timeout client-fin  2s\n")
	b.WriteString("    timeout server-fin  2s\n")
	b.WriteString("\n")
	b.WriteString("    # Redispatch to backup on the first retry — don't waste retries on a dead router\n")
	b.WriteString("    option redispatch 1\n")
	b.WriteString("    retries 2\n")
	b.WriteString("\n")
	b.WriteString("    # Health check tuning\n")
	b.WriteString("    # fall 2      = mark DOWN after 2 consecutive failures — detects dead primary in ~1s\n")
	b.WriteString("    # rise 2      = mark UP after 2 consecutive successes (avoid premature recovery)\n")
	b.WriteString("    # inter       = check every 500ms when healthy\n")
	b.WriteString("    # fastinter   = check every 100ms when just failed (detect recovery fast)\n")
	b.WriteString("    # downinter   = check every 200ms when DOWN\n")
	b.WriteString("    # on-marked-down shutdown-sessions = kill connections immediately when server goes down\n")
	b.WriteString(fmt.Sprintf("    default-server inter 500ms fastinter 100ms downinter 200ms fall 2 rise 2 on-marked-down shutdown-sessions on-marked-up shutdown-backup-sessions check port %d\n", patroniPort))
	b.WriteString("\n")
	b.WriteString(fmt.Sprintf("    # Backend port %d (PostgreSQL)\n", dbPort))
	b.WriteString("    # Use first server as primary, others as backup\n")
	for i, ip := range nodeIPs {
		b.WriteString(fmt.Sprintf("    server db%d %s:%d check", i+1, ip, dbPort))
		if i > 0 {
			b.WriteString(" backup")
		}
		b.WriteString("\n")
	}
	return b.String()
}

func mysqlPortDesc(port int) string {
	switch port {
	case 6446:
		return "MySQL Router R/W"
	case 6447:
		return "MySQL Router R/O"
	default:
		return "custom"
	}
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o664)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := out.ReadFrom(in); err != nil {
		return err
	}
	return out.Sync()
}
