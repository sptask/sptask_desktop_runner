package desktopbot

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ApprovalsFile is kept in the runner's data folder. Only the person at this
// computer adds to it (sptask-runner --approve-app); nothing the server sends
// can, so a recording can only narrow what was approved here.
const ApprovalsFile = "desktop-apps.json"

// deniedApps are never touched, whatever was approved: shells and script
// hosts, the Windows shell and management consoles, sign-in and elevation
// prompts, and browsers (the browser bot has its own origin guard).
var deniedApps = map[string]bool{
	"cmd.exe": true, "powershell.exe": true, "powershell_ise.exe": true, "pwsh.exe": true,
	"wscript.exe": true, "cscript.exe": true, "mshta.exe": true, "rundll32.exe": true, "regsvr32.exe": true,
	"regedit.exe": true, "regedt32.exe": true, "conhost.exe": true, "openconsole.exe": true,
	"windowsterminal.exe": true, "wt.exe": true, "bash.exe": true, "wsl.exe": true, "wslhost.exe": true,
	"explorer.exe": true, "taskmgr.exe": true, "mmc.exe": true, "control.exe": true, "systemsettings.exe": true,
	"consent.exe": true, "credentialuibroker.exe": true, "logonui.exe": true, "lockapp.exe": true,
	"chrome.exe": true, "msedge.exe": true, "firefox.exe": true, "brave.exe": true, "opera.exe": true, "iexplore.exe": true,
}

type approvedApp struct {
	Path       string    `json:"path"`
	ApprovedAt time.Time `json:"approved_at"`
}

// Policy decides which program windows the bot may act in.
type Policy struct {
	file string
	self string

	mu       sync.Mutex
	approved map[string]approvedApp
}

// LoadPolicy reads the approvals of dataDir. A missing file means nothing is
// approved yet.
func LoadPolicy(dataDir string) (*Policy, error) {
	self, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("find the runner program: %w", err)
	}
	p := &Policy{file: filepath.Join(dataDir, ApprovalsFile), self: normalizePath(self), approved: map[string]approvedApp{}}
	raw, err := os.ReadFile(p.file)
	if errors.Is(err, os.ErrNotExist) {
		return p, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", p.file, err)
	}
	var list []approvedApp
	if err := json.Unmarshal(raw, &list); err != nil {
		return nil, fmt.Errorf("read %s: %w", p.file, err)
	}
	for _, a := range list {
		p.approved[normalizePath(a.Path)] = a
	}
	return p, nil
}

// normalizePath makes two spellings of one program path equal. Windows paths
// are case-insensitive.
func normalizePath(path string) string {
	return strings.ToLower(filepath.Clean(strings.TrimSpace(path)))
}

func (p *Policy) denied(path string) bool {
	return deniedApps[strings.ToLower(filepath.Base(path))] || normalizePath(path) == p.self
}

// Approve lets the bot act in the program at path (a full path to an .exe).
func (p *Policy) Approve(path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("give the full path of the program, got %q", path)
	}
	if _, err := NormalizeApp(filepath.Base(path)); err != nil {
		return err
	}
	if p.denied(path) {
		return botErr(CodeAppBlocked, "%s can never be automated", filepath.Base(path))
	}
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("check %s: %w", path, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a folder, not a program", path)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	p.approved[normalizePath(path)] = approvedApp{Path: filepath.Clean(path), ApprovedAt: time.Now().UTC()}
	return p.saveLocked()
}

// Revoke removes an approval. It reports whether the program was approved.
func (p *Policy) Revoke(path string) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := normalizePath(path)
	if _, ok := p.approved[key]; !ok {
		return false, nil
	}
	delete(p.approved, key)
	return true, p.saveLocked()
}

// Approved lists the approved program paths.
func (p *Policy) Approved() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]string, 0, len(p.approved))
	for _, a := range p.approved {
		out = append(out, a.Path)
	}
	sort.Strings(out)
	return out
}

func (p *Policy) saveLocked() error {
	list := make([]approvedApp, 0, len(p.approved))
	for _, a := range p.approved {
		list = append(list, a)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Path < list[j].Path })
	raw, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return fmt.Errorf("encode approvals: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(p.file), 0o700); err != nil {
		return fmt.Errorf("create %s: %w", filepath.Dir(p.file), err)
	}
	if err := os.WriteFile(p.file, raw, 0o600); err != nil {
		return fmt.Errorf("write %s: %w", p.file, err)
	}
	return nil
}

// check decides whether the bot may act in a window of the program at path,
// for a recording that allows the program names in allowed.
func (p *Policy) check(path string, allowed map[string]bool) error {
	if path == "" {
		return botErr(CodeAppBlocked, "the window's program could not be identified")
	}
	name := strings.ToLower(filepath.Base(path))
	if p.denied(path) {
		return botErr(CodeAppBlocked, "%s can never be automated", name)
	}
	if !allowed[name] {
		return botErr(CodeAppBlocked, "%s is not in the recording's allowed apps", name)
	}
	p.mu.Lock()
	_, ok := p.approved[normalizePath(path)]
	p.mu.Unlock()
	if !ok {
		return botErr(CodeAppNotApproved, "%s is not approved on this computer (sptask-runner --approve-app=%q)", name, path)
	}
	return nil
}

// approvesName reports whether some program with the file name name (lower
// case, "eczane.exe") is approved on this computer.
func (p *Policy) approvesName(name string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	for key := range p.approved {
		if filepath.Base(key) == name {
			return true
		}
	}
	return false
}
