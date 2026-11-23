// A WinForms data-entry form for the desktop recorder tests, built with the
// .NET Framework compiler that ships with Windows (see frameworks_windows_test.go).
// It runs in the compatibility accessibility mode most installed WinForms
// programs use: its drop-downs have no open action for UI Automation.
//
//	winforms-form.exe <saves.jsonl> <window title>
//
// Kaydet (also Enter, the form's accept button) appends the fields as a JSON line.
using System;
using System.Drawing;
using System.IO;
using System.Runtime.InteropServices;
using System.Text;
using System.Windows.Forms;

static class Program {
    [DllImport("user32.dll")] static extern bool SetProcessDPIAware();

    static string Esc(string s) {
        return s.Replace("\\", "\\\\").Replace("\"", "\\\"").Replace("\r", "\\r").Replace("\n", "\\n");
    }

    [STAThread]
    static void Main(string[] args) {
        SetProcessDPIAware();
        Application.EnableVisualStyles();
        string saves = args[0];
        var f = new Form { Text = args[1], Width = 520, Height = 360, TopMost = true };
        var name = new TextBox { Name = "txtHasta", Left = 130, Top = 16, Width = 330 };
        var note = new TextBox { Name = "txtNot", Left = 130, Top = 50, Width = 330, Height = 60, Multiline = true, AcceptsReturn = true };
        var pay = new ComboBox { Name = "cmbOdeme", Left = 130, Top = 120, Width = 200, DropDownStyle = ComboBoxStyle.DropDownList };
        pay.Items.AddRange(new object[] { "Nakit", "Kart", "Havale" });
        var city = new ComboBox { Name = "cmbSehir", Left = 130, Top = 154, Width = 200, DropDownStyle = ComboBoxStyle.DropDown };
        city.Items.AddRange(new object[] { "Ankara", "İstanbul", "İzmir" });
        var save = new Button { Name = "btnKaydet", Text = "Kaydet", Left = 130, Top = 200, Width = 120, Height = 30 };
        var status = new Label { Name = "lblDurum", Text = "Hazır", Left = 16, Top = 250, Width = 440 };
        int y = 16;
        foreach (var label in new[] { "Hasta adı:", "Not:", "Ödeme:", "Şehir:" }) {
            f.Controls.Add(new Label { Text = label, Left = 16, Top = y + 3, Width = 110 });
            y += label == "Not:" ? 70 : 34;
        }
        f.Controls.AddRange(new Control[] { name, note, pay, city, save, status });
        f.AcceptButton = save;
        int count = 0;
        save.Click += (s, e) => {
            count++;
            File.AppendAllText(saves, "{\"name\":\"" + Esc(name.Text) + "\",\"note\":\"" + Esc(note.Text) +
                "\",\"payment\":\"" + Esc(pay.Text) + "\",\"city\":\"" + Esc(city.Text) + "\"}\n", new UTF8Encoding(false));
            status.Text = "Kaydedildi: " + count;
        };
        Application.Run(f);
    }
}
