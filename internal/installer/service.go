package installer

import "fmt"

// ServiceFile returns the filename, file content, and a human "how to enable"
// hint for the selected process manager. The binary + config paths are taken
// from the resolved Options so the generated unit points at the real install.
func ServiceFile(o Options) (name, content, hint string, err error) {
	bin := o.BinaryPath()
	conf := o.ConfigPath()
	logp := o.LogPath()

	switch o.Service {
	case ServiceSystemd:
		name = "frappe-monitor.service"
		content = fmt.Sprintf(`[Unit]
Description=frappe-monitor
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
ExecStart=%s --config %s
WorkingDirectory=%s
Restart=always
RestartSec=3
# Run as a dedicated user in production, e.g.:
#   User=frappe-monitor
#   Group=frappe-monitor

[Install]
WantedBy=multi-user.target
`, bin, conf, o.Root)
		hint = fmt.Sprintf(`  sudo cp %s/%s /etc/systemd/system/%s
  sudo systemctl daemon-reload
  sudo systemctl enable --now frappe-monitor`, o.Root, name, name)

	case ServiceLaunchd:
		name = "com.frappe-monitor.plist"
		content = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key><string>com.frappe-monitor</string>
  <key>ProgramArguments</key>
  <array>
    <string>%s</string>
    <string>--config</string>
    <string>%s</string>
  </array>
  <key>RunAtLoad</key><true/>
  <key>KeepAlive</key><true/>
  <key>WorkingDirectory</key><string>%s</string>
  <key>StandardOutPath</key><string>%s</string>
  <key>StandardErrorPath</key><string>%s</string>
  <key>EnvironmentVariables</key>
  <dict><key>PATH</key><string>/usr/local/bin:/opt/homebrew/bin:/usr/bin:/bin</string></dict>
</dict>
</plist>
`, bin, conf, o.Root, logp, logp)
		hint = fmt.Sprintf(`  cp %s/%s ~/Library/LaunchAgents/%s
  launchctl unload ~/Library/LaunchAgents/%s 2>/dev/null
  launchctl load -w ~/Library/LaunchAgents/%s`, o.Root, name, name, name, name)

	case ServiceSupervisor:
		name = "frappe-monitor.supervisor.conf"
		content = fmt.Sprintf(`[program:frappe-monitor]
command=%s --config %s
directory=%s
autostart=true
autorestart=true
startsecs=3
stopwaitsecs=10
stdout_logfile=%s
stderr_logfile=%s
; user=frappe-monitor   ; uncomment to run as a dedicated user
`, bin, conf, o.Root, logp, logp)
		hint = fmt.Sprintf(`  sudo cp %s/%s /etc/supervisor/conf.d/frappe-monitor.conf
  sudo supervisorctl reread
  sudo supervisorctl update
  sudo supervisorctl start frappe-monitor`, o.Root, name)

	case ServicePM2:
		name = "ecosystem.config.js"
		// JSON-ish JS; paths are JSON-escaped so Windows backslashes survive.
		content = fmt.Sprintf(`module.exports = {
  apps: [{
    name: "frappe-monitor",
    script: %s,
    args: ["--config", %s],
    cwd: %s,
    autorestart: true,
    max_restarts: 50,
    out_file: %s,
    error_file: %s,
  }],
}
`, jsStr(bin), jsStr(conf), jsStr(o.Root), jsStr(logp), jsStr(logp))
		hint = fmt.Sprintf(`  pm2 start %s/%s
  pm2 save
  pm2 startup    # then run the command it prints, to start on boot`, o.Root, name)

	case ServiceNone, "":
		return "", "", "", fmt.Errorf("no service manager selected")
	default:
		return "", "", "", fmt.Errorf("unknown service manager %q (want systemd|launchd|supervisor|pm2)", o.Service)
	}
	return name, content, hint, nil
}

// jsStr renders a Go string as a JSON/JS double-quoted literal (escapes
// backslashes + quotes — important for Windows paths).
func jsStr(s string) string {
	out := make([]rune, 0, len(s)+2)
	out = append(out, '"')
	for _, r := range s {
		switch r {
		case '\\':
			out = append(out, '\\', '\\')
		case '"':
			out = append(out, '\\', '"')
		default:
			out = append(out, r)
		}
	}
	out = append(out, '"')
	return string(out)
}
