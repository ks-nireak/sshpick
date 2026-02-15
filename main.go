package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type sshHost struct {
	Alias         string
	Hostname      string
	IP            string // resolved from Hostname if it's not already an IP
	User          string
	Port          string
	LocalForwards []string
	Notes         []string
	SourcePath    string
	SourceLine    int // 1-based line number of the Host directive
}
type model struct {
	allHosts       []sshHost
	hosts          []sshHost
	cursor         int
	ready          bool
	width          int
	height         int
	showNotes      bool
	err            error
	chosen         bool
	selectedHost   sshHost
	title          string
	styles         styles
	localForward   string
	configPath     string
	filterActive   bool
	filterQuery    string
	lastValidRegex string
	filterErr      error
}

type styles struct {
	title    lipgloss.Style
	item     lipgloss.Style
	selected lipgloss.Style
	help     lipgloss.Style
	error    lipgloss.Style
}

type editorFinishedMsg struct{ err error }

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
		hosts         []sshHost
		aliases       []string              // aliases for the current Host block
		fields        = map[string]string{} // collected key/values for the block
		localForwards []string
		notes         []string
		hostLine      int
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
				Alias:         a,
				Hostname:      hostname,
				User:          user,
				Port:          port,
				LocalForwards: append([]string{}, localForwards...),
				Notes:         append([]string{}, notes...),
				SourcePath:    path,
				SourceLine:    hostLine,
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
		localForwards = nil
		notes = nil
		hostLine = 0
	}

	sc := bufio.NewScanner(f)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			if note := strings.TrimSpace(line[1:]); note != "" {
				notes = append(notes, note)
			}
			continue
		}
		comment := ""
		if idx := strings.Index(line, "#"); idx >= 0 {
			comment = strings.TrimSpace(line[idx+1:])
			line = strings.TrimSpace(line[:idx])
			if line == "" {
				if comment != "" {
					notes = append(notes, comment)
				}
				continue
			}
		}
		if comment != "" {
			notes = append(notes, comment)
		}
		parts := strings.Fields(line)
		if len(parts) < 2 {
			continue
		}

		key := strings.ToLower(parts[0])
		// value is the text after the key (preserves spaces inside)
		value := strings.TrimSpace(line[len(parts[0]):])

		switch key {
		case "host":
			// new block -> commit the previous one
			commit()
			// capture all aliases on this line
			aliases = parts[1:]
			hostLine = lineNo
		case "hostname", "user", "port":
			fields[key] = value
		case "localforward":
			if len(parts) >= 2 {
				if port := extractLocalForwardPort(strings.TrimSpace(parts[1])); port != "" {
					localForwards = append(localForwards, port)
				}
			}
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
func extractLocalForwardPort(arg string) string {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ""
	}
	if idx := strings.Index(arg, "]:"); idx >= 0 && idx+2 < len(arg) {
		return strings.TrimSpace(arg[idx+2:])
	}
	if idx := strings.LastIndex(arg, ":"); idx >= 0 && idx+1 < len(arg) {
		return strings.TrimSpace(arg[idx+1:])
	}
	return arg
}
func initialModel(hosts []sshHost, localForward string, configPath string) model {
	return model{
		allHosts:     hosts,
		hosts:        hosts,
		title:        "Pick an SSH host",
		styles:       defaultStyles(),
		localForward: localForward,
		configPath:   configPath,
	}
}

func (m model) Init() tea.Cmd { return nil }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {

	case editorFinishedMsg:
		if msg.err != nil {
			m.err = msg.err
		}
		return m, nil

	case tea.KeyMsg:
		if m.filterActive {
			switch msg.String() {
			case "esc":
				m.filterActive = false
				m.filterErr = nil
				m.filterQuery = m.lastValidRegex
				m.applyFilter(m.lastValidRegex)
				return m, nil
			case "enter":
				pattern := m.filterQuery
				m.applyFilter(pattern)
				if m.filterErr != nil {
					return m, nil
				}
				m.lastValidRegex = pattern
				m.filterActive = false
				return m, nil
			case "ctrl+c", "q":
				return m, tea.Quit
			case "backspace":
				if m.filterQuery != "" {
					_, n := utf8.DecodeLastRuneInString(m.filterQuery)
					if n > 0 {
						m.filterQuery = m.filterQuery[:len(m.filterQuery)-n]
					} else {
						m.filterQuery = ""
					}
					m.applyFilter(m.filterQuery)
				}
				return m, nil
			default:
				if msg.Type == tea.KeyRunes {
					// Avoid unbounded growth.
					if len(m.filterQuery) < 256 {
						m.filterQuery += string(msg.Runes)
						m.applyFilter(m.filterQuery)
					}
					return m, nil
				}
				return m, nil
			}
		}

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
		case "n":
			m.showNotes = !m.showNotes
		case "/":
			m.filterActive = true
			m.filterQuery = m.lastValidRegex
			return m, nil
		case "e":
			if len(m.hosts) == 0 || m.configPath == "" {
				m.err = errors.New("no config file to edit")
				return m, nil
			}
			line := m.hosts[m.cursor].SourceLine
			if line <= 0 {
				line = 1
			}
			cmd, err := editorCommand(m.configPath, line)
			if err != nil {
				m.err = err
				return m, nil
			}
			return m, tea.ExecProcess(cmd, func(err error) tea.Msg { return editorFinishedMsg{err: err} })
		case "backspace", "delete":
			if m.lastValidRegex != "" {
				m.lastValidRegex = ""
				m.filterQuery = ""
				m.applyFilter("")
				return m, nil
			}
		}

	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
	}
	return m, nil
}

func editorCommand(path string, line int) (*exec.Cmd, error) {
	editor := strings.TrimSpace(os.Getenv("VISUAL"))
	if editor == "" {
		editor = strings.TrimSpace(os.Getenv("EDITOR"))
	}
	if editor == "" {
		editor = "vi"
	}

	parts := strings.Fields(editor)
	if len(parts) == 0 {
		return nil, errors.New("empty editor command")
	}
	bin := parts[0]
	baseArgs := parts[1:]

	switch filepath.Base(bin) {
	case "code", "code-insiders", "cursor":
		args := append(append([]string{}, baseArgs...), "--goto", fmt.Sprintf("%s:%d:1", path, line))
		return exec.Command(bin, args...), nil
	case "vim", "nvim", "vi":
		args := append(append([]string{}, baseArgs...), fmt.Sprintf("+%d", line), path)
		return exec.Command(bin, args...), nil
	case "nano":
		args := append(append([]string{}, baseArgs...), fmt.Sprintf("+%d,1", line), path)
		return exec.Command(bin, args...), nil
	case "subl", "sublime_text":
		args := append(append([]string{}, baseArgs...), fmt.Sprintf("%s:%d", path, line))
		return exec.Command(bin, args...), nil
	default:
		// Best effort: pass the file as the last arg.
		args := append(append([]string{}, baseArgs...), path)
		return exec.Command(bin, args...), nil
	}
}

func filterHostsRegex(all []sshHost, pattern string) ([]sshHost, error) {
	pattern = strings.TrimSpace(pattern)
	if pattern == "" {
		return all, nil
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return nil, err
	}
	out := make([]sshHost, 0, len(all))
	for _, h := range all {
		matched := re.MatchString(h.Alias) ||
			re.MatchString(h.Hostname) ||
			re.MatchString(h.IP) ||
			re.MatchString(h.User) ||
			re.MatchString(h.Port)
		if !matched {
			for _, lf := range h.LocalForwards {
				if re.MatchString(lf) {
					matched = true
					break
				}
			}
		}
		if !matched {
			for _, note := range h.Notes {
				if re.MatchString(note) {
					matched = true
					break
				}
			}
		}
		if matched {
			out = append(out, h)
		}
	}
	return out, nil
}

func (m *model) applyFilter(pattern string) {
	filtered, err := filterHostsRegex(m.allHosts, pattern)
	if err != nil {
		m.filterErr = err
		return
	}
	m.filterErr = nil
	m.hosts = filtered
	if len(m.hosts) == 0 {
		m.cursor = 0
		return
	}
	if m.cursor >= len(m.hosts) {
		m.cursor = len(m.hosts) - 1
	}
}

func (m model) View() string {
	if !m.ready {
		return "loading...\n"
	}
	var b strings.Builder

	fmt.Fprintln(&b, m.styles.title.Render(m.title))
	fmt.Fprintln(&b, m.styles.help.Render("Use h/j/k/l or arrows • / filter (regex) • e edit in $EDITOR • n notes • Enter connect • q quit"))
	if m.localForward != "" {
		fmt.Fprintln(&b, m.styles.help.Render("Forwarding: "+m.localForward))
	}
	if m.lastValidRegex != "" && !m.filterActive {
		fmt.Fprintln(&b, m.styles.help.Render("Filter: /"+m.lastValidRegex+"/  (press / to edit, Backspace to clear)"))
	}
	if m.filterActive {
		fmt.Fprintln(&b, m.styles.help.Render("/ "+m.filterQuery+"  (Enter to apply, Esc to cancel)"))
		if m.filterErr != nil {
			fmt.Fprintln(&b, m.styles.error.Render("Invalid regex: "+m.filterErr.Error()))
		}
	}
	fmt.Fprintln(&b, "")

	if len(m.hosts) == 0 {
		if strings.TrimSpace(m.lastValidRegex) != "" {
			fmt.Fprintln(&b, m.styles.error.Render("No hosts match current filter"))
		} else {
			fmt.Fprintln(&b, m.styles.error.Render("No hosts found in ~/.ssh/config"))
		}
		return b.String()
	}

	for i, h := range m.hosts {
		ipText := ""
		if h.IP != "" {
			ipText = "IP: " + h.IP
		}

		parts := []string{
			fmt.Sprintf("%-15s", h.Alias),
			fmt.Sprintf("Hostname: %-25s", h.Hostname),
		}
		if h.Port != "" {
			parts = append(parts, fmt.Sprintf("Port: %-5s", h.Port))
		}
		parts = append(parts, fmt.Sprintf("User: %-10s", h.User))
		if ipText != "" {
			parts = append(parts, ipText)
		}
		if lfLen := len(h.LocalForwards); lfLen == 1 {
			parts = append(parts, h.LocalForwards[0])
		} else if lfLen > 1 {
			parts = append(parts, "LocalForward: "+strings.Join(h.LocalForwards, ","))
		}

		line := strings.Join(parts, "  ")

		if i == m.cursor {
			fmt.Fprintln(&b, m.styles.selected.Render("> "+line))
		} else {
			fmt.Fprintln(&b, m.styles.item.Render("  "+line))
		}
		if m.showNotes && len(h.Notes) > 0 {
			for _, note := range h.Notes {
				if note == "" {
					continue
				}
				fmt.Fprintln(&b, m.styles.help.Render("    > "+note))
			}
		}
	}

	if m.err != nil {
		fmt.Fprintln(&b, "")
		fmt.Fprintln(&b, m.styles.error.Render(m.err.Error()))
	}
	return b.String()
}

func runSSH(host string, localForward string) error {
	// Replace current process with ssh for clean TTY behavior
	bin, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}
	args := []string{"ssh"}
	if localForward != "" {
		args = append(args, "-L", localForward)
	}
	args = append(args, host)
	return syscall.Exec(bin, args, os.Environ())
}

func main() {
	var cfgPath, localForward string
	flag.StringVar(&cfgPath, "config", "", "Path to ssh config (default: ~/.ssh/config)")
	flag.StringVar(&localForward, "L", "", "Local port forward (e.g. 8080:localhost:8080)")
	flag.Parse()

	if cfgPath == "" {
		cfgPath = filepath.Join(os.Getenv("HOME"), ".ssh", "config")
	}

	hosts, err := parseSSHConfig(cfgPath)
	if err != nil && !os.IsNotExist(err) {
		fmt.Fprintln(os.Stderr, "error reading config:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(initialModel(hosts, localForward, cfgPath), tea.WithAltScreen())
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
	if err := runSSH(final.selectedHost.Alias, localForward); err != nil {
		// Fallback: spawn ssh as a subprocess.
		args := []string{}
		if localForward != "" {
			args = append(args, "-L", localForward)
		}
		args = append(args, final.selectedHost.Alias)
		cmd := exec.Command("ssh", args...)
		cmd.Stdin = os.Stdin
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if e := cmd.Run(); e != nil {
			fmt.Fprintln(os.Stderr, "ssh error:", e)
			os.Exit(1)
		}
	}
}
