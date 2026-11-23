//go:build windows && (amd64 || arm64)

package desktopbot

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sptask/sptask_desktop_runner/internal/joblock"
)

// These tests teach and replay forms written with WinForms and WPF
// (testdata/dotnet), the frameworks most installed business programs use.
// The forms are built with the .NET Framework compiler that ships with
// Windows; without it the tests are skipped. Like the other recording tests
// they move the mouse and type.

const vkDown = 0x28

// dotnetForm is a form of testdata/dotnet and how to build it.
type dotnetForm struct {
	name, source, exe string
	refs              []string // relative to the .NET Framework folder
	cityEditID        string   // automation id of the edit box of the drop-down that takes typed text
}

var dotnetForms = []dotnetForm{
	{name: "WinForms", source: "WinFormsForm.cs", exe: "winforms-form.exe",
		refs: []string{"System.Windows.Forms.dll", "System.Drawing.dll"}, cityEditID: "1001"},
	{name: "WPF", source: "WpfForm.cs", exe: "wpf-form.exe",
		refs: []string{`WPF\PresentationFramework.dll`, `WPF\PresentationCore.dll`, `WPF\WindowsBase.dll`, "System.Xaml.dll"}, cityEditID: "PART_EditableTextBox"},
}

// startDotnetForm builds and opens one of the .NET forms.
func startDotnetForm(t *testing.T, form dotnetForm) *fixture {
	t.Helper()
	if os.Getenv("SPTASK_DESKTOP_TESTS") != "1" {
		t.Skip("set SPTASK_DESKTOP_TESTS=1 to open the test windows")
	}
	framework := filepath.Join(os.Getenv("WINDIR"), "Microsoft.NET", "Framework64", "v4.0.30319")
	csc := filepath.Join(framework, "csc.exe")
	if _, err := os.Stat(csc); err != nil {
		t.Skipf("the .NET Framework compiler is not installed: %v", err)
	}
	dir := t.TempDir()
	f := &fixture{
		exe:   filepath.Join(dir, form.exe),
		out:   filepath.Join(dir, "saves.jsonl"),
		title: fmt.Sprintf("SparTask %s %d", form.name, time.Now().UnixNano()),
	}
	args := []string{"-nologo", "-target:winexe", "-codepage:65001", "-out:" + f.exe}
	for _, r := range form.refs {
		args = append(args, "-r:"+filepath.Join(framework, r))
	}
	args = append(args, filepath.Join("testdata", "dotnet", form.source))
	if out, err := exec.Command(csc, args...).CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", form.source, err, out)
	}
	f.cmd = exec.Command(f.exe, f.out, f.title)
	if err := f.cmd.Start(); err != nil {
		t.Fatalf("start %s: %v", form.exe, err)
	}
	t.Cleanup(func() {
		if err := f.cmd.Process.Kill(); err != nil {
			t.Logf("stop %s: %v", form.exe, err)
		}
		if err := f.cmd.Wait(); err != nil {
			t.Logf("%s ended: %v", form.exe, err)
		}
	})
	deadline := time.Now().Add(30 * time.Second)
	for {
		w, ok, err := findTopWindow(Window{App: form.exe, Title: f.title})
		if err != nil {
			t.Fatal(err)
		}
		if ok && uiaReady(w.hwnd, "lblDurum") {
			f.win = w
			return f
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not open", form.exe)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (tc *teacher) down() {
	tc.t.Helper()
	tc.keys([]keyboardInput{{inputType: inputKeyboard, vk: vkDown}, {inputType: inputKeyboard, vk: vkDown, flags: keyeventfKeyUp}})
}

func TestDesktopRecordingInDotNetPrograms(t *testing.T) {
	for _, form := range dotnetForms {
		t.Run(form.name, func(t *testing.T) {
			f := startDotnetForm(t, form)
			p := approvedPolicy(t, f.exe)
			app := strings.ToLower(form.exe)
			m := NewManager(context.Background(), p, &joblock.Lock{})
			done := make(chan Recording, 1)
			if err := m.StartRecording("teach", []string{app}, RecordOptions{MaxDuration: recordTestDuration}, func(r Recording) { done <- r }); err != nil {
				t.Fatal(err)
			}
			tc := newTeacher(t, f)
			tc.click("txtHasta")
			tc.typeText("Ayşe Yılmaz")
			tc.click("txtNot")
			tc.typeText("Satır 1")
			tc.press("Enter") // a new line in the note, not a step
			tc.typeText("Satır 2")
			tc.click("cmbOdeme") // opens the list
			tc.down()
			tc.down()
			tc.press("Enter") // takes Kart and closes the list; the form's default button must not be replayed
			tc.click("cmbSehir")
			tc.typeText("Bursa")
			tc.click("btnKaydet")
			tc.shiftEscape()
			rec := waitRecording(t, done)
			m.Wait()

			got := make([]string, 0, len(rec.Steps))
			for _, s := range rec.Steps {
				got = append(got, describeStep(s))
			}
			want := []string{"focus_window", "fill txtHasta Ayşe Yılmaz", "fill txtNot Satır 1\r\nSatır 2", "select cmbOdeme Kart",
				"fill " + form.cityEditID + " Bursa", "click btnKaydet"}
			if strings.Join(got, " | ") != strings.Join(want, " | ") {
				t.Fatalf("recorded steps:\n%q\nwant:\n%q\nwarnings: %v", got, want, rec.Warnings)
			}
			if city := rec.Steps[4]; city.Label != "cmbSehir" {
				t.Errorf("the drop-down's edit box is labelled %q, want the drop-down's name", city.Label)
			}
			if taught := waitSaves(t, f, 1)[0]; taught["payment"] != "Kart" || taught["city"] != "Bursa" {
				t.Fatalf("the form saved %v while teaching", taught)
			}

			// The process gives other values; the form saves once, with them.
			rec.Steps[1].Value, rec.Steps[3].Value, rec.Steps[4].Value = "Mehmet Demir", "Havale", "İzmir"
			if res := replayNow(t, p, rec, time.Minute); !res.Success {
				t.Fatalf("replay failed: %+v", res)
			}
			replayed := waitSaves(t, f, 2)[1]
			for field, want := range map[string]string{"name": "Mehmet Demir", "note": "Satır 1\r\nSatır 2", "payment": "Havale", "city": "İzmir"} {
				if !sameText(fmt.Sprint(replayed[field]), want) {
					t.Errorf("replay saved %s %q, want %q", field, replayed[field], want)
				}
			}
		})
	}
}
