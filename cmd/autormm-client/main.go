// Command autormm-client is the operator's CLI: list hosts, watch telemetry,
// and open a remote-desktop session in the browser.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/Paco5687/autormm/client"
	"github.com/Paco5687/autormm/internal/protocol"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "login":
		cmdLogin(os.Args[2:])
	case "hosts", "ls":
		cmdHosts(os.Args[2:])
	case "watch":
		cmdWatch(os.Args[2:])
	case "connect", "rdp":
		cmdConnect(os.Args[2:])
	case "exec", "run":
		cmdExec(os.Args[2:])
	case "inventory", "software":
		cmdInventory(os.Args[2:])
	case "shell", "ssh":
		cmdShell(os.Args[2:])
	case "script", "scripts":
		cmdScript(os.Args[2:])
	case "-h", "--help", "help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Print(`autormm-client — remote monitoring & desktop client

Usage:
  autormm-client login   [--server URL] [--token TOKEN] [--insecure]
  autormm-client hosts
  autormm-client watch   [--interval 3s]
  autormm-client connect <host> [--fps 12] [--quality 60] [--print]
  autormm-client exec    <host> [--shell sh|bash|powershell|cmd] [--timeout 30] <command...>
  autormm-client inventory <host> [--filter substr]
  autormm-client shell   <host>                        # interactive terminal (headless-friendly)
  autormm-client script list | add | run | rm | runs | schedule | schedules | unschedule

Config is stored at ` + client.ConfigPath() + `
Environment overrides: AUTORMM_SERVER, AUTORMM_TOKEN
`)
}

// effectiveConfig loads the saved config and applies env overrides.
func effectiveConfig() *client.Config {
	cfg, err := client.Load()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not read config: %v\n", err)
		cfg = &client.Config{}
	}
	if v := os.Getenv("AUTORMM_SERVER"); v != "" {
		cfg.Server = v
	}
	if v := os.Getenv("AUTORMM_TOKEN"); v != "" {
		cfg.Token = v
	}
	return cfg
}

func requireConfigured(cfg *client.Config) {
	if cfg.Server == "" || cfg.Token == "" {
		fmt.Fprintln(os.Stderr, "not configured — run `autormm-client login` first")
		os.Exit(1)
	}
}

func cmdLogin(args []string) {
	fs := flag.NewFlagSet("login", flag.ExitOnError)
	server := fs.String("server", "", "server base URL")
	token := fs.String("token", "", "admin/access token")
	insecure := fs.Bool("insecure", false, "skip TLS verification")
	fs.Parse(args)

	cfg, _ := client.Load()
	if *server != "" {
		cfg.Server = *server
	}
	if *token != "" {
		cfg.Token = *token
	}
	if *insecure {
		cfg.Insecure = true
	}
	if cfg.Server == "" {
		cfg.Server = prompt("Server URL (e.g. https://rmm.example.com): ")
	}
	if cfg.Token == "" {
		cfg.Token = prompt("Access token: ")
	}

	// Verify before saving.
	if _, err := client.New(cfg).Hosts(); err != nil {
		fmt.Fprintf(os.Stderr, "login check failed: %v\n", err)
		os.Exit(1)
	}
	if err := cfg.Save(); err != nil {
		fmt.Fprintf(os.Stderr, "could not save config: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Logged in to %s (saved to %s)\n", cfg.Server, client.ConfigPath())
}

func cmdHosts(args []string) {
	cfg := effectiveConfig()
	requireConfigured(cfg)
	hosts, err := client.New(cfg).Hosts()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	printHosts(hosts)
}

func cmdWatch(args []string) {
	fs := flag.NewFlagSet("watch", flag.ExitOnError)
	interval := fs.Duration("interval", 3*time.Second, "refresh interval")
	fs.Parse(args)
	cfg := effectiveConfig()
	requireConfigured(cfg)
	c := client.New(cfg)
	for {
		hosts, err := c.Hosts()
		fmt.Print("\033[H\033[2J") // clear screen
		fmt.Printf("autormm — %s — %s\n\n", cfg.Server, time.Now().Format("15:04:05"))
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
		} else {
			printHosts(hosts)
		}
		time.Sleep(*interval)
	}
}

func cmdConnect(args []string) {
	fs := flag.NewFlagSet("connect", flag.ExitOnError)
	fps := fs.Int("fps", 12, "target frames per second")
	quality := fs.Int("quality", 60, "JPEG quality 1-100")
	printOnly := fs.Bool("print", false, "print the viewer URL instead of opening a browser")
	host, rest := popHost(args)
	fs.Parse(rest)
	if host == "" {
		host = fs.Arg(0)
	}
	if host == "" {
		fmt.Fprintln(os.Stderr, "usage: autormm-client connect <host>")
		os.Exit(2)
	}
	cfg := effectiveConfig()
	requireConfigured(cfg)
	c := client.New(cfg)

	hosts, err := c.Hosts()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	h, err := client.FindHost(hosts, host)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !h.Online {
		fmt.Fprintf(os.Stderr, "%s is offline\n", h.Hostname)
		os.Exit(1)
	}
	if !h.CanStream {
		fmt.Fprintf(os.Stderr, "%s does not support screen streaming (no graphical session?)\n", h.Hostname)
		os.Exit(1)
	}

	url, err := c.StartSession(h.AgentID, *fps, *quality)
	if err != nil {
		fmt.Fprintf(os.Stderr, "could not start session: %v\n", err)
		os.Exit(1)
	}
	if *printOnly {
		fmt.Println(url)
		return
	}
	fmt.Printf("Opening remote desktop for %s …\n", h.Hostname)
	if err := openBrowser(url); err != nil {
		fmt.Printf("Open this URL in your browser:\n  %s\n", url)
	}
}

func cmdExec(args []string) {
	fs := flag.NewFlagSet("exec", flag.ExitOnError)
	shell := fs.String("shell", "", "shell: sh|bash|powershell|cmd (default: OS default)")
	timeout := fs.Int("timeout", 30, "command timeout in seconds")
	host, rest := popHost(args)
	fs.Parse(rest)
	cmdArgs := fs.Args()
	if host == "" {
		host = fs.Arg(0)
		cmdArgs = fs.Args()[1:]
	}
	if host == "" || len(cmdArgs) == 0 {
		fmt.Fprintln(os.Stderr, "usage: autormm-client exec <host> <command...>")
		os.Exit(2)
	}
	cfg := effectiveConfig()
	requireConfigured(cfg)
	c := client.New(cfg)

	hosts, err := c.Hosts()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	h, err := client.FindHost(hosts, host)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	command := strings.Join(cmdArgs, " ")

	res, err := c.Exec(h.AgentID, command, *shell, *timeout)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if res.Stdout != "" {
		fmt.Print(res.Stdout)
		if !strings.HasSuffix(res.Stdout, "\n") {
			fmt.Println()
		}
	}
	if res.Stderr != "" {
		fmt.Fprint(os.Stderr, res.Stderr)
	}
	if res.Truncated {
		fmt.Fprintln(os.Stderr, "[output truncated]")
	}
	if res.Error != "" {
		fmt.Fprintln(os.Stderr, "error:", res.Error)
	}
	os.Exit(res.ExitCode)
}

func cmdInventory(args []string) {
	fs := flag.NewFlagSet("inventory", flag.ExitOnError)
	filter := fs.String("filter", "", "only show packages whose name contains this substring")
	host, rest := popHost(args)
	fs.Parse(rest)
	if host == "" {
		host = fs.Arg(0)
	}
	if host == "" {
		fmt.Fprintln(os.Stderr, "usage: autormm-client inventory <host> [--filter substr]")
		os.Exit(2)
	}
	cfg := effectiveConfig()
	requireConfigured(cfg)
	c := client.New(cfg)

	hosts, err := c.Hosts()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	h, err := client.FindHost(hosts, host)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	inv, err := c.Inventory(h.AgentID)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if inv.Error != "" {
		fmt.Fprintln(os.Stderr, "agent error:", inv.Error)
		os.Exit(1)
	}
	needle := strings.ToLower(*filter)
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	shown := 0
	for _, p := range inv.Packages {
		if needle != "" && !strings.Contains(strings.ToLower(p.Name), needle) {
			continue
		}
		fmt.Fprintf(w, "%s\t%s\n", p.Name, p.Version)
		shown++
	}
	w.Flush()
	fmt.Printf("\n%d packages (%s)%s\n", inv.Count, inv.Source,
		func() string {
			if needle != "" {
				return fmt.Sprintf(", %d shown", shown)
			}
			return ""
		}())
}

func cmdShell(args []string) {
	fs := flag.NewFlagSet("shell", flag.ExitOnError)
	host, rest := popHost(args)
	fs.Parse(rest)
	if host == "" {
		host = fs.Arg(0)
	}
	if host == "" {
		fmt.Fprintln(os.Stderr, "usage: autormm-client shell <host>")
		os.Exit(2)
	}
	cfg := effectiveConfig()
	requireConfigured(cfg)
	c := client.New(cfg)

	hosts, err := c.Hosts()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	h, err := client.FindHost(hosts, host)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if err := c.Shell(h.AgentID); err != nil {
		fmt.Fprintln(os.Stderr, "\r\nshell error:", err)
		os.Exit(1)
	}
}

func cmdScript(args []string) {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: autormm-client script <list|add|run|rm|runs|schedule|schedules|unschedule>")
		os.Exit(2)
	}
	cfg := effectiveConfig()
	requireConfigured(cfg)
	c := client.New(cfg)
	sub, rest := args[0], args[1:]

	switch sub {
	case "list":
		scripts, err := c.ListScripts()
		exitIf(err)
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tNAME\tSHELL\tLINES")
		for _, s := range scripts {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\n", s.ID, s.Name, orDefault(s.Shell, "default"), strings.Count(s.Content, "\n")+1)
		}
		w.Flush()

	case "add":
		fs := flag.NewFlagSet("add", flag.ExitOnError)
		name := fs.String("name", "", "script name")
		shell := fs.String("shell", "", "shell: sh|bash|powershell|cmd")
		file := fs.String("file", "", "read script content from this file (default: stdin)")
		fs.Parse(rest)
		if *name == "" {
			fmt.Fprintln(os.Stderr, "script add: --name is required")
			os.Exit(2)
		}
		var content []byte
		var err error
		if *file != "" {
			content, err = os.ReadFile(*file)
		} else {
			content, err = io.ReadAll(os.Stdin)
		}
		exitIf(err)
		if len(content) == 0 {
			fmt.Fprintln(os.Stderr, "script add: empty content")
			os.Exit(2)
		}
		saved, err := c.SaveScript(client.Script{Name: *name, Shell: *shell, Content: string(content)})
		exitIf(err)
		fmt.Printf("saved script %q (id %s)\n", saved.Name, saved.ID)

	case "run":
		if len(rest) < 2 {
			fmt.Fprintln(os.Stderr, "usage: autormm-client script run <script> <host>")
			os.Exit(2)
		}
		sc, err := c.ScriptByRef(rest[0])
		exitIf(err)
		h := resolveHost(c, rest[1])
		run, err := c.RunScript(sc.ID, h.AgentID)
		exitIf(err)
		printRun(run)
		os.Exit(run.ExitCode)

	case "rm":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: autormm-client script rm <script>")
			os.Exit(2)
		}
		sc, err := c.ScriptByRef(rest[0])
		exitIf(err)
		exitIf(c.DeleteScript(sc.ID))
		fmt.Printf("deleted %q\n", sc.Name)

	case "runs":
		fs := flag.NewFlagSet("runs", flag.ExitOnError)
		host := fs.String("host", "", "filter by host")
		limit := fs.Int("limit", 20, "max runs")
		fs.Parse(rest)
		agentID := ""
		if *host != "" {
			agentID = resolveHost(c, *host).AgentID
		}
		runs, err := c.ListRuns(agentID, *limit)
		exitIf(err)
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "WHEN\tSCRIPT\tHOST\tEXIT\tSOURCE")
		for _, r := range runs {
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\n",
				time.Unix(r.Started, 0).Format("01-02 15:04"), r.ScriptName, r.AgentID, r.ExitCode, r.Source)
		}
		w.Flush()

	case "schedule":
		if len(rest) < 3 {
			fmt.Fprintln(os.Stderr, "usage: autormm-client script schedule <script> <host> <cron 5 fields>")
			os.Exit(2)
		}
		sc, err := c.ScriptByRef(rest[0])
		exitIf(err)
		h := resolveHost(c, rest[1])
		cronExpr := strings.Join(rest[2:], " ")
		saved, err := c.SaveSchedule(client.Schedule{ScriptID: sc.ID, AgentID: h.AgentID, Cron: cronExpr, Enabled: true})
		exitIf(err)
		fmt.Printf("scheduled %q on %s: %q (id %s)\n", sc.Name, h.Hostname, cronExpr, saved.ID)

	case "schedules":
		schedules, err := c.ListSchedules()
		exitIf(err)
		scripts, _ := c.ListScripts()
		nameByID := map[string]string{}
		for _, s := range scripts {
			nameByID[s.ID] = s.Name
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tSCRIPT\tHOST\tCRON\tENABLED")
		for _, s := range schedules {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%v\n", s.ID, orDefault(nameByID[s.ScriptID], s.ScriptID), s.AgentID, s.Cron, s.Enabled)
		}
		w.Flush()

	case "unschedule":
		if len(rest) < 1 {
			fmt.Fprintln(os.Stderr, "usage: autormm-client script unschedule <id>")
			os.Exit(2)
		}
		exitIf(c.DeleteSchedule(rest[0]))
		fmt.Println("removed schedule", rest[0])

	default:
		fmt.Fprintf(os.Stderr, "unknown script subcommand %q\n", sub)
		os.Exit(2)
	}
}

func printRun(r *client.Run) {
	if r.Stdout != "" {
		fmt.Print(r.Stdout)
		if !strings.HasSuffix(r.Stdout, "\n") {
			fmt.Println()
		}
	}
	if r.Stderr != "" {
		fmt.Fprint(os.Stderr, r.Stderr)
	}
	if r.Error != "" {
		fmt.Fprintln(os.Stderr, "error:", r.Error)
	}
}

func resolveHost(c *client.Client, ref string) *protocol.HostView {
	hosts, err := c.Hosts()
	exitIf(err)
	h, err := client.FindHost(hosts, ref)
	exitIf(err)
	return h
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

func exitIf(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// ---- helpers ----

// popHost pulls a leading <host> argument out so that flags can follow it
// (Go's flag package otherwise stops parsing at the first positional).
func popHost(args []string) (host string, rest []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func prompt(label string) string {
	fmt.Print(label)
	s, _ := bufio.NewReader(os.Stdin).ReadString('\n')
	return strings.TrimSpace(s)
}

func printHosts(hosts []protocol.HostView) {
	if len(hosts) == 0 {
		fmt.Println("(no hosts connected)")
		return
	}
	w := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(w, "STATUS\tHOST\tOS\tCPU\tMEM\tUPTIME\tALERTS")
	for _, h := range hosts {
		status := "●online"
		if !h.Online {
			status = "○offline"
		}
		cpu, mem, up := "—", "—", "—"
		if h.Metrics != nil {
			cpu = fmt.Sprintf("%.0f%%", h.Metrics.CPUPercent)
			mem = fmt.Sprintf("%.0f%%", h.Metrics.MemPercent)
			up = fmtUptime(h.Metrics.UptimeSecs)
		}
		stream := ""
		if h.CanStream {
			stream = " ⧉"
		}
		fmt.Fprintf(w, "%s\t%s%s\t%s\t%s\t%s\t%s\t%s\n",
			status, h.Hostname, stream, short(h.Platform, 22), cpu, mem, up, strings.Join(h.Alerts, ","))
	}
	w.Flush()
}

func fmtUptime(s uint64) string {
	if s == 0 {
		return "—"
	}
	d := s / 86400
	h := (s % 86400) / 3600
	m := (s % 3600) / 60
	if d > 0 {
		return fmt.Sprintf("%dd%dh", d, h)
	}
	if h > 0 {
		return fmt.Sprintf("%dh%dm", h, m)
	}
	return fmt.Sprintf("%dm", m)
}

func short(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func openBrowser(url string) error {
	switch runtime.GOOS {
	case "linux":
		return exec.Command("xdg-open", url).Start()
	case "windows":
		return exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		return exec.Command("open", url).Start()
	default:
		return fmt.Errorf("unsupported platform")
	}
}
