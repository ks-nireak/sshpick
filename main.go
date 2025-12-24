package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

type sshHost struct {
	Alias    string
	Hostname string
	IP       string // resolved from Hostname if it's not already an IP
	User     string
	Port     string
}
type model struct {
	hosts        []sshHost
	cursor       int
	ready        bool
	width        int
	height       int
	err          error
	chosen       bool
	selectedHost sshHost
	title        string
	styles       styles
}

type styles struct {
	title    lipgloss.Style
	item     lipgloss.Style
	selected lipgloss.Style
	help     lipgloss.Style
	error    lipgloss.Style
}

func defaultStyles() styles {
	return styles{
		title:    lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213")),
		item:     lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		selected: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("229")).Background(lipgloss.Color("57")).Padding(0, 1),
		help:     lipgloss.NewStyle().Foreground(lipgloss.Color("244")),
		error:    lipgloss.NewStyle().Foreground(lipgloss.Color("9")),
	}
}

func parseSSHConfig(path string) ([]sshHost, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var (
		hosts   []sshHost
		aliases []string              // aliases for the current Host block
		fields  = map[string]string{} // collected key/values for the block
	)

	// helper to read a field or ""
	get := func(k string) string {
		if v, ok := fields[k]; ok {
			return v
		}
		return ""
	}

	// commit the current block (expand to one object per alias)
	commit := func() {
		if len(aliases) == 0 {
			return
		}
		hostname := get("hostname")
		user := get("user")
		port := get("port")

		for _, a := range aliases {
			// skip wildcard/negation aliases
			if strings.ContainsAny(a, "*?!") {
				continue
			}
			h := sshHost{
				Alias:    a,
				Hostname: hostname,
				User:     user,
				Port:     port,
			}
			// Fill IP if Hostname is an IP; otherwise try a DNS lookup (best-effort)
			if h.Hostname != "" {
				if ip := net.ParseIP(h.Hostname); ip != nil {
					h.IP = ip.String()
				} else if ips, err := net.LookupIP(h.Hostname); err == nil && len(ips) > 0 {
					h.IP = ips[0].String()
				}
			}
			hosts = append(hosts, h)
		}
		// reset for next block
		aliases = nil
		fields = map[string]string{}
	}

	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()

		// strip comments
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		key := strings.ToLower(parts[0])
		// value is the original text after the key (preserves spaces inside)
		value := strings.TrimSpace(line[len(parts[0]):])

		switch key {
		case "host":
			// new block -> commit the previous one
			commit()
			// capture all aliases on this line
			aliases = parts[1:]
		case "hostname", "user", "port":
			fields[key] = value
		default:
			// ignore other directives for now (IdentityFile, ProxyJump, etc.)
		}
	}
	// commit the last block
	commit()

	if err := sc.Err(); err != nil {
		return nil, err
	}
	return hosts, nil
}
func initialModel(hosts []sshHost) model {
	return model{
		hosts:  hosts,
		title:  "Pick an SSH host",
		styles: defaultStyles(),
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "q", "esc":
			return m, tea.Quit

		// down
		case "j", "l", "down":
			if len(m.hosts) > 0 {
				m.cursor = (m.cursor + 1) % len(m.hosts)
			}
		// up
		case "k", "h", "up":
			if len(m.hosts) > 0 {
				m.cursor = (m.cursor - 1 + len(m.hosts)) % len(m.hosts)
			}
		case "enter":
			if len(m.hosts) == 0 {
				m.err = errors.New("no hosts to select")
				return m, nil
			}
			m.chosen = true
			m.selectedHost = m.hosts[m.cursor]
			return m, tea.Quit
		}

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
	}
	return m, nil
}

func (m model) View() string {
	if !m.ready {
		return "loading...\n"
	}
	var b strings.Builder

	fmt.Fprintln(&b, m.styles.title.Render(m.title))
	fmt.Fprintln(&b, m.styles.help.Render("Use h/j/k/l or arrows • Enter to connect • q to quit"))
	fmt.Fprintln(&b, "")

	if len(m.hosts) == 0 {
		fmt.Fprintln(&b, m.styles.error.Render("No hosts found in ~/.ssh/config"))
		return b.String()
	}

	for i, h := range m.hosts {
		portText := ""
		if h.Port != "" {
			portText = ":" + h.Port
		}
		ipText := ""
		if h.IP != "" {
			ipText = "  IP: " + h.IP
		}
		line := fmt.Sprintf(
			"%-15s  Hostname: %-20s%s  User: %-8s%s",
			h.Alias, h.Hostname, ipText, h.User, portText,
		)

		if i == m.cursor {
			fmt.Fprintln(&b, m.styles.selected.Render("> "+line))
		} else {
			fmt.Fprintln(&b, m.styles.item.Render("  "+line))
		}
	}

	if m.err != nil {
		fmt.Fprintln(&b, "")
		fmt.Fprintln(&b, m.styles.error.Render(m.err.Error()))
	}
	return b.String()
}

func runSSH(host string) error {
	// Replace current process with ssh for clean TTY behavior
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}
	args := []string{"ssh", host}
	return syscall.Exec(bin, args, os.Environ())
}

func main() {
	var cfgPath string
	flag.StringVar(&cfgPath, "config", "", "Path to ssh config (deault: ~/.ssh/config)")
	flag.Parse()

	if cfgPath == "" {
		cfgPath = filepath.Join(os.Getenv("HOME"), ".ssh", "config")
	}

	hosts, err := parseSSHConfig(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "error reading config:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(initialModel(hosts), tea.WithAltScreen())
	m, err := p.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, "tui error:", err)
		os.Exit(1)
	}

	final := m.(model)
	if !final.chosen || final.selectedHost.Alias == "" {
		return
	}

	// Prefer a clean handoff to ssh (replaces current process).
	if err := runSSH(final.selectedHost.Alias); err != nil {
		// Fallback: spawn ssh as a subprocess.
		cmd := exec.Command("ssh", final.selectedHost.Alias)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if e := cmd.Run(); e != nil {
			fmt.Fprintln(os.Stderr, "ssh error:", e)
			os.Exit(1)
		}
	}
}
