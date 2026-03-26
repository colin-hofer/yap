package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	deploycfg "yap/internal/deploy"
)

const (
	slotBlue           = "blue"
	slotGreen          = "green"
	deployTimeout      = 5 * time.Minute
	healthTimeout      = 90 * time.Second
	publicHealthWindow = 45 * time.Second
)

func main() {
	cfg := deploycfg.MustLoad()
	if err := validateDeployConfig(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "invalid config: %v\n", err)
		os.Exit(1)
	}
	if err := ensureSafeName(cfg.AppName); err != nil {
		fmt.Fprintf(os.Stderr, "invalid APP_NAME: %v\n", err)
		os.Exit(1)
	}

	logger := deploycfg.NewLogger(cfg, "deploy")

	if err := requireLocalTools("go", "ssh", "scp"); err != nil {
		logger.Error("missing required local tool", slog.String("error", err.Error()))
		os.Exit(1)
	}

	sshKey, err := normalizePath(cfg.SSHKey)
	if err != nil {
		logger.Error("invalid SSH_KEY", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if sshKey != "" {
		if err := requireFile(sshKey); err != nil {
			logger.Error("ssh key check failed", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}

	deployer := &remote{
		host:      cfg.DeployHost,
		port:      cfg.SSHPort,
		sshKey:    sshKey,
		batchMode: cfg.SSHBatchMode,
	}

	controlDir, err := os.MkdirTemp("", cfg.AppName+"-sshcm-")
	if err != nil {
		logger.Error("failed to initialize SSH connection cache", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer func() { _ = os.RemoveAll(controlDir) }()
	deployer.controlPath = filepath.Join(controlDir, "cm-%r@%h:%p")
	defer deployer.Close()

	projectRoot, err := os.Getwd()
	if err != nil {
		logger.Error("failed to determine working directory", slog.String("error", err.Error()))
		os.Exit(1)
	}
	buildRoot, err := resolveBuildRoot(projectRoot, cfg.BuildWorkdir)
	if err != nil {
		logger.Error("invalid BUILD_WORKDIR", slog.String("error", err.Error()))
		os.Exit(1)
	}

	localConfigPath := deploycfg.PathFromEnv()
	if !filepath.IsAbs(localConfigPath) {
		localConfigPath = filepath.Join(projectRoot, localConfigPath)
	}
	if err := requireFile(localConfigPath); err != nil {
		logger.Error("config file check failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	localBin, cleanupBin, err := buildLinuxBinary(buildRoot, cfg, logger)
	if err != nil {
		logger.Error("build failed", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer cleanupBin()

	unitBlue := systemdUnit(cfg.AppName, slotBlue, cfg.BlueAddr, cfg.BlueHealthAddr, cfg.RelayPublicAddr, cfg.RelayStateDir)
	unitGreen := systemdUnit(cfg.AppName, slotGreen, cfg.GreenAddr, cfg.GreenHealthAddr, cfg.RelayPublicAddr, cfg.RelayStateDir)

	localUnitBlue, cleanupUnitBlue, err := writeTempFile(cfg.AppName+"-blue-*.service", unitBlue)
	if err != nil {
		logger.Error("failed to create temporary blue unit", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer cleanupUnitBlue()

	localUnitGreen, cleanupUnitGreen, err := writeTempFile(cfg.AppName+"-green-*.service", unitGreen)
	if err != nil {
		logger.Error("failed to create temporary green unit", slog.String("error", err.Error()))
		os.Exit(1)
	}
	defer cleanupUnitGreen()

	paths := remotePaths(cfg.AppName)

	logger.Info("checking SSH connectivity", slog.String("host", cfg.DeployHost))
	if err := deployer.SSH("true"); err != nil {
		logger.Error("ssh preflight failed", slog.String("error", err.Error()))
		os.Exit(1)
	}

	activeSlot, err := deployer.ReadActiveSlot(paths.activeSlotFile)
	if err != nil {
		logger.Error("failed to determine active slot", slog.String("error", err.Error()))
		os.Exit(1)
	}
	targetSlot := oppositeSlot(activeSlot)
	targetAddr := slotAddr(targetSlot, cfg.BlueAddr, cfg.GreenAddr)
	targetHealthAddr := slotAddr(targetSlot, cfg.BlueHealthAddr, cfg.GreenHealthAddr)
	prevAddr := slotAddr(activeSlot, cfg.BlueAddr, cfg.GreenAddr)

	logger.Info("deploying new release",
		slog.String("active_slot", valueOrNone(activeSlot)),
		slog.String("target_slot", targetSlot),
		slog.String("target_addr", targetAddr),
		slog.String("public_addr", cfg.PublicAddr),
	)

	if err := deployer.SSH(installBaseScript(cfg, paths)); err != nil {
		logger.Error("failed to bootstrap remote host", slog.String("error", err.Error()))
		os.Exit(1)
	}

	remoteBinIncoming := filepath.ToSlash(filepath.Join(paths.tmpDir, "app.bin"))
	remoteCfgIncoming := filepath.ToSlash(filepath.Join(paths.tmpDir, "config.env"))
	remoteBlueIncoming := filepath.ToSlash(filepath.Join(paths.tmpDir, paths.serviceBlue))
	remoteGreenIncoming := filepath.ToSlash(filepath.Join(paths.tmpDir, paths.serviceGreen))

	if err := deployer.SCP(localBin, remoteBinIncoming); err != nil {
		logger.Error("failed to upload binary", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if err := deployer.SCP(localConfigPath, remoteCfgIncoming); err != nil {
		logger.Error("failed to upload config", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if err := deployer.SCP(localUnitBlue, remoteBlueIncoming); err != nil {
		logger.Error("failed to upload blue unit", slog.String("error", err.Error()))
		os.Exit(1)
	}
	if err := deployer.SCP(localUnitGreen, remoteGreenIncoming); err != nil {
		logger.Error("failed to upload green unit", slog.String("error", err.Error()))
		os.Exit(1)
	}

	if err := deployer.SSH(installReleaseScript(cfg.AppName, paths)); err != nil {
		logger.Error("failed to install release files", slog.String("error", err.Error()))
		os.Exit(1)
	}

	targetService := slotService(cfg.AppName, targetSlot)
	logger.Info("starting target slot", slog.String("service", targetService))
	_ = deployer.SSH("systemctl reset-failed " + shQuote(targetService) + " || true")
	if err := deployer.SSH("systemctl restart " + shQuote(targetService)); err != nil {
		if strings.Contains(err.Error(), "attempted too often") || strings.Contains(err.Error(), "start-limit-hit") {
			_ = deployer.SSH("systemctl reset-failed " + shQuote(targetService) + " || true")
			err = deployer.SSH("systemctl restart " + shQuote(targetService))
		}
		if err != nil {
			dumpSlotDiagnostics(deployer, cfg.AppName, targetService)
			logger.Error("failed to restart target slot", slog.String("error", err.Error()))
			os.Exit(1)
		}
	}

	logger.Info("waiting for local slot health", slog.String("slot", targetSlot), slog.String("health_addr", targetHealthAddr))
	if err := deployer.WaitSlotHealthy(targetHealthAddr, targetService, healthTimeout); err != nil {
		logger.Error("target slot failed local health check", slog.String("error", err.Error()))
		dumpSlotDiagnostics(deployer, cfg.AppName, targetService)
		os.Exit(1)
	}

	if err := switchHAProxy(cfg, deployer, paths, targetAddr, prevAddr, activeSlot, targetService, logger); err != nil {
		logger.Error("proxy switch failed", slog.String("error", err.Error()))
		_ = deployer.SSH("systemctl stop " + shQuote(targetService))
		os.Exit(1)
	}

	if err := deployer.WriteActiveSlot(paths.activeSlotFile, targetSlot); err != nil {
		logger.Error("failed to write active slot", slog.String("error", err.Error()))
		os.Exit(1)
	}

	if activeSlot != "" && activeSlot != targetSlot {
		prevService := slotService(cfg.AppName, activeSlot)
		logger.Info("stopping previous slot", slog.String("service", prevService))
		_ = deployer.SSH("systemctl stop " + shQuote(prevService))
	}

	logDeploymentComplete(cfg, targetSlot, logger)
}

type remote struct {
	host        string
	port        string
	sshKey      string
	batchMode   bool
	controlPath string
}

func (r *remote) SSH(remoteCommand string) error {
	args := r.sshArgs()
	args = append(args, r.host, remoteCommand)
	return runCmd(context.Background(), "ssh", args...)
}

func (r *remote) SSHCapture(remoteCommand string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), deployTimeout)
	defer cancel()

	args := r.sshArgs()
	args = append(args, r.host, remoteCommand)
	cmd := exec.CommandContext(ctx, "ssh", args...)
	cmd.Stdin = os.Stdin
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("ssh failed: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func (r *remote) SCP(localPath, remotePath string) error {
	args := []string{"-P", r.port, "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=" + r.batchFlag()}
	if r.controlPath != "" {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPersist=10m",
			"-o", "ControlPath="+r.controlPath,
		)
	}
	if r.sshKey != "" {
		args = append(args, "-i", r.sshKey, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, localPath, fmt.Sprintf("%s:%s", r.host, remotePath))
	return runCmd(context.Background(), "scp", args...)
}

func (r *remote) ReadActiveSlot(path string) (string, error) {
	cmd := fmt.Sprintf("if [ -f %s ]; then cat %s; fi", shQuote(path), shQuote(path))
	out, err := r.SSHCapture(cmd)
	if err != nil {
		return "", err
	}
	return sanitizeSlot(out), nil
}

func (r *remote) WriteActiveSlot(path, slot string) error {
	cmd := fmt.Sprintf("install -d -m 755 %s && printf %%s %s > %s", shQuote(filepath.Dir(path)), shQuote(slot), shQuote(path))
	return r.SSH(cmd)
}

func (r *remote) WaitSlotHealthy(addr, service string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error

	for time.Now().Before(deadline) {
		state, err := r.ReadServiceState(service)
		if err != nil {
			return err
		}

		if state.ActiveState == "failed" || state.Result == "start-limit-hit" {
			return fmt.Errorf("service not healthy: active_state=%s sub_state=%s result=%s restarts=%s", state.ActiveState, state.SubState, state.Result, state.NRestarts)
		}

		healthURL := "http://" + addr + "/healthz"
		cmd := fmt.Sprintf("curl -fsS --max-time 3 %s >/dev/null 2>&1", shQuote(healthURL))
		if err := r.SSH(cmd); err == nil {
			return nil
		} else {
			lastErr = err
		}

		time.Sleep(2 * time.Second)
	}

	state, err := r.ReadServiceState(service)
	if err == nil {
		return fmt.Errorf("slot health timed out after %s (active_state=%s sub_state=%s result=%s restarts=%s): %v", timeout, state.ActiveState, state.SubState, state.Result, state.NRestarts, lastErr)
	}

	return fmt.Errorf("slot health timed out after %s: %v", timeout, lastErr)
}

func (r *remote) WaitTCPHealthy(bindAddr string, timeout time.Duration) error {
	cmd, err := tcpProbeCommand(bindAddr)
	if err != nil {
		return err
	}

	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		if err := r.SSH(cmd); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(2 * time.Second)
	}
	if lastErr == nil {
		lastErr = errors.New("tcp health check timed out")
	}
	return lastErr
}

type serviceState struct {
	ActiveState string
	SubState    string
	Result      string
	NRestarts   string
}

func (r *remote) ReadServiceState(service string) (serviceState, error) {
	cmd := fmt.Sprintf("systemctl show %s -p ActiveState -p SubState -p Result -p NRestarts --value", shQuote(service))
	out, err := r.SSHCapture(cmd)
	if err != nil {
		return serviceState{}, err
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	for len(lines) < 4 {
		lines = append(lines, "")
	}

	return serviceState{
		ActiveState: strings.TrimSpace(lines[0]),
		SubState:    strings.TrimSpace(lines[1]),
		Result:      strings.TrimSpace(lines[2]),
		NRestarts:   strings.TrimSpace(lines[3]),
	}, nil
}

func (r *remote) sshArgs() []string {
	args := []string{"-p", r.port, "-o", "StrictHostKeyChecking=accept-new", "-o", "BatchMode=" + r.batchFlag()}
	if r.controlPath != "" {
		args = append(args,
			"-o", "ControlMaster=auto",
			"-o", "ControlPersist=10m",
			"-o", "ControlPath="+r.controlPath,
		)
	}
	if r.sshKey != "" {
		args = append(args, "-i", r.sshKey, "-o", "IdentitiesOnly=yes")
	}
	return args
}

func (r *remote) Close() {
	if r.controlPath == "" {
		return
	}
	args := []string{"-p", r.port, "-S", r.controlPath, "-O", "exit"}
	if r.sshKey != "" {
		args = append(args, "-i", r.sshKey, "-o", "IdentitiesOnly=yes")
	}
	args = append(args, r.host)
	cmd := exec.Command("ssh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	_ = cmd.Run()
}

func (r *remote) batchFlag() string {
	if r.batchMode {
		return "yes"
	}
	return "no"
}

type paths struct {
	tmpDir         string
	bin            string
	configDir      string
	configPath     string
	stateDir       string
	activeSlotFile string
	haproxyFile    string
	serviceBlue    string
	serviceGreen   string
	legacyService  string
}

func remotePaths(appName string) paths {
	return paths{
		tmpDir:         filepath.ToSlash(filepath.Join("/tmp", appName+"-deploy")),
		bin:            filepath.ToSlash(filepath.Join("/usr/local/bin", appName)),
		configDir:      filepath.ToSlash(filepath.Join("/etc", appName)),
		configPath:     filepath.ToSlash(filepath.Join("/etc", appName, "config.env")),
		stateDir:       filepath.ToSlash(filepath.Join("/var/lib", appName)),
		activeSlotFile: filepath.ToSlash(filepath.Join("/var/lib", appName, "active_slot")),
		haproxyFile:    "/etc/haproxy/haproxy.cfg",
		serviceBlue:    slotService(appName, slotBlue),
		serviceGreen:   slotService(appName, slotGreen),
		legacyService:  appName + ".service",
	}
}

func installBaseScript(cfg *deploycfg.Config, p paths) string {
	user := cfg.AppName
	group := cfg.AppName

	return "bash -se <<'EOF'\n" + fmt.Sprintf(`set -eu

if ! id -u %s >/dev/null 2>&1; then
	useradd --system --create-home --home-dir /var/lib/%s --shell /usr/sbin/nologin %s
fi

if ! getent group %s >/dev/null 2>&1; then
	groupadd --system %s
	usermod -a -G %s %s
fi

if ! command -v curl >/dev/null 2>&1; then
	apt-get update
	apt-get install -y curl
fi

if ! command -v haproxy >/dev/null 2>&1; then
	apt-get update
	apt-get install -y haproxy
fi

systemctl enable haproxy
systemctl start haproxy

if systemctl list-unit-files %s >/dev/null 2>&1; then
	systemctl disable --now %s || true
fi

install -d -m 755 %s
install -d -m 750 %s
install -d -m 755 %s
chown %s:%s %s %s
`,
		shQuote(user), shQuote(cfg.AppName), shQuote(user),
		shQuote(group), shQuote(group), shQuote(group), shQuote(user),
		shQuote(p.legacyService), shQuote(p.legacyService),
		shQuote(p.tmpDir),
		shQuote(p.configDir),
		shQuote(p.stateDir),
		shQuote(user), shQuote(group), shQuote(p.configDir), shQuote(p.stateDir),
	) + "\nEOF"
}

func installReleaseScript(appName string, p paths) string {
	user := appName
	group := appName

	incomingBin := filepath.ToSlash(filepath.Join(p.tmpDir, "app.bin"))
	incomingCfg := filepath.ToSlash(filepath.Join(p.tmpDir, "config.env"))
	incomingBlue := filepath.ToSlash(filepath.Join(p.tmpDir, p.serviceBlue))
	incomingGreen := filepath.ToSlash(filepath.Join(p.tmpDir, p.serviceGreen))

	return "bash -se <<'EOF'\n" + fmt.Sprintf(`set -eu

install -m 755 %s %s
install -m 640 %s %s
chown %s:%s %s

install -m 644 %s /etc/systemd/system/%s
install -m 644 %s /etc/systemd/system/%s

rm -f %s %s %s %s

systemctl daemon-reload
systemctl enable %s
systemctl enable %s
`,
		shQuote(incomingBin), shQuote(p.bin),
		shQuote(incomingCfg), shQuote(p.configPath),
		shQuote(user), shQuote(group), shQuote(p.configPath),
		shQuote(incomingBlue), shQuote(p.serviceBlue),
		shQuote(incomingGreen), shQuote(p.serviceGreen),
		shQuote(incomingBin), shQuote(incomingCfg), shQuote(incomingBlue), shQuote(incomingGreen),
		shQuote(p.serviceBlue),
		shQuote(p.serviceGreen),
	) + "\nEOF"
}

func switchHAProxyScript(p paths, incomingPath string) string {
	return "bash -se <<'EOF'\n" + fmt.Sprintf(`set -eu

haproxy -c -f %s

if [ -f %s ]; then
	cp %s %s.prev
fi

install -m 644 %s %s
systemctl reload haproxy || systemctl restart haproxy
rm -f %s
`,
		shQuote(incomingPath),
		shQuote(p.haproxyFile),
		shQuote(p.haproxyFile),
		shQuote(p.haproxyFile),
		shQuote(incomingPath),
		shQuote(p.haproxyFile),
		shQuote(incomingPath),
	) + "\nEOF"
}

func haproxyConfig(publicAddr, upstream string) string {
	var b strings.Builder
	b.WriteString("global\n")
	b.WriteString("  log /dev/log local0\n")
	b.WriteString("  log /dev/log local1 notice\n")
	b.WriteString("  daemon\n")
	b.WriteString("  maxconn 4096\n\n")
	b.WriteString("defaults\n")
	b.WriteString("  mode tcp\n")
	b.WriteString("  log global\n")
	b.WriteString("  option tcplog\n")
	b.WriteString("  timeout connect 5s\n")
	b.WriteString("  timeout client 2m\n")
	b.WriteString("  timeout server 2m\n\n")
	b.WriteString("frontend relay_in\n")
	b.WriteString("  bind ")
	b.WriteString(publicAddr)
	b.WriteString("\n")
	b.WriteString("  default_backend relay_active\n\n")
	b.WriteString("backend relay_active\n")
	b.WriteString("  option tcp-check\n")
	b.WriteString("  server active ")
	b.WriteString(upstream)
	b.WriteString(" check\n")
	return b.String()
}

func systemdUnit(appName, slot, addr, healthAddr, relayPublicAddr, relayStateDir string) string {
	service := slotService(appName, slot)
	return fmt.Sprintf(`[Unit]
Description=%s backend (%s slot)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=/usr/local/bin/%s
EnvironmentFile=-/etc/%s/config.env
Environment=CONFIG_FILE=/etc/%s/config.env
Environment=SERVER_ADDR=%s
Environment=HEALTH_ADDR=%s
Environment=YAP_RELAY_PUBLIC_ADDR=%s
Environment=YAP_RELAY_STATE_DIR=%s
Environment=APP_SLOT=%s
Restart=on-failure
RestartSec=2

User=%s
Group=%s
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true
ReadOnlyPaths=/etc/%s
ReadWritePaths=/var/lib/%s

WorkingDirectory=/var/lib/%s

[Install]
WantedBy=multi-user.target
`, service, slot, appName, appName, appName, addr, healthAddr, relayPublicAddr, relayStateDir, slot, appName, appName, appName, appName, appName)
}

func switchHAProxy(cfg *deploycfg.Config, deployer *remote, p paths, targetAddr, prevAddr, activeSlot, targetService string, logger *slog.Logger) error {
	nextConfig := haproxyConfig(cfg.PublicAddr, targetAddr)
	localConfig, cleanupConfig, err := writeTempFile(cfg.AppName+"-haproxy-*", nextConfig)
	if err != nil {
		return err
	}
	defer cleanupConfig()

	remoteIncoming := filepath.ToSlash(filepath.Join(p.tmpDir, "haproxy.cfg.next"))
	if err := deployer.SCP(localConfig, remoteIncoming); err != nil {
		return err
	}

	logger.Info("switching haproxy upstream", slog.String("to", targetAddr), slog.String("public_addr", cfg.PublicAddr))
	if err := deployer.SSH(switchHAProxyScript(p, remoteIncoming)); err != nil {
		return err
	}

	if err := deployer.WaitTCPHealthy(cfg.PublicAddr, publicHealthWindow); err != nil {
		if rollbackErr := rollbackHAProxy(cfg, deployer, p, prevAddr, activeSlot); rollbackErr != nil {
			logger.Warn("tcp rollback failed", slog.String("error", rollbackErr.Error()))
		}
		return fmt.Errorf("public tcp check failed after switch: %w", err)
	}
	return nil
}

func rollbackHAProxy(cfg *deploycfg.Config, deployer *remote, p paths, prevAddr, activeSlot string) error {
	if activeSlot == "" {
		return nil
	}
	rollbackConfig := haproxyConfig(cfg.PublicAddr, prevAddr)
	rollbackPath, cleanupRollback, err := writeTempFile(cfg.AppName+"-haproxy-rollback-*", rollbackConfig)
	if err != nil {
		return err
	}
	defer cleanupRollback()
	remoteRollback := filepath.ToSlash(filepath.Join(p.tmpDir, "haproxy.cfg.rollback"))
	if err := deployer.SCP(rollbackPath, remoteRollback); err != nil {
		return err
	}
	if err := deployer.SSH(switchHAProxyScript(p, remoteRollback)); err != nil {
		return err
	}
	_ = deployer.SSH("systemctl restart " + shQuote(slotService(cfg.AppName, activeSlot)))
	_ = deployer.WriteActiveSlot(p.activeSlotFile, activeSlot)
	return nil
}

func validateDeployConfig(cfg *deploycfg.Config) error {
	return cfg.EnsureRequired("APP_NAME", "DEPLOY_HOST", "YAP_RELAY_PUBLIC_ADDR")
}

func resolveBuildRoot(projectRoot, configured string) (string, error) {
	configured = strings.TrimSpace(configured)
	if configured == "" {
		return projectRoot, nil
	}
	if !filepath.IsAbs(configured) {
		configured = filepath.Join(projectRoot, configured)
	}
	return filepath.Abs(configured)
}

func logDeploymentComplete(cfg *deploycfg.Config, activeSlot string, logger *slog.Logger) {
	logger.Info("deployment finished",
		slog.String("public_addr", cfg.PublicAddr),
		slog.String("relay_public_addr", cfg.RelayPublicAddr),
		slog.String("active_slot", activeSlot),
	)
}

func writeTempFile(pattern, contents string) (string, func(), error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", nil, err
	}
	if _, err := io.WriteString(f, contents); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(f.Name())
		return "", nil, err
	}
	cleanup := func() { _ = os.Remove(f.Name()) }
	return f.Name(), cleanup, nil
}

func tcpProbeCommand(bindAddr string) (string, error) {
	host, port, err := net.SplitHostPort(bindAddr)
	if err != nil {
		if strings.HasPrefix(bindAddr, ":") {
			host = ""
			port = strings.TrimPrefix(bindAddr, ":")
		} else {
			return "", fmt.Errorf("parse PUBLIC_ADDR: %w", err)
		}
	}
	host = strings.Trim(strings.TrimSpace(host), "[]")
	port = strings.TrimSpace(port)
	if port == "" {
		return "", fmt.Errorf("PUBLIC_ADDR must include a port")
	}

	probeHost := host
	switch probeHost {
	case "", "0.0.0.0", "::":
		probeHost = "127.0.0.1"
	}
	if strings.Contains(probeHost, ":") {
		probeHost = "127.0.0.1"
	}

	inner := fmt.Sprintf("exec 3<>/dev/tcp/%s/%s; exec 3<&-; exec 3>&-", probeHost, port)
	return "timeout 3 bash -lc " + shQuote(inner) + " >/dev/null 2>&1", nil
}

func buildLinuxBinary(projectRoot string, cfg *deploycfg.Config, logger *slog.Logger) (string, func(), error) {
	tmpDir, err := os.MkdirTemp("", cfg.AppName+"-build-")
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = os.RemoveAll(tmpDir) }

	localBin := filepath.Join(tmpDir, cfg.AppName+"-linux")
	logger.Info("building linux binary", slog.String("path", localBin), slog.String("package", cfg.BuildPackage))

	ctx, cancel := context.WithTimeout(context.Background(), deployTimeout)
	defer cancel()

	build := exec.CommandContext(ctx, "go", "build", "-o", localBin, cfg.BuildPackage)
	build.Dir = projectRoot
	build.Env = append(os.Environ(),
		"GOOS=linux",
		"GOARCH=amd64",
		"GOCACHE="+filepath.Join(projectRoot, ".gocache"),
	)
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr

	if err := build.Run(); err != nil {
		cleanup()
		return "", nil, err
	}

	return localBin, cleanup, nil
}

func runCmd(parent context.Context, name string, args ...string) error {
	ctx, cancel := context.WithTimeout(parent, deployTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func requireLocalTools(names ...string) error {
	for _, name := range names {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("required tool %q was not found in PATH", name)
		}
	}
	return nil
}

func normalizePath(path string) (string, error) {
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~/"))
	}
	return filepath.Abs(path)
}

func requireFile(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("file does not exist: %s", path)
		}
		return err
	}
	if st.IsDir() {
		return fmt.Errorf("expected file but found directory: %s", path)
	}
	return nil
}

func sanitizeSlot(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	if v == slotBlue || v == slotGreen {
		return v
	}
	return ""
}

func oppositeSlot(active string) string {
	if active == slotBlue {
		return slotGreen
	}
	return slotBlue
}

func slotAddr(slot, blueAddr, greenAddr string) string {
	if slot == slotGreen {
		return greenAddr
	}
	return blueAddr
}

func slotService(appName, slot string) string {
	return appName + "-" + slot + ".service"
}

func valueOrNone(v string) string {
	if strings.TrimSpace(v) == "" {
		return "none"
	}
	return v
}

func shQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func ensureSafeName(name string) error {
	if name == "" {
		return errors.New("must not be empty")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]*$`).MatchString(name) {
		return errors.New("must match ^[a-zA-Z0-9][a-zA-Z0-9._-]*$")
	}
	return nil
}

func dumpSlotDiagnostics(deployer *remote, appName, serviceName string) {
	_, _ = fmt.Fprintf(os.Stderr, "\n--- diagnostics for %s ---\n", serviceName)
	output, err := deployer.SSHCapture("systemctl status --no-pager " + shQuote(serviceName))
	printDiagnostic(os.Stderr, "systemctl status", output, err)

	output, err = deployer.SSHCapture("journalctl -u " + shQuote(serviceName) + " -n 80 --no-pager")
	printDiagnostic(os.Stderr, "journalctl", output, err)

	output, err = deployer.SSHCapture("ls -la " + shQuote(filepath.ToSlash(filepath.Join("/var/lib", appName))))
	printDiagnostic(os.Stderr, "state dir", output, err)
}

func printDiagnostic(w io.Writer, label, output string, err error) {
	if err != nil {
		_, _ = fmt.Fprintf(w, "[%s] %v\n", label, err)
		return
	}
	if strings.TrimSpace(output) == "" {
		return
	}
	_, _ = fmt.Fprintf(w, "[%s]\n%s\n", label, output)
}
